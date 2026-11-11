package runtimes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/spec"
)

// lab is a temp directory with stub executables on PATH and a data directory.
type lab struct {
	t    *testing.T
	bin  string
	data string
}

func newLab(t *testing.T) *lab {
	t.Helper()
	root := t.TempDir()
	l := &lab{t: t, bin: filepath.Join(root, "bin"), data: filepath.Join(root, "data")}
	for _, d := range []string{l.bin, l.data} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// stub writes an executable shell script into dir (default: the lab's bin).
func (l *lab) stub(name, body string) string { return l.stubIn(l.bin, name, body) }

func (l *lab) stubIn(dir, name, body string) string {
	l.t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		l.t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // test stub
		l.t.Fatal(err)
	}
	return p
}

func (l *lab) manager() *Manager {
	l.t.Helper()
	path := l.bin + ":/usr/bin:/bin"
	m := New(Options{Data: l.data, Path: path, ExactPath: true, UserHome: l.t.TempDir()})
	l.t.Cleanup(m.Close)
	return m
}

// adapter installs a fake adapter package the way npm lays it out and returns its script path.
func (l *lab) adapter(kind, version string) string {
	l.t.Helper()
	ai := kinds[kind].adapter
	pkg := filepath.Join(l.data, "adapters", ai.bin, "node_modules", filepath.FromSlash(ai.pkg))
	if err := os.MkdirAll(filepath.Join(pkg, "dist"), 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"`+ai.pkg+`","version":"`+version+`","bin":{"`+ai.bin+`":"dist/index.js"}}`), 0o644); err != nil { //nolint:gosec // test file
		l.t.Fatal(err)
	}
	script := filepath.Join(pkg, "dist", "index.js")
	if err := os.WriteFile(script, []byte("// adapter\n"), 0o644); err != nil { //nolint:gosec // test file
		l.t.Fatal(err)
	}
	return script
}

func (l *lab) node(version string) string { return l.stub("node", `echo v`+version) }

const claudeSignedIn = `case "$1" in --version) echo "2.1.311 (Claude Code)";; auth) echo '{"loggedIn": true, "authMethod": "claude.ai"}';; esac`

func codes(d Detection) []string {
	var out []string
	for _, w := range d.Warnings {
		out = append(out, w.Code)
	}
	return out
}

func detect(t *testing.T, m *Manager, name string, rt spec.Runtime) Detection {
	t.Helper()
	return m.Detect(context.Background(), name, rt, true)
}

func TestReadyWhenCLIAdapterNodeAndLoginArePresent(t *testing.T) {
	l := newLab(t)
	l.stub("claude", claudeSignedIn)
	node := l.node("24.1.0")
	script := l.adapter("claude", ClaudeAdapterVersion)
	d := detect(t, l.manager(), "claude", spec.Runtime{Kind: "claude"})
	if d.Status != StatusReady || len(d.Warnings) != 0 {
		t.Fatalf("status = %s, warnings = %v, want ready and none", d.Status, codes(d))
	}
	if !slices.Equal(d.Command, []string{node, script}) {
		t.Errorf("command = %v, want [node script]", d.Command)
	}
	if d.CLI.Version == nil || *d.CLI.Version != "2.1.311" || d.Auth.Status != AuthOK {
		t.Errorf("cli = %+v auth = %+v", d.CLI, d.Auth)
	}
	if d.Adapter.Source != SourceManaged || !d.Adapter.Installed || *d.Adapter.Version != ClaudeAdapterVersion || *d.Adapter.PinnedVersion != ClaudeAdapterVersion || !d.Adapter.Installable {
		t.Errorf("adapter = %+v", d.Adapter)
	}
	if !d.Node.OK || *d.Node.Version != "24.1.0" {
		t.Errorf("node = %+v", d.Node)
	}
}

func TestMissingCLIWarnsButAnInstalledAdapterStillRuns(t *testing.T) {
	l := newLab(t)
	l.node("22.0.0")
	l.adapter("codex", CodexAdapterVersion)
	d := detect(t, l.manager(), "codex", spec.Runtime{Kind: "codex"})
	if d.Status != StatusWarning || !slices.Equal(codes(d), []string{CodeCLIMissing}) {
		t.Fatalf("status = %s, warnings = %v", d.Status, codes(d))
	}
	if d.CLI.Found || d.Auth.Status != AuthUnknown || d.Auth.Command == nil || *d.Auth.Command != "codex login" {
		t.Errorf("cli = %+v auth = %+v", d.CLI, d.Auth)
	}
}

func TestBrokenShimCountsAsNotFound(t *testing.T) {
	l := newLab(t)
	shims := filepath.Join(filepath.Dir(l.bin), "shims")
	l.stubIn(shims, "claude", `echo "mise: No version is set for shim: claude" >&2; exit 126`)
	l.stub("claude", claudeSignedIn)
	m := l.manager()
	// The shim comes first on PATH: it must be skipped, not chosen.
	m.path = shims + ":" + m.path
	d := detect(t, m, "claude", spec.Runtime{Kind: "claude"})
	if !d.CLI.Found || *d.CLI.Path != filepath.Join(l.bin, "claude") {
		t.Errorf("cli = %+v, want the working claude behind the broken shim", d.CLI)
	}

	only := newLab(t)
	only.stubIn(only.bin, "claude", `exit 126`)
	if d := detect(t, only.manager(), "claude", spec.Runtime{Kind: "claude"}); d.CLI.Found {
		t.Errorf("a shim that cannot run was found: %+v", d.CLI)
	}
}

func TestLoginStates(t *testing.T) {
	tests := []struct {
		name, kind, script string
		auth               string
		code               string
	}{
		{"claude signed out", "claude", `case "$1" in --version) echo 2.1.0;; auth) echo '{"loggedIn": false}'; exit 1;; esac`, AuthMissing, CodeAuthMissing},
		{"claude signed out without json", "claude", `case "$1" in --version) echo 2.1.0;; auth) exit 1;; esac`, AuthMissing, CodeAuthMissing},
		{"claude check crashes", "claude", `case "$1" in --version) echo 2.1.0;; auth) echo boom >&2; exit 7;; esac`, AuthUnknown, CodeAuthUnknown},
		{"codex signed in", "codex", `case "$1" in --version) echo codex-cli 0.162.1;; login) echo "Logged in using ChatGPT";; esac`, AuthOK, ""},
		{"codex signed out", "codex", `case "$1" in --version) echo codex-cli 0.162.1;; login) echo "Not logged in"; exit 1;; esac`, AuthMissing, CodeAuthMissing},
		{"codex broken config", "codex", `case "$1" in --version) echo codex-cli 0.162.1;; login) echo "Error loading configuration" >&2; exit 1;; esac`, AuthUnknown, CodeAuthUnknown},
		{"omp with a provider", "omp", `case "$1" in --version) echo omp/18.18.7;; models) echo '{"models":[{"provider":"apple"},{"provider":"anthropic"}]}';; esac`, AuthOK, ""},
		{"omp without providers", "omp", `case "$1" in --version) echo omp/18.18.7;; models) echo '{"models":[{"provider":"apple","id":"on-device"}]}';; esac`, AuthMissing, CodeProvidersMissing},
		{"omp models fails", "omp", `case "$1" in --version) echo omp/18.18.7;; models) exit 2;; esac`, AuthUnknown, CodeAuthUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t)
			l.stub(tc.kind, tc.script)
			l.node("24.0.0")
			l.adapter("claude", ClaudeAdapterVersion)
			l.adapter("codex", CodexAdapterVersion)
			d := detect(t, l.manager(), tc.kind, spec.Runtime{Kind: tc.kind})
			if d.Auth.Status != tc.auth {
				t.Errorf("auth = %s, want %s", d.Auth.Status, tc.auth)
			}
			want := []string(nil)
			if tc.code != "" {
				want = []string{tc.code}
			}
			if !slices.Equal(codes(d), want) {
				t.Errorf("warnings = %v, want %v", codes(d), want)
			}
			wantStatus := StatusReady
			if tc.code != "" {
				wantStatus = StatusWarning
			}
			if d.Status != wantStatus {
				t.Errorf("status = %s, want %s", d.Status, wantStatus)
			}
		})
	}
}

func TestNodeMissingAndOld(t *testing.T) {
	l := newLab(t)
	l.stub("claude", claudeSignedIn)
	l.adapter("claude", ClaudeAdapterVersion)
	d := detect(t, l.manager(), "claude", spec.Runtime{Kind: "claude"})
	if d.Status != StatusUnavailable || d.Command != nil || !slices.Contains(codes(d), CodeNodeMissing) || d.Node.Found {
		t.Errorf("no node: status = %s command = %v warnings = %v", d.Status, d.Command, codes(d))
	}

	l.node("20.11.0")
	d = detect(t, l.manager(), "claude", spec.Runtime{Kind: "claude"})
	if d.Status != StatusWarning || !slices.Contains(codes(d), CodeNodeOld) || d.Node.OK || len(d.Command) != 2 {
		t.Errorf("old node: status = %s command = %v warnings = %v node = %+v", d.Status, d.Command, codes(d), d.Node)
	}
}

func TestAdapterMissingIsUnavailableWithInstallFix(t *testing.T) {
	l := newLab(t)
	l.stub("codex", `case "$1" in --version) echo codex-cli 0.1.0;; login) echo ok;; esac`)
	l.node("24.0.0")
	d := detect(t, l.manager(), "codex", spec.Runtime{Kind: "codex"})
	if d.Status != StatusUnavailable || d.Command != nil || d.Adapter.Source != SourceNone || d.Adapter.Installed || !d.Adapter.Installable {
		t.Fatalf("status = %s adapter = %+v", d.Status, d.Adapter)
	}
	w := d.Warnings[0]
	if w.Code != CodeAdapterMissing || w.Command == nil || *w.Command != "hal9001 runtimes install codex" {
		t.Errorf("warning = %+v", w)
	}
	res := l.manager().Resolve(context.Background(), "codex", spec.Runtime{Kind: "codex"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "hal9001 runtimes install codex") || len(res.Command) != 0 {
		t.Errorf("Resolve = %+v, want an error naming the fix", res)
	}
}

func TestOmpUsesItsOwnACPMode(t *testing.T) {
	l := newLab(t)
	omp := l.stub("omp", `case "$1" in --version) echo omp/18.18.7;; models) echo '{"models":[{"provider":"anthropic"}]}';; esac`)
	d := detect(t, l.manager(), "omp", spec.Runtime{Kind: "omp"})
	if d.Status != StatusReady || d.Adapter.Source != SourceBuiltin || !slices.Equal(d.Command, []string{omp, "acp"}) || d.Adapter.Installable {
		t.Fatalf("status = %s adapter = %+v command = %v", d.Status, d.Adapter, d.Command)
	}
	if d.Node.OK || len(d.Warnings) != 0 {
		t.Errorf("omp must not warn about Node.js: %v", codes(d))
	}

	missing := detect(t, newLab(t).manager(), "omp", spec.Runtime{Kind: "omp"})
	if missing.Status != StatusUnavailable || !slices.Equal(codes(missing), []string{CodeCLIMissing}) || missing.Command != nil {
		t.Errorf("no omp: status = %s warnings = %v", missing.Status, codes(missing))
	}
}

func TestResolutionOrderCustomCommandThenManagedThenBuiltin(t *testing.T) {
	l := newLab(t)
	l.stub("claude", claudeSignedIn)
	node := l.node("24.0.0")
	script := l.adapter("claude", ClaudeAdapterVersion)
	custom := l.stub("my-acp", "exit 0")
	m := l.manager()
	ctx := context.Background()

	res := m.Resolve(ctx, "claude", spec.Runtime{Kind: "claude", Command: []string{"my-acp", "--flag", "{{data}}/x"}})
	if res.Source != SourceCustom || !slices.Equal(res.Command, []string{"my-acp", "--flag", l.data + "/x"}) {
		t.Errorf("custom: %+v", res)
	}
	res = m.Resolve(ctx, "claude", spec.Runtime{Kind: "claude"})
	if res.Source != SourceManaged || !slices.Equal(res.Command, []string{node, script}) || res.AdapterVersion != ClaudeAdapterVersion {
		t.Errorf("managed: %+v", res)
	}
	// A kind-less command is detected from its argv; hal9001 does not replace it.
	l.stub("claude-agent-acp", "exit 0")
	res = m.Resolve(ctx, "x", spec.Runtime{Command: []string{"claude-agent-acp"}})
	if res.Source != SourceCustom || res.Err != nil {
		t.Errorf("detected-kind command: %+v", res)
	}

	d := detect(t, m, "claude", spec.Runtime{Kind: "claude", Command: []string{custom}})
	if d.Adapter.Source != SourceCustom || !d.Adapter.Installed || d.Adapter.Installable || d.Status == StatusUnavailable {
		t.Errorf("custom detection: %+v status %s", d.Adapter, d.Status)
	}
	if d := detect(t, m, "claude", spec.Runtime{Kind: "claude", Command: []string{"does-not-exist"}}); d.Status != StatusUnavailable || d.Command != nil {
		t.Errorf("missing custom command: status %s command %v", d.Status, d.Command)
	}
}

func TestUnknownAgentIsReadyWhenItsCommandResolves(t *testing.T) {
	l := newLab(t)
	l.stub("gemini-acp", "exit 0")
	d := detect(t, l.manager(), "gemini", spec.Runtime{Command: []string{"gemini-acp"}})
	if d.Status != StatusReady || d.Auth.Status != AuthUnknown || len(d.Warnings) != 0 {
		t.Errorf("status = %s auth = %s warnings = %v", d.Status, d.Auth.Status, codes(d))
	}
}

// fakeNPM writes an npm that lays the package out like the real one. extra runs first (to block or fail).
func (l *lab) fakeNPM(extra string) {
	l.stub("npm", `[ "$1" = "--version" ] && { echo 10.9.0; exit 0; }
