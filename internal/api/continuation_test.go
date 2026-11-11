package api

import (
	"slices"
	"strings"
	"testing"

	"hal9001/internal/store"
)

// continuationRun stores a continuation run of orig started from the conversation.
func (e *env) continuationRun(orig *store.FlowRun, conversation string) *store.FlowRun {
	e.t.Helper()
	fr := &store.FlowRun{
		FlowID: orig.FlowID, FlowHash: orig.FlowHash, EventID: orig.EventID,
		Continuation: &store.Continuation{OfRunID: orig.ID, ConversationID: conversation, NodeID: "filter", Port: "out", ApprovalID: e.st.NewID()},
	}
	must(e.t, e.st.CreateContinuationRun(ctx(), fr, []*store.NodeRun{{NodeID: "filter", NodeType: store.NodeCommand, Status: store.NodeDone}}))
	return fr
}

func TestContinuationRunsAreListedLinkedAndFiltered(t *testing.T) {
	e := newEnv(t)
	ev := e.event("gh", "1", `{}`)
	orig := e.flowRun("A", ev.ID, store.KindLive, store.FlowRunSucceeded)
	other := e.flowRun("A", "", store.KindLive, store.FlowRunSucceeded)
	chatA, chatB := e.st.NewID(), e.st.NewID()
	c1 := e.continuationRun(orig, chatA)
	c2 := e.continuationRun(orig, chatB)
	c3 := e.continuationRun(other, chatA)

	var detail struct {
		FlowRun struct {
			Kind         string `json:"kind"`
			Continuation *struct {
				OfRunID        string  `json:"of_run_id"`
				ConversationID string  `json:"conversation_id"`
				NodeID         string  `json:"node_id"`
				Port           string  `json:"port"`
				ApprovalID     *string `json:"approval_id"`
			} `json:"continuation"`
		} `json:"flow_run"`
		NodeRuns []struct {
			NodeID string `json:"node_id"`
		} `json:"node_runs"`
	}
	e.get("/api/flow-runs/" + c1.ID).want(200).decode(&detail)
	c := detail.FlowRun.Continuation
	if detail.FlowRun.Kind != "continuation" || c == nil || c.OfRunID != orig.ID || c.ConversationID != chatA || c.NodeID != "filter" || c.Port != "out" ||
		c.ApprovalID == nil || *c.ApprovalID != c1.Continuation.ApprovalID || len(detail.NodeRuns) != 1 {
		t.Fatalf("continuation run = %+v", detail)
	}
	if got := e.get("/api/flow-runs/" + orig.ID).want(200).json()["flow_run"].(map[string]any)["continuation"]; got != nil {
		t.Errorf("a live run has continuation %v, want null", got)
	}

	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{c3.ID, c2.ID, c1.ID, other.ID, orig.ID}}, // continuation runs are listed like live and replay runs
		{"kind=continuation", []string{c3.ID, c2.ID, c1.ID}},
		{"continuation_of=" + orig.ID, []string{c2.ID, c1.ID}},
		{"conversation=" + chatA, []string{c3.ID, c1.ID}},
		{"continuation_of=" + orig.ID + "&conversation=" + chatA, []string{c1.ID}},
		{"kind=live", []string{other.ID, orig.ID}},
	} {
		if got := e.get("/api/flow-runs?" + tc.query).want(200).ids(); !slices.Equal(got, tc.want) {
			t.Errorf("?%s: got %v, want %v", tc.query, got, tc.want)
		}
	}
	e.get("/api/flow-runs?continuation_of=lower").want(400)
	e.get("/api/flow-runs?conversation=lower").want(400)
	e.get("/api/flow-runs?kind=other").want(400)

	// The event's timeline shows them next to the run they continue.
	var events struct {
		Items []struct {
			FlowRuns []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"flow_runs"`
		} `json:"items"`
	}
	e.get("/api/events").want(200).decode(&events)
	var kinds []string
	for _, r := range events.Items[0].FlowRuns {
		kinds = append(kinds, r.Kind)
	}
	if !slices.Contains(kinds, "continuation") || !slices.Contains(kinds, "live") {
		t.Errorf("event timeline run kinds = %v, want live and continuation", kinds)
	}
}

// The approval of an emit call carries its reason and arguments, so the Inbox and the chat card can show what is
// being approved.
func TestToolCallApprovalExposesReasonAndArguments(t *testing.T) {
	e := newEnv(t)
	a := e.assistSession("", "awaiting_approval")
	turn := &store.AgentRun{
		AssistSessionID: a.ID, Runtime: "claude", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated,
		Phase: store.PhaseExecute, Status: store.AgentAwaitingApproval, Prompt: store.Inline("hi"),
	}
	must(t, e.st.CreateAgentRun(ctx(), turn))
	pr := &store.PermissionRequest{
		AgentRunID: turn.ID, ToolCallID: "hal:emit_ship:1", ToolKind: "other", Title: "hal tool: emit_ship",
		RawInput: store.Inline(`{"tool":"emit_ship","arguments":{"body":"hi"}}`), Reason: "Emit a result to flow f: node work -> port ship.", MatchedRule: "hal:approval",
	}
	must(t, e.st.CreatePermissionRequest(ctx(), pr))
	ap := &store.Approval{AgentRunID: turn.ID, Type: store.ApprovalToolCall, PermissionRequestID: pr.ID,
		Title: "Emit ship", Description: "Emit a result to flow f.", Origin: store.OriginChat}
	must(t, e.st.CreateApproval(ctx(), ap))

	got := e.get("/api/approvals/" + ap.ID).want(200).json()["permission_request"].(map[string]any)
	if !strings.HasPrefix(got["reason"].(string), "Emit a result to flow f: node work -> port ship.") || got["title"] != "hal tool: emit_ship" {
		t.Errorf("permission request = %v", got)
	}
	if args := got["raw_input"].(map[string]any)["arguments"].(map[string]any); args["body"] != "hi" {
		t.Errorf("raw_input = %v", got["raw_input"])
	}
}
