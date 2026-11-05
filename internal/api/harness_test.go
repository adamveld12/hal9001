package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hal9001/content"
	"hal9001/internal/apispec"
	"hal9001/internal/auth"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// engineDouble stands in for core.Engine. Tests script the call they exercise;
// an unscripted call fails with an error so it surfaces as a 500.
type engineDouble struct {
	inject       func(ctx context.Context, sensorID string, item json.RawMessage) (core.InjectResult, error)
	poll         func(ctx context.Context, sensorID string) (*store.SensorRun, error)
	pauseSensor  func(ctx context.Context, sensorID, principal string) (store.SensorState, error)
	resumeSensor func(ctx context.Context, sensorID string) (store.SensorState, error)
	replay       func(ctx context.Context, eventID, flowID string, dryRun bool) (*store.FlowRun, error)
	testNode     func(ctx context.Context, req core.TestNodeRequest) (*store.FlowRun, *store.NodeRun, error)
	decide       func(ctx context.Context, id string, d core.ApprovalDecision) (*store.Approval, error)
	pause        func(ctx context.Context, killRunning bool, principal string) (store.KillSwitch, error)
	resume       func(ctx context.Context, principal string) (store.KillSwitch, error)
	probe        func(ctx context.Context, name string) (core.ProbeResult, error)
}

var errUnscripted = errors.New("engineDouble: call not scripted")

func (d *engineDouble) InjectEvent(ctx context.Context, sensorID string, item json.RawMessage) (core.InjectResult, error) {
	if d.inject == nil {
		return core.InjectResult{}, errUnscripted
	}
	return d.inject(ctx, sensorID, item)
}

func (d *engineDouble) PollSensor(ctx context.Context, sensorID string) (*store.SensorRun, error) {
	if d.poll == nil {
		return nil, errUnscripted
	}
	return d.poll(ctx, sensorID)
}

func (d *engineDouble) PauseSensor(ctx context.Context, sensorID, principal string) (store.SensorState, error) {
	if d.pauseSensor == nil {
		return store.SensorState{}, errUnscripted
	}
	return d.pauseSensor(ctx, sensorID, principal)
}

func (d *engineDouble) ResumeSensor(ctx context.Context, sensorID string) (store.SensorState, error) {
	if d.resumeSensor == nil {
		return store.SensorState{}, errUnscripted
	}
	return d.resumeSensor(ctx, sensorID)
}

func (d *engineDouble) Replay(ctx context.Context, eventID, flowID string, dryRun bool) (*store.FlowRun, error) {
	if d.replay == nil {
		return nil, errUnscripted
	}
	return d.replay(ctx, eventID, flowID, dryRun)
}

func (d *engineDouble) TestNode(ctx context.Context, req core.TestNodeRequest) (*store.FlowRun, *store.NodeRun, error) {
	if d.testNode == nil {
		return nil, nil, errUnscripted
	}
	return d.testNode(ctx, req)
}

func (d *engineDouble) DecideApproval(ctx context.Context, id string, dec core.ApprovalDecision) (*store.Approval, error) {
	if d.decide == nil {
		return nil, errUnscripted
	}
	return d.decide(ctx, id, dec)
}

func (d *engineDouble) Pause(ctx context.Context, killRunning bool, principal string) (store.KillSwitch, error) {
	if d.pause == nil {
		return store.KillSwitch{}, errUnscripted
	}
	return d.pause(ctx, killRunning, principal)
}

func (d *engineDouble) Resume(ctx context.Context, principal string) (store.KillSwitch, error) {
	if d.resume == nil {
		return store.KillSwitch{}, errUnscripted
	}
	return d.resume(ctx, principal)
}

func (d *engineDouble) ProbeRuntime(ctx context.Context, name string) (core.ProbeResult, error) {
	if d.probe == nil {
		return core.ProbeResult{}, errUnscripted
	}
	return d.probe(ctx, name)
}

func (d *engineDouble) Apply(*spec.Workspace) {}

// env is a real store, a real seeded home directory, a real auth manager and
// the real router.
type env struct {
	t       *testing.T
	home    string
	st      *store.Store
	eng     *engineDouble
	mgr     *auth.Manager
	srv     *Server
	ws      atomic.Pointer[spec.Workspace]
	reloads atomic.Int32
}

type envOptions struct {
	auth *auth.Config
	ui   http.Handler
}

func newEnv(t *testing.T, mods ...func(*envOptions)) *env {
	t.Helper()
	var eo envOptions
	for _, m := range mods {
		m(&eo)
	}
	e := &env{t: t, home: t.TempDir(), eng: &engineDouble{}}
	if _, err := spec.Init(e.home, content.FS); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(e.home, spec.FileDB))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	cfg := auth.DefaultConfig()
	if eo.auth != nil {
		cfg = *eo.auth
	}
	e.mgr, err = auth.New(auth.Options{Config: cfg, Store: auth.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	e.ws.Store(spec.Load(e.home))
	e.srv = New(Options{
		Store: st, Home: e.home, Engine: e.eng, Auth: e.mgr,
		Workspace: func() *spec.Workspace { return e.ws.Load() },
		Reload: func() {
			e.reloads.Add(1)
			e.ws.Store(spec.Load(e.home))
		},
		Version: "test", UI: eo.ui,
	}).(*Server)
	t.Cleanup(e.srv.Close)
	return e
}

// resp is a recorded response.
type resp struct {
	t      *testing.T
	Status int
	Header http.Header
	Body   []byte
}

func (r *resp) decode(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("response is not the expected JSON (%v): %s", err, r.Body)
	}
}

