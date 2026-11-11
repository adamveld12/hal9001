package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hal9001/content"
	"hal9001/internal/agent/agenttest"
	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/apispec"
	"hal9001/internal/cli"
	"hal9001/internal/daemon"
	"hal9001/internal/spec"
)

var (
	fakeAgentBin string
	fakeAgentDir string
	// hal9001Bin is the real CLI binary, built once; assist tests put it on the agents' PATH.
	hal9001Bin string
)

// TestMain builds the fake ACP agent once for the whole suite. Under -short
// every test skips, so nothing is built.
func TestMain(m *testing.M) {
	flag.Parse()
	code := 0
	if !testing.Short() {
		dir, err := os.MkdirTemp("", "hal-e2e-agent-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(1)
		}
		fakeAgentDir = dir
		fakeAgentBin, err = agenttest.BuildFakeAgent(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			_ = os.RemoveAll(dir)
			os.Exit(1)
		}
		if hal9001Bin, err = buildHal9001(dir); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			_ = os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	code = m.Run()
	if fakeAgentDir != "" {
		_ = os.RemoveAll(fakeAgentDir)
	}
	os.Exit(code)
}

// Daemon is one real hal9001 daemon, running in-process on a loopback port,
// with a temp home. Configure it (Runtime, Sensor, Flow, ...) before Start;
// Restart runs a new daemon on the same home and database.
type Daemon struct {
	t    *testing.T
	Home string
	// Bin is a directory placed first on PATH for the daemon and everything it
	// spawns; tests put fake gh, tailscale, ... in it with Bin.
	Bin string

	mu       sync.Mutex
	config   string
	runtimes map[string]fakeagent.Script
	kinds    map[string]string // runtime name -> `kind:` (the lockdown family)
	demo     bool

	addr   net.Addr
	cancel context.CancelFunc
	done   chan error
	logOK  atomic.Bool

	// Cookie, when set, is sent with every request.
	Cookie string

	// AssistIdle is the assist idle timeout (the daemon default when zero).
	AssistIdle time.Duration

	// Clock, when set by withClock, drives the daemon's sensor schedules and
	// agent timeouts; advance it with Tick. It starts at the wall-clock time and
	// keeps its value across Restart.
	Clock *agenttest.ManualClock
}

// Option configures newDaemon.
type Option func(*Daemon)

// withDemo keeps the seeded demo sensor and flow (every other starter sensor
// and flow is neutralized, because they need credentials and the network).
func withDemo() Option { return func(d *Daemon) { d.demo = true } }

// withClock gives the daemon a manual clock for its schedulers and timeouts
// instead of the wall clock. Interval sensors then poll once at start (they
// are overdue) and again only when the test calls Tick.
func withClock() Option {
	return func(d *Daemon) { d.Clock = agenttest.NewManualClockAt(time.Now()) }
}

func skipShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end suite: runs a real daemon and spawns processes")
	}
}

