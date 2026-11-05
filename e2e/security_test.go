package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

const (
	secToken  = "s3cr3t-value-0123456789"
	secOther  = "other-secret-9876543210"
	secMarker = "«SECRET_TOKEN»"
)

// secStream follows GET /api/stream and collects every byte it receives until
// stop is called.
type secStream struct {
	mu     sync.Mutex
	buf    strings.Builder
	cancel context.CancelFunc
	done   chan struct{}
}

func (d *Daemon) secOpenStream() *secStream {
	d.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL()+"/api/stream", nil)
	if err != nil {
		d.t.Fatal(err)
	}
	if d.Cookie != "" {
		req.Header.Set("Cookie", d.Cookie)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		cancel()
		d.t.Fatalf("GET /api/stream: %v", err)
	}
	s := &secStream{cancel: cancel, done: make(chan struct{})}
	ready := make(chan struct{})
	go func() {
		defer close(s.done)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		var once sync.Once
		for sc.Scan() {
			s.mu.Lock()
			s.buf.WriteString(sc.Text() + "\n")
			s.mu.Unlock()
			if strings.Contains(sc.Text(), "stream.ready") {
				once.Do(func() { close(ready) })
			}
		}
	}()
	select {
	case <-ready:
	case <-s.done:
		d.t.Fatal("stream closed before stream.ready")
	}
	return s
}

func (s *secStream) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *secStream) stop() string {
	s.cancel()
	<-s.done
	return s.text()
}

// secViews GETs every read view of the API reachable from the stored runs and
// returns the response bodies by request path.
func (d *Daemon) secViews() map[string]string {
	d.t.Helper()
	views := map[string]string{}
	get := func(path string) string {
		if b, ok := views[path]; ok {
			return b
		}
		r := d.Get(path).Want(200)
		views[path] = string(r.Body)
		return views[path]
	}
	// ids returns field `key` of every item of the list at path.
	ids := func(path, key string) []string {
		var l struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(get(path)), &l); err != nil {
			d.t.Fatalf("GET %s: %v", path, err)
		}
		var out []string
		for _, it := range l.Items {
			if v, ok := it[key].(string); ok {
				out = append(out, v)
			}
		}
		return out
	}
	for _, p := range []string{"/api/health", "/api/version", "/api/status", "/api/killswitch", "/api/settings", "/api/env",
		"/api/runtimes", "/api/auth/sessions", "/api/auth/events?limit=1000", "/api/auth/whoami"} {
		get(p)
	}
	for _, id := range ids("/api/sensors", "id") {
		get("/api/sensors/" + id)
		for _, run := range ids("/api/sensors/"+id+"/runs", "id") {
			get("/api/sensor-runs/" + run)
		}
	}
	for _, id := range ids("/api/events?limit=200", "id") {
		get("/api/events/" + id)
	}
	for _, id := range ids("/api/flows", "id") {
		get("/api/flows/" + id)
		for _, h := range ids("/api/flows/"+id+"/versions", "hash") {
			get("/api/flows/" + id + "/versions/" + h)
		}
	}
	for _, q := range []string{"", "?kind=test", "?kind=replay"} {
		for _, id := range ids("/api/flow-runs"+q+limitSep(q)+"limit=200", "id") {
			var det FlowRunDetail
			if err := json.Unmarshal([]byte(get("/api/flow-runs/"+id)), &det); err != nil {
				d.t.Fatal(err)
			}
			for _, n := range det.NodeRuns {
				get("/api/node-runs/" + n.ID)
			}
		}
	}
	for _, id := range ids("/api/agent-runs?limit=200", "id") {
		get("/api/agent-runs/" + id)
		get("/api/agent-runs/" + id + "/events?limit=1000")
		get("/api/agent-runs/" + id + "/permission-requests")
		get("/api/agent-runs/" + id + "/raw")
	}
	for _, id := range ids("/api/approvals?limit=200", "id") {
		get("/api/approvals/" + id)
	}
	for _, q := range []string{"", "&archived=true"} {
		for _, id := range ids("/api/inbox?limit=200"+q, "id") {
			get("/api/inbox/" + id)
		}
	}
	for _, name := range ids("/api/prompts", "name") {
		get("/api/prompts/" + name)
	}
	// Blobs referenced from any of the views above.
	for _, body := range sortedValues(views) {
		var v any
		if json.Unmarshal([]byte(body), &v) == nil {
			secWalkBlobs(v, func(h string) { get("/api/blobs/" + h) })
		}
	}
	return views
}

