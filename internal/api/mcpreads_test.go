package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"hal9001/internal/store"
	"hal9001/internal/tools"
)

type noHost struct{}

func (noHost) Approve(context.Context, tools.ApprovalRequest) (tools.Decision, error) {
	return tools.Decision{}, nil
}
func (noHost) Ask(context.Context, tools.AskRequest) (tools.AskResult, error) {
	return tools.AskResult{}, nil
}
func (noHost) Record(context.Context, tools.CallRecord) (string, error) { return "", nil }
func (noHost) Finish(context.Context, string, string)                   {}

// openRunSession opens a session of kind with the read tools for assist sessions only, like the assist manager does.
func openRunSession(t *testing.T, kind string) (*tools.Hub, *tools.Session) {
	t.Helper()
	hub, err := tools.NewHub(tools.Options{Host: noHost{}})
	if err != nil {
		t.Fatal(err)
	}
	hub.AddProvider(func(_ context.Context, rs tools.RunSpec) (tools.Extension, error) {
		if rs.Kind != tools.KindAssist {
			return tools.Extension{}, nil
		}
		return tools.Extension{Tools: tools.ReadTools()}, nil
	})
	s, err := hub.Open(context.Background(), tools.RunSpec{
		Kind: kind, RunID: "run1", ApprovalTimeout: time.Minute,
		Info: func() tools.RunInfo { return tools.RunInfo{AgentRunID: "run1"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return hub, s
}

func callMcp(t *testing.T, e *env, token, tool string, args any) (text string, isError bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	req := request(http.MethodPost, "/api/mcp/call", body)
	req.Header.Set("Authorization", "Bearer "+token)
	r := serve(t, e.srv, req).want(200)
	var out struct {
		Text    string `json:"text"`
		IsError bool   `json:"is_error"`
	}
	r.decode(&out)
	return out.Text, out.IsError
}

// The read tools of an assist session reach the REST reads with the run token alone: no cookie, even where the stack
// would refuse the agent's own request. They reach nothing else.
func TestAssistReadToolsReadTheAPIWithTheRunToken(t *testing.T) {
	cfg := lockedConfig()
	hub, sess := openRunSession(t, tools.KindAssist)
	e := newEnv(t, func(o *envOptions) { o.auth = &cfg; o.mcp = hub })

	ev := e.event("gh", "k1", `{"id":1}`)
	fr := e.flowRun("demo", ev.ID, store.KindLive, store.FlowRunFailed)
	nr := e.nodeRun(fr.ID, "n1", store.NodeFailed, func(n *store.NodeRun) { n.Error = "jq: boom" })
	ar := e.agentRun(nr.ID, store.AgentFailed)

	// The same read straight against the router is refused, which is what the dispatcher must not depend on.
	e.get("/api/flow-runs").want(401)

	text, bad := callMcp(t, e, sess.Token(), "flow_runs_list", map[string]any{"flow_id": "demo", "status": "failed"})
	if bad || !strings.Contains(text, fr.ID) {
		t.Fatalf("flow_runs_list: error=%v %s", bad, text)
	}
	text, bad = callMcp(t, e, sess.Token(), "flow_run_get", map[string]any{"id": fr.ID})
	if bad || !strings.Contains(text, "jq: boom") {
		t.Fatalf("flow_run_get should show the node error: error=%v %s", bad, text)
	}
	text, bad = callMcp(t, e, sess.Token(), "agent_run_get", map[string]any{"id": ar.ID})
	if bad || !strings.Contains(text, "review it") {
		t.Fatalf("agent_run_get should show the rendered prompt: error=%v %s", bad, text)
	}
	text, bad = callMcp(t, e, sess.Token(), "agent_runs_list", map[string]any{"node_run_id": nr.ID})
	if bad || !strings.Contains(text, ar.ID) {
		t.Fatalf("agent_runs_list: error=%v %s", bad, text)
	}
	if text, bad = callMcp(t, e, sess.Token(), "flow_run_get", map[string]any{"id": "01JNOPENOPENOPENOPENOPENOP"}); !bad || !strings.Contains(text, "404") {
		t.Errorf("a missing run must be a tool error with the status: error=%v %s", bad, text)
	}
	// Reads that are not part of the tool set are not reachable by name, whatever the id says.
	if _, bad = callMcp(t, e, sess.Token(), "assist_sessions_list", map[string]any{}); !bad {
		t.Error("an unknown tool must be an error")
	}
}

func TestReadDispatcherServesOnlyAPIGETs(t *testing.T) {
	e := newEnv(t)
	call := e.srv.readDispatcher(request(http.MethodPost, "/api/mcp/call", nil))
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/flows/demo/run"},
		{http.MethodPut, "/api/flows/demo"},
		{http.MethodDelete, "/api/flows/demo"},
		{http.MethodPatch, "/api/settings"},
	} {
		if status, _, err := call(context.Background(), c.method, c.path, nil, []byte(`{}`)); err == nil {
			t.Errorf("%s %s reached the API (status %d)", c.method, c.path, status)
		}
	}
	// Operations of other services, the event stream and the UI are not in the read router at all.
	for _, path := range []string{"/api/stream", "/api/auth/sessions", "/api/mcp/call", "/"} {
		if status, _, err := call(context.Background(), http.MethodGet, path, url.Values{}, nil); err != nil || status < 400 {
			t.Errorf("GET %s: status %d err %v, want a refusal", path, status, err)
		}
	}
	if status, body, err := call(context.Background(), http.MethodGet, "/api/status", nil, nil); err != nil || status != 200 {
		t.Errorf("GET /api/status: %d %s %v", status, body, err)
	}
}

func TestFlowAgentSessionsGetNoReadAPI(t *testing.T) {
	hub, sess := openRunSession(t, tools.KindFlow)
	e := newEnv(t, func(o *envOptions) { o.mcp = hub })
	// A flow agent has no read tools; even asking for one by name fails.
	if text, bad := callMcp(t, e, sess.Token(), "flow_runs_list", map[string]any{}); !bad || !strings.Contains(text, "unknown tool") {
		t.Errorf("flow_runs_list for a flow agent: error=%v %s", bad, text)
	}
}