`+extra+`
echo "fake npm $@"
dir="$3"; pkgspec="$4"; pkg="${pkgspec%@*}"; ver="${pkgspec##*@}"
mkdir -p "$dir/node_modules/$pkg/dist"
echo "{\"name\":\"$pkg\",\"version\":\"$ver\",\"bin\":{\"${pkg##*/}\":\"dist/index.js\"}}" > "$dir/node_modules/$pkg/package.json"
echo "// js" > "$dir/node_modules/$pkg/dist/index.js"`)
}

type recorder struct {
	mu     sync.Mutex
	events []Event
	done   chan Event
}

func record(m *Manager) *recorder {
	r := &recorder{done: make(chan Event, 4)}
	m.Subscribe(func(ev Event) {
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
		if ev.Phase != PhaseInstalling {
			r.done <- ev
		}
	})
	return r
}

func (r *recorder) wait(t *testing.T) Event {
	t.Helper()
	select {
	case ev := <-r.done:
		return ev
	case <-time.After(20 * time.Second):
		t.Fatal("install did not finish")
		return Event{}
	}
}

func TestInstallInstallsThePinnedAdapterAndStreamsOutput(t *testing.T) {
	l := newLab(t)
	l.node("24.0.0")
	l.fakeNPM("")
	m := l.manager()
	rec := record(m)
	rt := spec.Runtime{Kind: "codex"}
	if err := m.Install(context.Background(), "codex", rt); err != nil {
		t.Fatal(err)
	}
	if ev := rec.wait(t); ev.Phase != PhaseDone || ev.Name != "codex" {
		t.Fatalf("final event = %+v", ev)
	}
	var lines []string
	for _, ev := range rec.events {
		if ev.Phase == PhaseInstalling && ev.Line != "" {
			lines = append(lines, ev.Line)
		}
	}
	want := "fake npm install --prefix " + filepath.Join(l.data, "adapters", "codex-acp") + " @agentclientprotocol/codex-acp@" + CodexAdapterVersion + " --no-audit --no-fund"
	if !slices.Contains(lines, want) {
		t.Errorf("npm output lines = %q, want one equal to %q", lines, want)
	}
	res := m.Resolve(context.Background(), "codex", rt)
	if res.Err != nil || res.Source != SourceManaged || res.AdapterVersion != CodexAdapterVersion {
		t.Fatalf("Resolve after install = %+v", res)
	}
	if d := detect(t, m, "codex", rt); d.Adapter.Installing || !d.Adapter.Installed {
		t.Errorf("adapter = %+v", d.Adapter)
	}
}

func TestInstallFailureIsReportedAndLeavesNothingBehind(t *testing.T) {
	l := newLab(t)
	l.node("24.0.0")
	l.stub("npm", `[ "$1" = "--version" ] && { echo 10.9.0; exit 0; }