func (r *resp) json() map[string]any {
	r.t.Helper()
	var m map[string]any
	r.decode(&m)
	return m
}

// problem returns the problem type URI suffix, e.g. "not-found".
func (r *resp) problem() string {
	r.t.Helper()
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/problem+json") {
		r.t.Fatalf("expected a problem+json response, got %d %q: %s", r.Status, r.Header.Get("Content-Type"), r.Body)
	}
	typ, _ := r.json()["type"].(string)
	return strings.TrimPrefix(typ, "urn:hal9001:problem:")
}

// want fails unless the status is code.
func (r *resp) want(code int) *resp {
	r.t.Helper()
	if r.Status != code {
		r.t.Fatalf("status = %d, want %d: %s", r.Status, code, r.Body)
	}
	return r
}

// request builds a loopback request for the contract's server URL.
func request(method, path string, body []byte) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:9001"+path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// serve runs req through h and checks the response against the contract.
func serve(t *testing.T, h http.Handler, req *http.Request) *resp {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	apispec.ValidateResponse(t, req, res)
	body := rec.Body.Bytes()
	return &resp{t: t, Status: res.StatusCode, Header: res.Header, Body: body}
}

// do sends a JSON request (body may be nil, a string/[]byte of JSON, or any
// value to marshal) and validates the response against the contract.
func (e *env) do(method, path string, body any) *resp {
	e.t.Helper()
	return serve(e.t, e.srv, request(method, path, encodeBody(e.t, body)))
}

func encodeBody(t *testing.T, body any) []byte {
	t.Helper()
	switch b := body.(type) {
	case nil:
		return nil
	case string:
		return []byte(b)
	case []byte:
		return b
	}
	out, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) get(path string) *resp { e.t.Helper(); return e.do(http.MethodGet, path, nil) }

// async starts a GET in the background and returns a function that waits for
// it and checks the response against the contract on the test goroutine.
func (e *env) async(path string) func() *resp {
	req := request(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.ServeHTTP(rec, req)
	}()
	return func() *resp {
		<-done
		res := rec.Result()
		apispec.ValidateResponse(e.t, req, res)
		return &resp{t: e.t, Status: res.StatusCode, Header: res.Header, Body: rec.Body.Bytes()}
	}
}

// ids returns the ids of the items of a list response.
func (r *resp) ids() []string {
	r.t.Helper()
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	r.decode(&page)
	out := make([]string, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, it.ID)
	}
	return out
}

// walk follows next_cursor through a list endpoint and returns every id in order.
func (e *env) walk(path string, limit int) (ids []string, pages int) {
	e.t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	cursor := ""
	for {
		url := path + sep + "limit=" + strconv.Itoa(limit)
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		r := e.get(url).want(200)
		ids = append(ids, r.ids()...)
		pages++
		var page struct {
			NextCursor *string `json:"next_cursor"`
		}
		r.decode(&page)
		if page.NextCursor == nil {
			return ids, pages
		}
		cursor = *page.NextCursor
		if pages > 100 {
			e.t.Fatal("pagination does not terminate")
		}
	}
}

// ---- store fixtures ----

const demoFlowYAML = `id: demo
enabled: true
subscribe: [demo-ticker]
nodes:
  - id: filter
    type: command
    lang: jq
    script: '.'
  - id: review
    type: agent
    runtime: claude
    mode: approve
    prompt: pr-review
  - id: note
    type: inbox
    title: A note
edges:
  - {from: filter, to: review}
  - {from: review, to: note}
`

func (e *env) event(sensor, key, payload string) *store.Event {
	e.t.Helper()
	ev := &store.Event{SensorID: sensor, DedupKey: key, Payload: store.Inline(payload)}
	if ok, err := e.st.IngestEvent(context.Background(), ev); err != nil || !ok {
		e.t.Fatalf("ingest %s/%s: new=%v err=%v", sensor, key, ok, err)
	}
	time.Sleep(2 * time.Millisecond) // distinct timestamps for since/until
	return ev
}

func (e *env) flowRun(flowID, eventID string, kind store.FlowRunKind, status store.FlowRunStatus) *store.FlowRun {
	e.t.Helper()
	fr := &store.FlowRun{
		FlowID: flowID, FlowHash: spec.Hash([]byte(demoFlowYAML)), FlowYAML: demoFlowYAML,
		EventID: eventID, Kind: kind, Status: status,
	}
	if err := e.st.CreateFlowRun(context.Background(), fr); err != nil {
		e.t.Fatal(err)
	}
	return fr
}

func (e *env) nodeRun(frID, nodeID string, status store.NodeRunStatus, mod func(*store.NodeRun)) *store.NodeRun {
	e.t.Helper()
	nr := &store.NodeRun{FlowRunID: frID, NodeID: nodeID, NodeType: store.NodeCommand, Status: status, Lang: "jq", Script: "."}
	if mod != nil {
		mod(nr)
	}
	if err := e.st.CreateNodeRun(context.Background(), nr); err != nil {
		e.t.Fatal(err)
	}
	return nr
}

func (e *env) agentRun(nodeRunID string, status store.AgentStatus) *store.AgentRun {
	e.t.Helper()
	ar := &store.AgentRun{
		NodeRunID: nodeRunID, Runtime: "claude", Mode: store.ModeApprove, ApproveStyle: store.StyleProposal,
		Policy: store.PolicyReadonly, Phase: store.PhasePropose, Status: status, Prompt: store.Inline("review it"),
	}
	if err := e.st.CreateAgentRun(context.Background(), ar); err != nil {
		e.t.Fatal(err)
	}
	return ar
}

func ctx() context.Context { return context.Background() }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