func limitSep(q string) string {
	if q == "" {
		return "?"
	}
	return "&"
}

func sortedValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

// secWalkBlobs calls f with every `blob` hash in a decoded view.
func secWalkBlobs(v any, f func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "blob" && s != "" {
				f(s)
				continue
			}
			secWalkBlobs(val, f)
		}
	case []any:
		for _, val := range x {
			secWalkBlobs(val, f)
		}
	}
}

// secLeaks lists the views whose body contains any of the raw secrets.
func secLeaks(views map[string]string, secrets ...string) []string {
	var out []string
	for path, body := range views {
		for _, s := range secrets {
			if strings.Contains(body, s) {
				out = append(out, path+" contains "+s)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Scenario 17: values from the env file are replaced with «KEY» in stored
// stdout, stderr and argv (REQUIREMENTS §9), and no API view returns them
// (api/openapi.yaml: env values are write-only; raw JSON-RPC is redacted).
func TestScenario17SecretsNeverInTraceViews(t *testing.T) {
	d := newDaemon(t)
	d.Env("SECRET_TOKEN=" + secToken + "\n")
	d.IdleSensor("src")
	d.IdleSensor("asrc")
	d.Sensor("leaky", `id: leaky
interval: 1h
lang: bash
script: |
  echo "sensor stderr $SECRET_TOKEN" >&2
  printf '{"id":"s1","token":"%s"}\n' "$SECRET_TOKEN"
key: "{{.id}}"
first_run: fire
timeout: 10s
`)
	d.Prompt("p", "none", "Look at {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		say("the token is " + secToken),
		{Op: fakeagent.OpThought, Text: "thinking about " + secToken},
		{Op: fakeagent.OpStderr, Text: "agent stderr " + secToken},
		execPermission("x1", "echo "+secToken, nil, say("rejected "+secToken)),
		say("final " + secToken),
	}}}})
	d.Flow("sec", `id: sec
enabled: true
subscribe: [src, leaky]
nodes:
  - id: emit
    type: command
    lang: bash
    script: |
      echo "using $SECRET_TOKEN other=${OTHER_TOKEN:-none}" >&2
      printf '{"id":"%s","token":"%s"}\n' "${HAL_EVENT_ID}" "$SECRET_TOKEN"
  - id: relay
    type: command
    lang: bash
    script: |
      cat
      echo "relay saw $SECRET_TOKEN" >&2
  - id: viaexec
    type: command
    lang: exec
    command: [sh, -c, 'echo "exec saw $SECRET_TOKEN" >&2; printf "{\"token\":\"%s\"}" "$SECRET_TOKEN"']
  - {id: note, type: inbox, title: "token {{.input.token}}", summary_md: "carried {{.input.token}}"}
edges:
  - {from: emit, to: relay}
  - {from: relay, to: viaexec}
  - {from: viaexec, to: note}
`)
	d.Flow("agentsec", `id: agentsec
enabled: true
subscribe: [asrc]
nodes:
  - {id: work, type: agent, runtime: fake, mode: readonly, prompt: p}
  - {id: done, type: inbox, title: "agent {{.input.input.id}}", summary_md: "{{.input.agent.final_message}}"}
edges:
  - {from: work, to: done}
`)
	d.Start()
	stream := d.secOpenStream()

	// Command nodes: the secret is redacted in stdout and stderr, in the
	// downstream node's stdin and in the inbox item built from the output.
	run := d.RunOf("src", map[string]any{"id": "1"})
	emit, relay, viaexec := d.Node(run, "emit"), d.Node(run, "relay"), d.Node(run, "viaexec")
	for name, blob := range map[string]TextBlob{
		"emit stdout": emit.Stdout, "emit stderr": emit.Stderr, "relay stdin": relay.Stdin, "relay stdout": relay.Stdout,
		"relay stderr": relay.Stderr, "viaexec stderr": viaexec.Stderr, "viaexec stdout": viaexec.Stdout,
	} {
		if !strings.Contains(blob.Text, secMarker) {
			t.Errorf("%s lacks %s: %q", name, secMarker, blob.Text)
		}
	}
	if !strings.Contains(emit.Stderr.Text, "other=none") {
		t.Errorf("emit stderr: %q", emit.Stderr.Text)
	}
	if len(viaexec.Argv) != 3 || viaexec.Argv[0] != "sh" || strings.Contains(strings.Join(viaexec.Argv, " "), secToken) {
		t.Errorf("exec argv: %q", viaexec.Argv)
	}
	if !secContains(emit.EnvKeys, "SECRET_TOKEN") {
		t.Errorf("env keys %v lack SECRET_TOKEN", emit.EnvKeys)
	}
	if got := d.InboxByTitle()["token "+secMarker]; got.ID == "" || !strings.Contains(got.SummaryMD, secMarker) {
		t.Errorf("inbox: %+v", d.InboxByTitle())
	}

	// The sensor's own stdout and stderr, and the events built from them.
	sr := d.Poll("leaky")
	if sr.Status != "succeeded" || !strings.Contains(sr.Stderr.Text, secMarker) || !strings.Contains(sr.Stdout.Text, secMarker) {
		t.Errorf("sensor run: %+v", sr)
	}
	if evs := d.Events("leaky"); len(evs) != 1 || !strings.Contains(string(evs[0].Payload), secMarker) {
		t.Errorf("leaky events: %+v", evs)
	}

	// Agent run: transcript, permission request, final message, raw traffic.
	arun := d.WaitRun(d.InjectRun("asrc", map[string]any{"id": "7"}), "succeeded")
	work := d.Node(arun, "work")
	agents := d.AgentRuns(work.ID)
	if len(agents) != 1 {
		t.Fatalf("agent runs: %+v", agents)
	}
	ar := agents[0]
	if !strings.Contains(ar.FinalMessage, secMarker) {
		t.Errorf("final message: %q", ar.FinalMessage)
	}
	if text := d.Transcript(ar.ID); !strings.Contains(text, secMarker) {
		t.Errorf("transcript: %q", text)
	}
	perms := d.Permissions(ar.ID)
	if len(perms) != 1 || !strings.Contains(perms[0].Title, secMarker) || !strings.Contains(string(perms[0].RawInput), secMarker) {
		t.Errorf("permission requests: %+v", perms)
	}
	raw := string(d.Get("/api/agent-runs/" + ar.ID + "/raw").Want(200).Body)
	if !strings.Contains(raw, secMarker) {
		t.Errorf("raw JSON-RPC traffic shows no marker: %.300s", raw)
	}

	// Write-only env: PUT then every view. The new key is redacted from the
	// next run, and its value is returned nowhere.
	d.Put("/api/env/OTHER_TOKEN", map[string]string{"value": secOther}).Want(204)
	var keys struct {
		Items []struct {
			Key string `json:"key"`
		} `json:"items"`
	}
	d.Get("/api/env").Want(200).JSON(&keys)
	if len(keys.Items) != 2 || keys.Items[0].Key != "OTHER_TOKEN" || keys.Items[1].Key != "SECRET_TOKEN" {
		t.Errorf("env keys: %+v", keys.Items)
	}
	attempt := 0
	d.Eventually("a run to redact the API-set OTHER_TOKEN", func() bool {
		attempt++
		det := d.RunOf("src", map[string]any{"id": "again-" + strconv.Itoa(attempt)})
		return strings.Contains(d.Node(det, "emit").Stderr.Text, "other=«OTHER_TOKEN»")
	})

	// Replay and a node test add flow runs of kind replay and test.
	var replayed struct {
		FlowRun FlowRun `json:"flow_run"`
	}
	d.Post("/api/events/"+run.FlowRun.EventID+"/replay", map[string]any{"flow": "sec"}).Want(200, 201, 202).JSON(&replayed)
	if replayed.FlowRun.ID != "" {
		d.WaitRun(replayed.FlowRun.ID, "succeeded")
	}
	var tested struct {
		NodeRun NodeRun `json:"node_run"`
	}
	d.Post("/api/flows/sec/test-node", map[string]any{
		"node_id": "emit", "input": map[string]any{"source": "event", "event_id": run.FlowRun.EventID},
	}).Want(200).JSON(&tested)
	if !strings.Contains(tested.NodeRun.Stderr.Text, secMarker) || strings.Contains(tested.NodeRun.Stderr.Text, secToken) {
		t.Errorf("test-node stderr: %q", tested.NodeRun.Stderr.Text)
	}

	views := d.secViews()
	if len(views) < 40 {
		t.Fatalf("only %d views fetched; the crawl missed resources", len(views))
	}
	if leaks := secLeaks(views, secToken, secOther); len(leaks) > 0 {
		t.Errorf("secret values appear in API views:\n%s", strings.Join(leaks, "\n"))
	}
	if body := stream.stop(); strings.Contains(body, secToken) || strings.Contains(body, secOther) {
		t.Error("a secret value appears on /api/stream")
	} else if !strings.Contains(body, "agent_event") && !strings.Contains(body, "node_run") {
		t.Errorf("the stream carried no run updates:\n%.500s", body)
	}
	// The env file itself keeps the values (it is the input), nothing else on disk is checked here.
	if !strings.Contains(d.Read("env"), "OTHER_TOKEN=") {
		t.Errorf("env file: %q", d.Read("env"))
	}
	if !strings.Contains(string(d.Get("/api/settings").Body), "runtimes") {
		t.Error("settings view lacks runtimes")
	}
}

func secContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Scenario 18 (REQUIREMENTS §10.3): a cross-origin request, a non-JSON
// mutating request and a request with a foreign Host are rejected before
// anything happens; a same-origin request works. Rejected requests change
// nothing.
func TestScenario18NetworkBaseline(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: note, type: inbox, title: \"event {{.input.id}}\"}\n")
	d.Start()
	port := strconv.Itoa(d.Port())
	item := map[string]any{"id": "x"}
	inject := "/api/events?sensor_id=src"

	problem := func(r Resp, status int, typ string) {
		t.Helper()
		if r.Status != status || r.Problem() != typ {
			t.Errorf("status %d problem %q, want %d %q: %s", r.Status, r.Problem(), status, typ, r.Body)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("Content-Type %q, want application/problem+json", ct)
		}
		if v := r.Header.Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("rejection carries CORS header Access-Control-Allow-Origin: %s", v)
		}
	}

	// Cross-origin: rejected for mutating and read requests alike.
	for _, origin := range []string{
		"https://evil.example", "http://evil.example", "null", "http://127.0.0.1:1",
		"https://127.0.0.1:" + port, "http://127.0.0.1:" + port + "/path", "http://127.0.0.1." + "evil.example:" + port,
	} {
		problem(d.Post(inject, item, withHeader("Origin", origin)), 403, "origin-not-allowed")
		problem(d.Get("/api/status", withHeader("Origin", origin)), 403, "origin-not-allowed")
	}
	// A preflight is never granted.
	pre := d.Do("OPTIONS", inject, nil, withHeader("Origin", "https://evil.example"),
		withHeader("Access-Control-Request-Method", "POST"), withoutContract())
	if pre.Status < 400 || pre.Header.Get("Access-Control-Allow-Origin") != "" || pre.Header.Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("preflight: %s headers %v", pre, pre.Header)
	}

	// Content type: only application/json may mutate, whatever the method.
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "text/plain; charset=utf-8", "application/jsonx", ""} {
		problem(d.Post(inject, nil, withContentType(ct), withRawBody(`{"id":"x"}`)), 415, "unsupported-media-type")
	}
	problem(d.Put("/api/env/NEW_KEY", nil, withContentType("text/plain"), withRawBody(`{"value":"v"}`)), 415, "unsupported-media-type")
	problem(d.Patch("/api/settings", nil, withContentType("text/plain"), withRawBody(`{}`)), 415, "unsupported-media-type")
	problem(d.Delete("/api/flows/f", withContentType("text/plain")), 415, "unsupported-media-type")
	problem(d.Post("/api/killswitch/pause", nil, withContentType("text/plain")), 415, "unsupported-media-type")
	problem(d.Post("/api/sensors", nil, withContentType("text/plain"), withRawBody("id: evil\n")), 415, "unsupported-media-type")

	// DNS rebinding: a foreign Host is rejected for every kind of request,
	// including the UI shell, and before the Origin check.
	for _, host := range []string{"evil.example", "evil.example:" + port, "127.0.0.1", "127.0.0.1:1", "localhost", "127.0.0.1.evil.example:" + port, "[::1]:" + port} {
		problem(d.Get("/api/status", withHost(host)), 403, "host-not-allowed")
		problem(d.Get("/api/health", withHost(host)), 403, "host-not-allowed")
		problem(d.Post(inject, item, withHost(host)), 403, "host-not-allowed")
		problem(d.Get("/", withHost(host), withoutContract()), 403, "host-not-allowed")
	}
	problem(d.Post(inject, item, withHost("evil.example"), withHeader("Origin", "http://evil.example")), 403, "host-not-allowed")

	// None of the rejected requests had an effect.
	if n := len(d.Events("src")); n != 0 {
		t.Errorf("rejected requests created %d events", n)
	}
	var runs struct {
		Items []FlowRun `json:"items"`
	}
	d.Get("/api/flow-runs").Want(200).JSON(&runs)
	if len(runs.Items) != 0 || len(d.Inbox()) != 0 {
		t.Errorf("rejected requests started flow runs: %+v", runs.Items)
	}
	if d.GetStatus().KillSwitch.Paused {
		t.Error("a rejected request paused the daemon")
	}
	var keys struct {
		Items []struct{ Key string } `json:"items"`
	}
	d.Get("/api/env").Want(200).JSON(&keys)
	if len(keys.Items) != 0 {
		t.Errorf("rejected requests changed env: %+v", keys.Items)
	}
	d.Get("/api/sensors/evil").Want(404)
	if got := d.GetFlow("f"); got.ID != "f" {
		t.Errorf("flow f was deleted: %+v", got)
	}

	// Same-origin requests, and the other loopback spellings, are accepted.
	for _, origin := range []string{"http://127.0.0.1:" + port, "http://localhost:" + port} {
		r := d.Post(inject, map[string]any{"id": origin}, withHeader("Origin", origin)).Want(200, 201)
		if v := r.Header.Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("same-origin response carries CORS header %q", v)
		}
	}
	d.Post(inject, map[string]any{"id": "ct"}, withContentType("application/json; charset=utf-8")).Want(200, 201)
	d.Get("/api/status", withHost("localhost:"+port)).Want(200)
	d.Get("/api/status", withHost("LOCALHOST:"+port)).Want(200)
	d.Get("/api/status", withHeader("Origin", "http://localhost:"+port)).Want(200)
	if n := len(d.Events("src")); n != 3 {
		t.Errorf("accepted requests created %d events, want 3", n)
	}
}