echo "npm ERR! 404 not found" >&2; exit 1`)
	m := l.manager()
	rec := record(m)
	rt := spec.Runtime{Kind: "claude"}
	if err := m.Install(context.Background(), "claude", rt); err != nil {
		t.Fatal(err)
	}
	ev := rec.wait(t)
	if ev.Phase != PhaseFailed || !strings.Contains(ev.Error, "404 not found") {
		t.Fatalf("final event = %+v", ev)
	}
	if _, err := os.Stat(filepath.Join(l.data, "adapters", "claude-agent-acp")); err == nil {
		t.Error("a failed install left its folder behind")
	}
	d := detect(t, m, "claude", rt)
	if !slices.Contains(codes(d), CodeAdapterInstallFailed) || d.Status != StatusUnavailable {
		t.Errorf("status = %s warnings = %v", d.Status, codes(d))
	}
	// A new attempt clears the failure.
	l.fakeNPM("")
	if err := m.Install(context.Background(), "claude", rt); err != nil {
		t.Fatal(err)
	}
	if ev := rec.wait(t); ev.Phase != PhaseDone {
		t.Fatalf("retry = %+v", ev)
	}
	if d := detect(t, m, "claude", rt); slices.Contains(codes(d), CodeAdapterInstallFailed) {
		t.Errorf("stale failure after a successful install: %v", codes(d))
	}
}

func TestInstallRefusals(t *testing.T) {
	l := newLab(t)
	l.fakeNPM("")
	ctx := context.Background()
	m := l.manager()

	if err := m.Install(ctx, "omp", spec.Runtime{Kind: "omp"}); !errors.Is(err, ErrNotInstallable) {
		t.Errorf("omp: %v, want ErrNotInstallable", err)
	}
	if err := m.Install(ctx, "claude", spec.Runtime{Kind: "claude", Command: []string{"mine"}}); !errors.Is(err, ErrNotInstallable) {
		t.Errorf("custom command: %v, want ErrNotInstallable", err)
	}
	var pre *PrerequisiteError
	if err := m.Install(ctx, "claude", spec.Runtime{Kind: "claude"}); !errors.As(err, &pre) || pre.Code != CodeNodeMissing {
		t.Errorf("no node: %v", err)
	}
	l.node("18.0.0")
	m.Refresh()
	if err := m.Install(ctx, "claude", spec.Runtime{Kind: "claude"}); !errors.As(err, &pre) || pre.Code != CodeNodeOld {
		t.Errorf("old node: %v", err)
	}
}

func TestSecondInstallWhileRunningIsBusy(t *testing.T) {
	l := newLab(t)
	l.node("24.0.0")
	gate := filepath.Join(l.data, "gate")
	l.fakeNPM(`while [ ! -f "` + gate + `" ]; do sleep 0.02; done`)
	m := l.manager()
	rec := record(m)
	rt := spec.Runtime{Kind: "claude"}
	if err := m.Install(context.Background(), "claude", rt); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(context.Background(), "claude", rt); !errors.Is(err, ErrBusy) {
		t.Errorf("second install: %v, want ErrBusy", err)
	}
	if err := m.Uninstall("claude", rt); !errors.Is(err, ErrBusy) {
		t.Errorf("uninstall during install: %v, want ErrBusy", err)
	}
	if d := m.Detect(context.Background(), "claude", rt, false); !d.Adapter.Installing {
		t.Error("detection does not show the running install")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ev := rec.wait(t); ev.Phase != PhaseDone {
		t.Fatalf("final = %+v", ev)
	}
}

func TestUninstallRemovesTheAdapter(t *testing.T) {
	l := newLab(t)
	l.node("24.0.0")
	l.adapter("claude", ClaudeAdapterVersion)
	m := l.manager()
	rt := spec.Runtime{Kind: "claude"}
	if err := m.Uninstall("claude", rt); err != nil {
		t.Fatal(err)
	}
	if res := m.Resolve(context.Background(), "claude", rt); res.Err == nil {
		t.Error("the adapter still resolves after uninstall")
	}
	if err := m.Uninstall("omp", spec.Runtime{Kind: "omp"}); !errors.Is(err, ErrNotInstallable) {
		t.Errorf("omp uninstall: %v", err)
	}
}

func TestDetectionIsCachedUntilRefreshOrConfigChange(t *testing.T) {
	l := newLab(t)
	counter := filepath.Join(l.data, "count")
	l.stub("omp", `case "$1" in --version) echo omp/1.0.0;; models) echo x >> "`+counter+`"; echo '{"models":[{"provider":"anthropic"}]}';; esac`)
	m := l.manager()
	ctx := context.Background()
	runs := func() int {
		b, _ := os.ReadFile(counter)
		return strings.Count(string(b), "x")
	}
	m.Detect(ctx, "omp", spec.Runtime{Kind: "omp"}, false)
	m.Detect(ctx, "omp", spec.Runtime{Kind: "omp"}, false)
	if runs() != 1 {
		t.Errorf("auth checks after two detections = %d, want 1 (cached)", runs())
	}
	m.Detect(ctx, "omp", spec.Runtime{Kind: "omp", Command: []string{"omp", "acp"}}, false)
	if runs() != 2 {
		t.Errorf("auth checks after a config change = %d, want 2", runs())
	}
	m.Refresh()
	m.Detect(ctx, "omp", spec.Runtime{Kind: "omp", Command: []string{"omp", "acp"}}, false)
	if runs() != 3 {
		t.Errorf("auth checks after Refresh = %d, want 3", runs())
	}
}
