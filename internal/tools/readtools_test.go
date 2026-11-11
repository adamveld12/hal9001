package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// openAssistSession opens an assist session that has ReadTools, like the assist manager's provider does.
func openAssistSession(t *testing.T, host *fakeHost) *Session {
	t.Helper()
	h, _ := newTestHub(t, host, t.TempDir())
	h.AddProvider(func(_ context.Context, rs RunSpec) (Extension, error) {
		if rs.Kind != KindAssist {
			return Extension{}, nil
		}
		return Extension{Tools: ReadTools()}, nil
	})
	s, err := h.Open(context.Background(), RunSpec{
		Kind: KindAssist, RunID: "sess1", ApprovalTimeout: time.Minute,
		Info: func() RunInfo { return RunInfo{AgentRunID: "run1", AssistSessionID: "sess1"} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestReadToolsAreListedAndNeverNeedApproval(t *testing.T) {
	host := &fakeHost{}
	s := openAssistSession(t, host)
	listed := map[string]bool{}
	for _, ti := range s.Tools() {
		listed[ti.Name] = true
	}
	for _, name := range []string{
		"status", "queue_list", "flows_list", "flow_get", "flow_runs_list", "flow_run_get", "node_run_get", "node_run_sandbox_log",
		"agent_runs_list", "agent_run_get", "agent_run_events", "agent_run_permission_requests", "agent_run_sandbox_log",
		"sensors_list", "sensor_get", "sensor_runs_list", "sensor_run_get", "events_list", "event_get",
		"inbox_list", "inbox_get", "approvals_list", "approval_get",
		"tools_list", "tool_get", "prompts_list", "prompt_get", "runtimes_list", "sandboxes_list", "sandbox_get", "settings_get",
	} {
		if !listed[name] {
			t.Errorf("tool %s is not offered", name)
		}
	}
	// Nothing that writes or runs: the operator's write tools and the CLI's mutating commands have no counterpart.
	for _, name := range []string{"flow_run", "sensor_poll", "flow_create", "sensor_create", "request_approval", "send_note", "env_set", "approve", "deny"} {
		if listed[name] {
			t.Errorf("tool %s must not be in a read-only set", name)
		}
	}
	for _, t0 := range ReadTools() {
		if t0.Approval != nil {
			t.Errorf("%s has an Approval rule", t0.Name)
		}
		if t0.Policy != nil {
			if v, _ := t0.Policy(json.RawMessage(`{"id":"x"}`)); v == VerdictApprove {
				t.Errorf("%s asks for approval", t0.Name)
			}
		}
	}
	if len(host.approvals) != 0 {
		t.Errorf("approvals asked: %+v", host.approvals)
	}
}

type readReq struct {
	method, path string
	query        url.Values
	body         string
}

func readAPI(sent *[]readReq, body string) APIFunc {
	return func(_ context.Context, method, path string, q url.Values, b []byte) (int, []byte, error) {
		*sent = append(*sent, readReq{method, path, q, string(b)})
		return http.StatusOK, []byte(body), nil
	}
}

func TestReadToolsMapToGETsWithoutApproval(t *testing.T) {
	host := &fakeHost{}
	s := openAssistSession(t, host)
	var sent []readReq
	ctx := WithAPI(context.Background(), readAPI(&sent, `{"ok":true}`))

	cases := []struct {
		tool, args, path string
		query            map[string]string
	}{
		{"flow_runs_list", `{"flow_id":"triage","status":"failed","limit":5,"since":"2026-11-01T00:00:00Z"}`, "/api/flow-runs",
			map[string]string{"flow_id": "triage", "status": "failed", "limit": "5", "since": "2026-11-01T00:00:00Z"}},
		{"flow_run_get", `{"id":"01JRUN"}`, "/api/flow-runs/01JRUN", nil},
		{"node_run_get", `{"id":"01JNODE"}`, "/api/node-runs/01JNODE", nil},
		{"agent_runs_list", `{"flow_run_id":"01JRUN","status":"failed"}`, "/api/agent-runs", map[string]string{"flow_run_id": "01JRUN", "status": "failed", "limit": "20"}},
		{"agent_run_get", `{"id":"01JAGENT"}`, "/api/agent-runs/01JAGENT", nil},
		{"agent_run_events", `{"id":"01JAGENT","after_seq":10,"kind":"tool_call"}`, "/api/agent-runs/01JAGENT/events", map[string]string{"after_seq": "10", "kind": "tool_call", "limit": "50"}},
		{"agent_run_sandbox_log", `{"id":"01JAGENT","tail":true}`, "/api/agent-runs/01JAGENT/sandbox-log", map[string]string{"tail": "true", "limit": "100"}},
		{"node_run_sandbox_log", `{"id":"01JNODE","after_id":4,"limit":10}`, "/api/node-runs/01JNODE/sandbox-log", map[string]string{"after_id": "4", "limit": "10"}},
		{"sensor_runs_list", `{"id":"gh","status":"failed"}`, "/api/sensors/gh/runs", map[string]string{"status": "failed", "limit": "20"}},
		{"sensor_run_get", `{"id":"01JSR"}`, "/api/sensor-runs/01JSR", nil},
		{"events_list", `{"sensor_id":"gh","decision":"error","q":"deploy"}`, "/api/events", map[string]string{"sensor_id": "gh", "decision": "error", "q": "deploy", "limit": "20"}},
		{"event_get", `{"id":"01JEV"}`, "/api/events/01JEV", nil},
		{"approval_get", `{"id":"01JAP"}`, "/api/approvals/01JAP", nil},
		{"inbox_list", `{"flow_id":"triage","read":false}`, "/api/inbox", map[string]string{"flow_id": "triage", "read": "false", "limit": "20"}},
		{"sandbox_get", `{"id":"docker"}`, "/api/sandboxes/docker", nil},
		{"settings_get", `{}`, "/api/settings", nil},
		{"queue_list", `{}`, "/api/queue", nil},
	}
	for _, c := range cases {
		sent = nil
		res, err := s.Call(ctx, c.tool, json.RawMessage(c.args))
		if err != nil || res.Text != `{"ok":true}` {
			t.Errorf("%s: %q, %v", c.tool, res.Text, err)
			continue
		}
		if len(sent) != 1 || sent[0].method != http.MethodGet || sent[0].path != c.path || sent[0].body != "" {
			t.Errorf("%s: sent %+v, want GET %s", c.tool, sent, c.path)
			continue
		}
		for k, v := range c.query {
			if got := sent[0].query.Get(k); got != v {
				t.Errorf("%s: query %s = %q, want %q", c.tool, k, got, v)
			}
		}
		if len(sent[0].query) != len(c.query) {
			t.Errorf("%s: query = %v, want %v", c.tool, sent[0].query, c.query)
		}
	}
	if len(host.approvals) != 0 {
		t.Errorf("a read asked for approval: %+v", host.approvals)
	}
}

func TestReadToolsRefuseBadArguments(t *testing.T) {
	s := openAssistSession(t, &fakeHost{})
	var sent []readReq
	ctx := WithAPI(context.Background(), readAPI(&sent, `{}`))
	for _, c := range []struct{ tool, args string }{
		{"flow_run_get", `{"id":"../../api/settings"}`},
		{"flow_run_get", `{"id":"x","wait":"30s"}`}, // no long poll in a chat turn
		{"agent_run_events", `{"id":"x","limit":100000}`},
		{"agent_run_sandbox_log", `{"id":"x","limit":0}`},
		{"agent_run_get", `{}`},
		{"flows_list", `{"x":1}`},
		{"sensor_runs_list", `{"status":"failed"}`},
	} {
		if _, err := s.Call(ctx, c.tool, json.RawMessage(c.args)); err == nil {
			t.Errorf("%s %s: want an error", c.tool, c.args)
		}
	}
	if len(sent) != 0 {
		t.Errorf("refused calls reached the API: %+v", sent)
	}
}

func TestReadToolsReportAPIProblems(t *testing.T) {
	s := openAssistSession(t, &fakeHost{})
	api := func(context.Context, string, string, url.Values, []byte) (int, []byte, error) {
		return http.StatusNotFound, []byte(`{"title":"Not found","detail":"no flow run \"nope\""}`), nil
	}
	_, err := s.Call(WithAPI(context.Background(), api), "flow_run_get", json.RawMessage(`{"id":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "no flow run") {
		t.Errorf("err = %v", err)
	}
	if _, err := s.Call(context.Background(), "flows_list", nil); err == nil {
		t.Error("without an API the call must fail, not panic")
	}
}

func TestReadResultsAreCapped(t *testing.T) {
	s := openAssistSession(t, &fakeHost{})
	prompt := strings.Repeat("rendered prompt line\n", 20_000) // 400 KB in one string
	body, _ := json.Marshal(map[string]any{"id": "01JAGENT", "status": "failed", "error": "boom", "rendered_prompt": prompt, "approvals": []string{"a1"}})
	ctx := WithAPI(context.Background(), readAPI(new([]readReq), string(body)))

	res, err := s.Call(ctx, "agent_run_get", json.RawMessage(`{"id":"01JAGENT"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > MaxReadResult {
		t.Errorf("result is %d bytes, cap %d", len(res.Text), MaxReadResult)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
		t.Fatalf("a shrunk result must stay valid JSON: %v", err)
	}
	if got["status"] != "failed" || got["error"] != "boom" || got["approvals"] == nil {
		t.Errorf("the other fields must survive: %v", got)
	}
	if p, _ := got["rendered_prompt"].(string); !strings.Contains(p, "bytes omitted") || len(p) > MaxReadString+64 {
		t.Errorf("rendered_prompt is %d bytes, want it cut with a marker", len(p))
	}

	// A list that is large because it has many items is cut with a note instead.
	items := make([]string, 0, 5000)
	for range 5000 {
		items = append(items, strings.Repeat("e", 100))
	}
	big, _ := json.Marshal(map[string]any{"items": items})
	res, err = s.Call(WithAPI(context.Background(), readAPI(new([]readReq), string(big))), "events_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Text) > MaxReadResult+200 || !strings.Contains(res.Text, "truncated") {
		t.Errorf("list result: %d bytes, note present = %v", len(res.Text), strings.Contains(res.Text, "truncated"))
	}
}

func TestShrinkJSONLeavesSmallResultsAlone(t *testing.T) {
	const small = "{\n  \"a\": 1\n}"
	if got := ShrinkJSON(small, 100, 10); got != small {
		t.Errorf("got %q", got)
	}
	// Text that is not JSON is only cut, on a character boundary.
	got := ShrinkJSON(strings.Repeat("é", 100), 51, 10)
	if !strings.HasPrefix(got, strings.Repeat("é", 25)) || strings.Contains(got, "\ufffd") {
		t.Errorf("got %q", got)
	}
}
