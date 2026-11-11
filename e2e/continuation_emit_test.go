package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/engine"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// A real flow run (a fake agent finishing through emit_reviewed) is continued from a chat session of the engine: the
// emit tool of the other port waits for approval, an approval starts a continuation run that runs only the nodes behind
// that port, and a restarted daemon shows the run and its link through the API.
func TestContinuationEmitFromAChatSession(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{Steps: []step{toolCall("emit_reviewed", "e1", `{"priority":"urgent","body":"Looks fine"}`), say("done")}})
	orig := d.RunOf("src", map[string]any{"id": "7"})
	d.WaitRun(orig.FlowRun.ID, "succeeded")
	sameTitles(t, d.Inbox(), "Reviewed Looks fine")
	d.Stop() // the engine below takes over the home and database

	ctx := context.Background()
	st, err := store.Open(filepath.Join(d.Home, "data", "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(engine.Options{Store: st, Home: d.Home, BasePATH: os.Getenv("PATH"), UIBaseURL: "http://localhost:9001"})
	if err != nil {
		t.Fatal(err)
	}
	eng.Apply(spec.Load(d.Home))
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	closed := false
	closeEngine := func() {
		if closed {
			return
		}
		closed = true
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := eng.Stop(sctx); err != nil {
			t.Errorf("stop engine: %v", err)
		}
		_ = st.Close()
	}
	t.Cleanup(closeEngine)

	// The original agent run, as the conversation's fork would resolve it.
	work := d.Node(orig, "work")
	runs, err := st.ListAgentRuns(ctx, store.AgentRunFilter{NodeRunID: work.ID, ListOpts: store.ListOpts{Limit: 5}})
	if err != nil || len(runs.Items) != 1 {
		t.Fatalf("agent runs of the node: %v %v", runs.Items, err)
	}
	fork := core.ForkNode{
		AgentRunID: runs.Items[0].ID, NodeRunID: work.ID, FlowRunID: orig.FlowRun.ID, FlowID: "f", NodeID: "work",
		Ports: []core.ForkPort{{Name: "reviewed"}, {Name: "needs_human"}},
	}
	chat := &store.AssistSession{
		TargetKind: store.AssistTargetGeneral, Runtime: "fake", DraftYAML: "id: x\n", DraftHash: strings.Repeat("a", 64), DraftUpdatedBy: store.DraftByUser,
	}
	if err := st.CreateAssistSession(ctx, chat); err != nil {
		t.Fatal(err)
	}
	chatTurn := &store.AgentRun{
		AssistSessionID: chat.ID, Runtime: "fake", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated,
		Phase: store.PhaseExecute, Status: store.AgentRunning, Prompt: store.Inline("hi"),
	}
	if err := st.CreateAgentRun(ctx, chatTurn); err != nil {
		t.Fatal(err)
	}
	extra, err := eng.AssistHost().EmitTools(ctx, fork, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := eng.ToolHub().Open(ctx, tools.RunSpec{
		Kind: tools.KindAssist, RunID: chat.ID, ApprovalTimeout: time.Minute, ExtraTools: extra,
		Info: func() tools.RunInfo { return tools.RunInfo{AgentRunID: chatTurn.ID, AssistSessionID: chat.ID} },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Invalid payload: refused before any approval.
	if _, err := sess.Call(ctx, "emit_needs_human", json.RawMessage(`{"priority":"soon","body":"x"}`)); err == nil || !strings.Contains(err.Error(), "denied by policy") {
		t.Fatalf("invalid emit: %v", err)
	}

	// Valid payload on the other port: waits for the approval; approving starts the run.
	type result struct {
		res tools.Result
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := sess.Call(ctx, "emit_needs_human", json.RawMessage(`{"priority":"urgent","body":"please look"}`))
		done <- result{res, err}
	}()
	var ap store.Approval
	deadline := time.Now().Add(30 * time.Second)
	for ap.ID == "" {
		page, err := st.ListApprovals(ctx, store.ApprovalFilter{Statuses: []store.ApprovalState{store.ApprovalPending}, Type: store.ApprovalToolCall, ListOpts: store.ListOpts{Limit: 5}})
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case len(page.Items) > 0:
			ap = page.Items[0]
		case time.Now().After(deadline):
			t.Fatal("no tool_call approval appeared")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	pr, err := st.GetPermissionRequest(ctx, ap.PermissionRequestID)
	if err != nil || !strings.Contains(pr.Reason, "node work -> port needs_human") || !strings.Contains(pr.Reason, "please look") {
		t.Fatalf("approval reason: %+v %v", pr, err)
	}
	if _, err := eng.DecideApproval(ctx, ap.ID, core.ApprovalDecision{Approve: true, Principal: "e2e"}); err != nil {
		t.Fatal(err)
	}
	var r result
	select {
	case r = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the emit call did not return")
	}
	if r.err != nil || !strings.Contains(r.res.Text, "started flow run ") || !strings.Contains(r.res.Text, "(kind continuation)") {
		t.Fatalf("emit result: %+v", r)
	}
	page, err := st.ListFlowRuns(ctx, store.FlowRunFilter{ContinuationOf: orig.FlowRun.ID, ListOpts: store.ListOpts{Limit: 5}})
	if err != nil || len(page.Items) != 1 || !strings.Contains(r.res.Text, page.Items[0].ID) {
		t.Fatalf("continuation runs: %v %v (result %q)", page.Items, err, r.res.Text)
	}
	cont := page.Items[0]
	for cur := cont; !cur.Status.Terminal(); {
		if time.Now().After(deadline.Add(30 * time.Second)) {
			t.Fatalf("continuation run stuck in %s", cur.Status)
		}
		time.Sleep(20 * time.Millisecond)
		got, err := st.GetFlowRun(ctx, cont.ID)
		if err != nil {
			t.Fatal(err)
		}
		cur = *got
		cont = cur
	}
	if cont.Status != store.FlowRunSucceeded {
		t.Fatalf("continuation run ended %s: %s", cont.Status, cont.Error)
	}
	sess.Close()
	closeEngine()

	// The restarted daemon shows the run, its link and the inbox: the original's item once, the continuation's once.
	d.Start()
	det := d.WaitRun(cont.ID, "succeeded")
	var run struct {
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
	}
	d.Get("/api/flow-runs/" + cont.ID).Want(200).JSON(&run)
	c := run.FlowRun.Continuation
	if run.FlowRun.Kind != "continuation" || c == nil || c.OfRunID != orig.FlowRun.ID || c.ConversationID != chat.ID || c.NodeID != "work" || c.Port != "needs_human" || c.ApprovalID == nil || *c.ApprovalID != ap.ID {
		t.Fatalf("flow run over the API: %+v", run)
	}
	if ranNode(det, "shown") || !ranNode(det, "asked") || !ranNode(det, "work") || len(det.NodeRuns) != 2 {
		t.Errorf("continuation node runs: %+v", det.NodeRuns)
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	d.Get("/api/flow-runs?continuation_of=" + orig.FlowRun.ID).Want(200).JSON(&list)
	if len(list.Items) != 1 || list.Items[0].ID != cont.ID {
		t.Errorf("runs continuing the original: %+v", list.Items)
	}
	sameTitles(t, d.Inbox(), "Reviewed Looks fine", "Needs please look")
}
