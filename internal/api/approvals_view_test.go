package api

import (
	"encoding/json"
	"testing"

	"hal9001/internal/store"
)

// The approval view reads origin, details, conversation and the request machinery from the row; a node's request
// approval, a held tool call and an MCP approval each come out in the one shape (the harness validates every response
// against api/openapi.yaml).
func TestApprovalViewIsOneShape(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()

	// A chat-style tool call with details, a diff and a held note.
	held := seeded(&store.Approval{
		AgentRunID: f.agentRun.ID, Type: store.ApprovalToolCall, PermissionRequestID: f.perm.ID, DescriptionBy: store.DescByHal, Priority: store.PriorityHigh,
		Origin: store.OriginFlow, OriginLabel: "demo / review", ConversationID: f.agentRun.ID, ConversationKind: store.ConvFlow, Anchor: f.agentRun.ID + ":000004",
		HeldNote: "steering limit reached",
		Details: store.ApprovalDetails{
			Command: "npm test", Tool: "execute", Fields: []store.RequestField{{Key: "cwd", Value: "/work"}},
			Diffs: []store.ApprovalDiff{{Path: "main.go", Old: "a", New: "b"}},
		},
	})
	must(t, e.st.CreateApproval(ctx(), held))

	v := e.get("/api/approvals/" + held.ID).want(200).json()
	conv, _ := v["conversation"].(map[string]any)
	origin, _ := v["origin"].(map[string]any)
	details, _ := v["details"].(map[string]any)
	diffs, _ := details["diffs"].([]any)
	if conv["id"] != f.agentRun.ID || conv["kind"] != "flow" || conv["anchor"] != f.agentRun.ID+":000004" || conv["path"] != "/chat/"+f.agentRun.ID+"?message="+f.agentRun.ID+":000004" {
		t.Errorf("conversation = %v", conv)
	}
	if origin["kind"] != "flow" || origin["label"] != "demo / review" || origin["client"] != nil || v["held"] != true || v["held_note"] != "steering limit reached" ||
		v["description_by"] != "hal" || v["priority"] != "high" || v["gate"] != false || v["on_timeout"] != nil {
		t.Errorf("view = %v", v)
	}
	if details["command"] != "npm test" || details["tool"] != "execute" || len(diffs) != 1 || diffs[0].(map[string]any)["path"] != "main.go" {
		t.Errorf("details = %v", details)
	}

	// An MCP approval has no conversation and names its client; its machinery comes from the request JSON.
	meta, _ := json.Marshal(store.RequestApproval{Editable: []store.RequestEditable{{Key: "reply", Value: json.RawMessage(`"Hi"`)}}, OnTimeout: "denied", Detached: true})
	mcp := mcpApproval(string(meta))
	mcp.OriginLabel, mcp.OriginClient, mcp.DescriptionBy = "MCP: claude-code", "claude-code", store.DescByAgent
	must(t, e.st.CreateApproval(ctx(), mcp))
	v = e.get("/api/approvals/" + mcp.ID).want(200).json()
	origin, _ = v["origin"].(map[string]any)
	if v["conversation"] != nil || origin["kind"] != "mcp" || origin["client"] != "claude-code" || v["on_timeout"] != "denied" || v["description_by"] != "agent" ||
		v["agent_run_id"] != nil || v["flow_id"] != nil || len(v["editable"].([]any)) != 1 {
		t.Errorf("mcp view = %v", v)
	}

	// A gate belongs to its node run and says so.
	gate := seeded(&store.Approval{Type: store.ApprovalRequest, NodeRunID: f.nodeRun.ID, Request: `{"gate":true}`})
	must(t, e.st.CreateApproval(ctx(), gate))
	v = e.get("/api/approvals/" + gate.ID).want(200).json()
	if v["gate"] != true || v["node_run_id"] != f.nodeRun.ID || v["flow_id"] != "demo" || v["conversation"] != nil {
		t.Errorf("gate view = %v", v)
	}

	// The list filters by conversation and rejects a malformed id.
	got := e.get("/api/approvals?conversation_id=" + f.agentRun.ID).want(200).ids()
	if len(got) != 3 {
		t.Errorf("approvals of the conversation = %v, want the tool call, the proposal and the held call", got)
	}
	if got := e.get("/api/approvals?conversation_id=01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(200).ids(); len(got) != 0 {
		t.Errorf("approvals of another conversation = %v", got)
	}
	e.get("/api/approvals?conversation_id=not-an-id").want(400)
}

// Edits are only taken for approvals that declare editable paths, and cancel_run only for tool calls.
func TestApprovalDecisionValidation(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()
	meta, _ := json.Marshal(store.RequestApproval{Editable: []store.RequestEditable{{Key: "reply", Value: json.RawMessage(`"Hi"`)}}})
	editable, plain := mcpApproval(string(meta)), mcpApproval(`{}`)
	must(t, e.st.CreateApproval(ctx(), editable))
	must(t, e.st.CreateApproval(ctx(), plain))

	for name, tc := range map[string]struct{ path, body string }{
		"edits on an approval without editable": {"/api/approvals/" + plain.ID + "/approve", `{"edits":{"reply":"x"}}`},
		"edits on a tool call":                  {"/api/approvals/" + f.toolAppr.ID + "/approve", `{"edits":{"reply":"x"}}`},
		"an undeclared path":                    {"/api/approvals/" + editable.ID + "/approve", `{"edits":{"other":"x"}}`},
		"cancel_run on a proposal":              {"/api/approvals/" + f.propAppr.ID + "/deny", `{"cancel_run":true}`},
		"cancel_run on a request":               {"/api/approvals/" + editable.ID + "/deny", `{"cancel_run":true}`},
	} {
		if r := e.do("POST", tc.path, tc.body); r.Status != 422 {
			t.Errorf("%s: %d %s, want 422", name, r.Status, r.Body)
		}
	}
}

// mcpApproval is a stored MCP approval with the given request JSON.
func mcpApproval(request string) *store.Approval {
	a := seeded(&store.Approval{Type: store.ApprovalRequest, Request: request})
	a.Origin = store.OriginMCP
	return a
}