// newDaemon creates (but does not start) a daemon on a fresh temp home seeded
// from content/ with the starter sensors and flows removed, and a bin dir with
// a recording fake terminal-notifier first on PATH.
func newDaemon(t *testing.T, opts ...Option) *Daemon {
	t.Helper()
	skipShort(t)
	d := &Daemon{t: t, Home: t.TempDir(), Bin: t.TempDir(), runtimes: map[string]fakeagent.Script{}, kinds: map[string]string{}}
	for _, o := range opts {
		o(d)
	}
	if _, err := spec.Init(d.Home, content.FS); err != nil {
		t.Fatal(err)
	}
	// The starter sensors and flows need credentials and the network, so they
	// are removed (withDemo keeps the credential-free demo pair). Init does not
	// re-seed an existing home, so they stay gone when the daemon starts.
	for _, dir := range []string{spec.DirSensors, spec.DirFlows} {
		entries, err := os.ReadDir(filepath.Join(d.Home, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			id := strings.TrimSuffix(e.Name(), ".yaml")
			if d.demo && (id == "demo-ticker" || id == "demo") {
				continue
			}
			if err := os.Remove(filepath.Join(d.Home, dir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", d.Bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	d.Script("terminal-notifier", `{ for a in "$@"; do printf '%s\037' "$a"; done; printf '\036'; } >> `+shq(d.notifyLog()))
	t.Cleanup(d.Stop)
	return d
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (d *Daemon) notifyLog() string { return filepath.Join(d.Bin, "notifications.log") }

// Script installs an executable shell script named name in the bin dir.
func (d *Daemon) Script(name, body string) {
	d.t.Helper()
	p := filepath.Join(d.Bin, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // test executable
		d.t.Fatal(err)
	}
}

// Notifications returns every notification the daemon raised, as the argv its
// terminal-notifier was run with ("-title", "...", "-message", "...", "-open", url).
func (d *Daemon) Notifications() []map[string]string {
	d.t.Helper()
	b, err := os.ReadFile(d.notifyLog())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		d.t.Fatal(err)
	}
	var out []map[string]string
	for _, rec := range strings.Split(string(b), "\x1e") {
		if rec == "" {
			continue
		}
		args := strings.Split(strings.TrimSuffix(rec, "\x1f"), "\x1f")
		m := map[string]string{}
		for i := 0; i+1 < len(args); i += 2 {
			m[strings.TrimPrefix(args[i], "-")] = args[i+1]
		}
		out = append(out, m)
	}
	return out
}

// Config sets extra top-level config.yaml text; the runtimes registered with
// Runtime are appended when the file is written at Start.
func (d *Daemon) Config(yaml string) { d.config = yaml }

// Runtime registers a fake ACP runtime named name that plays s. Defaults: modes
// default/bypass (config modes ask: default, full: bypass), sessions persisted
// so session/load works when s.LoadSession is set.
func (d *Daemon) Runtime(name string, s fakeagent.Script) {
	d.t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runtimes[name] = s
}

// RuntimeKind declares the lockdown family (`kind:`) of a fake runtime, so the daemon locks it down like that real one.
func (d *Daemon) RuntimeKind(name, kind string) {
	d.t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.kinds[name] = kind
}

// Write writes a home-relative file.
func (d *Daemon) Write(rel, data string) {
	d.t.Helper()
	p := filepath.Join(d.Home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		d.t.Fatal(err)
	}
}

// Read reads a home-relative file.
func (d *Daemon) Read(rel string) string {
	d.t.Helper()
	b, err := os.ReadFile(filepath.Join(d.Home, rel))
	if err != nil {
		d.t.Fatal(err)
	}
	return string(b)
}

// Path returns an absolute path under the home dir.
func (d *Daemon) Path(rel string) string { return filepath.Join(d.Home, rel) }

func (d *Daemon) Sensor(id, yaml string) { d.t.Helper(); d.Write("sensors/"+id+".yaml", yaml) }
func (d *Daemon) Flow(id, yaml string)   { d.t.Helper(); d.Write("flows/"+id+".yaml", yaml) }
func (d *Daemon) Env(content string)     { d.t.Helper(); d.Write("env", content) }

// Prompt writes prompts/<name>.md with the given output kind (note | none).
func (d *Daemon) Prompt(name, output, body string) {
	d.t.Helper()
	d.Write("prompts/"+name+".md", "---\nname: "+name+"\ndescription: e2e\noutput: "+output+"\n---\n"+body)
}

// IdleSensor defines sensor id that never emits; tests inject events into it.
func (d *Daemon) IdleSensor(id string) {
	d.t.Helper()
	d.Sensor(id, "id: "+id+"\ninterval: 1h\nlang: bash\nscript: \"true\"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
}

func (d *Daemon) writeConfig() {
	d.t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	var b strings.Builder
	b.WriteString(d.config)
	b.WriteString("\n")
	names := make([]string, 0, len(d.runtimes))
	for n := range d.runtimes {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		b.WriteString("runtimes:\n")
	}
	for _, n := range names {
		s := d.runtimes[n]
		if s.Modes == nil {
			s.Modes = []fakeagent.Mode{{ID: "default"}, {ID: "bypass"}}
		}
		dir := filepath.Join(d.Home, "fake", n)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			d.t.Fatal(err)
		}
		s.SessionsDir = filepath.Join(dir, "sessions")
		s.LogPath = filepath.Join(dir, "calls.jsonl")
		script := filepath.Join(dir, "script.json")
		if err := s.Write(script); err != nil {
			d.t.Fatal(err)
		}
		fmt.Fprintf(&b, "  %s:\n    command: [%q, --script, %q]\n    modes: {ask: default, full: bypass}\n", n, fakeAgentBin, script)
		if k := d.kinds[n]; k != "" {
			fmt.Fprintf(&b, "    kind: %s\n", k)
		}
	}
	if err := os.WriteFile(filepath.Join(d.Home, "config.yaml"), []byte(b.String()), 0o600); err != nil {
		d.t.Fatal(err)
	}
}

// AgentCalls returns the ACP calls the fake runtime received (its call log).
func (d *Daemon) AgentCalls(runtime string) []map[string]any {
	d.t.Helper()
	b, err := os.ReadFile(filepath.Join(d.Home, "fake", runtime, "calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		d.t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			d.t.Fatalf("call log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

type logWriter struct{ d *Daemon }

func (w logWriter) Write(p []byte) (int, error) {
	if w.d.logOK.Load() {
		w.d.t.Log("daemon: " + strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// Start writes config.yaml and runs the daemon until Stop or test cleanup.
func (d *Daemon) Start() {
	d.t.Helper()
	d.writeConfig()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	d.logOK.Store(true)
	opts := daemon.Options{
		Home: d.Home, Version: "e2e", ListenAddr: "127.0.0.1:0", AssistIdleTimeout: d.AssistIdle, Executable: hal9001Bin,
		// Only the fakes in Bin can answer as tailscale: never the Tailscale of the machine the suite runs on.
		TailscaleExact: true, TailscalePATH: d.Bin,
		Log:   slog.New(slog.NewTextHandler(logWriter{d}, &slog.HandlerOptions{Level: slog.LevelWarn})),
		Ready: func(a net.Addr) { ready <- a },
	}
	if d.Clock != nil { // a nil *ManualClock in the interface would not mean "wall clock"
		opts.Clock = d.Clock
	}
	go func() { done <- daemon.Run(ctx, opts) }()
	select {
	case d.addr = <-ready:
	case err := <-done:
		cancel()
		d.t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		d.t.Fatal("daemon did not start within 30s")
	}
	d.cancel, d.done = cancel, done
}

// Stop shuts the daemon down gracefully (as a launchd stop would). It is a
// no-op when the daemon is not running.
func (d *Daemon) Stop() {
	d.t.Helper()
	if d.cancel == nil {
		return
	}
	d.cancel()
	select {
	case err := <-d.done:
		if err != nil {
			d.t.Errorf("daemon shutdown: %v", err)
		}
	case <-time.After(45 * time.Second):
		d.t.Error("daemon did not shut down within 45s")
	}
	d.cancel, d.done = nil, nil
	d.logOK.Store(false)
}

// Restart stops the daemon and starts a new one on the same home. The listen
// port changes.
func (d *Daemon) Restart() {
	d.t.Helper()
	d.Stop()
	d.Start()
}

// Addr is the daemon's host:port.
func (d *Daemon) Addr() string { return d.addr.String() }

// URL is the daemon's base URL.
func (d *Daemon) URL() string { return "http://" + d.Addr() }

// Port is the daemon's TCP port.
func (d *Daemon) Port() int { return d.addr.(*net.TCPAddr).Port }

// ---- HTTP client ----

// Resp is one HTTP response.
type Resp struct {
	d      *Daemon
	Status int
	Header http.Header
	Body   []byte
}

func (r Resp) String() string { return fmt.Sprintf("HTTP %d %s", r.Status, r.Body) }

// JSON decodes the body into v, failing the test when it is not JSON of that shape.
func (r Resp) JSON(v any) {
	r.d.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.d.t.Fatalf("decode %T from %s: %v", v, r, err)
	}
}

// Problem returns the problem+json `type` suffix (for example "conflict"), or "".
func (r Resp) Problem() string {
	var p struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(r.Body, &p) != nil {
		return ""
	}
	return strings.TrimPrefix(p.Type, "urn:hal9001:problem:")
}

// Want fails the test unless the status is one of codes; it returns r for chaining.
func (r Resp) Want(codes ...int) Resp {
	r.d.t.Helper()
	for _, c := range codes {
		if r.Status == c {
			return r
		}
	}
	r.d.t.Fatalf("status %d, want %v: %s", r.Status, codes, r.Body)
	return r
}

// ReqOpt adjusts one request.
type ReqOpt func(*reqSpec)

type reqSpec struct {
	host        string
	header      http.Header
	contentType string
	rawBody     *string
	noCookie    bool
	noContract  bool
}

// withHost sets the Host header.
func withHost(h string) ReqOpt { return func(r *reqSpec) { r.host = h } }

// withHeader sets a request header.
func withHeader(k, v string) ReqOpt { return func(r *reqSpec) { r.header.Set(k, v) } }

// withContentType overrides the Content-Type of a request with a body.
func withContentType(ct string) ReqOpt { return func(r *reqSpec) { r.contentType = ct } }

// withRawBody sends s verbatim instead of JSON-encoding the body.
func withRawBody(s string) ReqOpt { return func(r *reqSpec) { r.rawBody = &s } }

// withoutCookie omits the session cookie.
func withoutCookie() ReqOpt { return func(r *reqSpec) { r.noCookie = true } }

// withoutContract skips the check against api/openapi.yaml (for requests the
// contract does not describe).
func withoutContract() ReqOpt { return func(r *reqSpec) { r.noContract = true } }

// Do sends one request and checks the response against the OpenAPI contract.
// body is JSON-encoded unless nil.
func (d *Daemon) Do(method, path string, body any, opts ...ReqOpt) Resp {
	d.t.Helper()
	rs := &reqSpec{header: http.Header{}, contentType: "application/json"}
	for _, o := range opts {
		o(rs)
	}
	var rd io.Reader
	switch {
	case rs.rawBody != nil:
		rd = strings.NewReader(*rs.rawBody)
	case body != nil:
		b, err := json.Marshal(body)
		if err != nil {
			d.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, d.URL()+path, rd)
	if err != nil {
		d.t.Fatal(err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Content-Type", rs.contentType)
	}
	for k, v := range rs.header {
		req.Header[k] = v
	}
	if rs.host != "" {
		req.Host = rs.host
	}
	if d.Cookie != "" && !rs.noCookie {
		req.Header.Set("Cookie", d.Cookie)
	}
	client := &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !rs.noContract {
		cr := req.Clone(context.Background())
		cr.URL.Host = "127.0.0.1:9001"
		apispec.ValidateResponse(d.t, cr, resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		d.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return Resp{d: d, Status: resp.StatusCode, Header: resp.Header, Body: data}
}

func (d *Daemon) Get(path string, opts ...ReqOpt) Resp {
	d.t.Helper()
	return d.Do("GET", path, nil, opts...)
}
func (d *Daemon) Post(path string, body any, opts ...ReqOpt) Resp {
	d.t.Helper()
	return d.Do("POST", path, body, opts...)
}
func (d *Daemon) Put(path string, body any, opts ...ReqOpt) Resp {
	d.t.Helper()
	return d.Do("PUT", path, body, opts...)
}
func (d *Daemon) Patch(path string, body any, opts ...ReqOpt) Resp {
	d.t.Helper()
	return d.Do("PATCH", path, body, opts...)
}
func (d *Daemon) Delete(path string, opts ...ReqOpt) Resp {
	d.t.Helper()
	return d.Do("DELETE", path, nil, opts...)
}

// CLI runs the hal9001 CLI against this daemon (it is a thin API client) and
// returns stdout, stderr and the exit code.
func (d *Daemon) CLI(stdin string, args ...string) (stdout, stderr string, code int) {
	d.t.Helper()
	var out, errb bytes.Buffer
	env := map[string]string{cli.EnvURL: d.URL(), cli.EnvHome: d.Home}
	if d.Cookie != "" {
		env[cli.EnvSessionCookie] = d.Cookie
	}
	code = cli.Run(context.Background(), args, cli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Version: "e2e",
		Getenv: func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return os.Getenv(k)
		},
	})
	return out.String(), errb.String(), code
}

// Tick advances the daemon's manual clock by dur and returns once every
// scheduler timer it fired has handled its tick (polled, or skipped because the
// sensor or the daemon is paused) and armed the next one. It first waits for a
// timer that dur will fire, so a scheduler still busy with its previous tick
// (a poll running, the next timer not armed yet) does not lose this one. Tests
// that Tick do not run agents on the clock. It fails if no timer due within dur
// appears.
func (d *Daemon) Tick(dur time.Duration) {
	d.t.Helper()
	if d.Clock == nil {
		d.t.Fatal("Tick needs a daemon created withClock()")
	}
	if !d.Clock.Tick(dur, 30*time.Second) {
		d.t.Fatalf("no scheduler timer due within %s, or the timers did not re-arm after the tick", dur)
	}
}

// Eventually polls cond every 5ms until it holds, failing with what after 30s.
func (d *Daemon) Eventually(what string, cond func() bool) {
	d.t.Helper()
	d.EventuallyWithin(30*time.Second, what, cond)
}

// EventuallyWithin is Eventually with an explicit deadline.
func (d *Daemon) EventuallyWithin(limit time.Duration, what string, cond func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out after %s waiting for %s", limit, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// buildHal9001 compiles cmd/hal9001 into dir and returns the path. Assist agents run it as `hal9001 assist test`.
func buildHal9001(dir string) (string, error) {
	bin := filepath.Join(dir, "hal9001")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", bin, "hal9001/cmd/hal9001") //nolint:gosec // fixed arguments
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build hal9001: %w\n%s", err, out)
	}
	return bin, nil
}

// InstallCLI puts the real hal9001 binary on the PATH of the daemon and everything it spawns (the agents).
func (d *Daemon) InstallCLI() {
	d.t.Helper()
	if err := os.Symlink(hal9001Bin, filepath.Join(d.Bin, "hal9001")); err != nil {
		d.t.Fatal(err)
	}
}
