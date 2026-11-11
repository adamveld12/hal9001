package e2e

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Emit from a continued chat: a call raises one tool_call approval, nothing runs before the decision, an approval starts
// a continuation run of the nodes behind the port, a denial is a tool error.

var ucStartedRun = regexp.MustCompile(`started flow run ([0-9A-Z]{26}) \(kind continuation\)`)

type ucContinuation struct {
	FlowRun struct {
		ID           string `json:"id"`
		Kind         string `json:"kind"`
		Status       string `json:"status"`
		Continuation *struct {
			OfRunID        string  `json:"of_run_id"`
			ConversationID string  `json:"conversation_id"`
			NodeID         string  `json:"node_id"`
			Port           string  `json:"port"`
			ApprovalID     *string `json:"approval_id"`
		} `json:"continuation"`
	} `json:"flow_run"`
}

func (d *Daemon) continuationRuns(originalFlowRun string) []FlowRun {
	d.t.Helper()
	var l struct {
		Items []FlowRun `json:"items"`
	}
	d.Get("/api/flow-runs?limit=200&continuation_of=" + originalFlowRun).Want(200).JSON(&l)
	return l.Items
}

func nodeIDs(det FlowRunDetail) []string {
	var out []string
	for _, n := range det.NodeRuns {
		out = append(out, n.NodeID+":"+n.Status)
	}
	return out
}

func TestUnifiedChatEmitApprovalStartsAContinuationRun(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine")
	origDetail := d.GetRun(orig.FlowRunID, 0)
	origEvents := len(d.AgentEvents(orig.ID))
	fork := d.fork(orig.ID)

	// An invalid payload is refused before any approval; the chat goes on.
	_, tr := e.ucTurn(fork.ID, "emit invalid")
	wantContains(t, "refusal transcript", tr, "mcp[e1] is_error=true", "denied by policy", "It was refused.")
	if n := len(d.pendingApprovals()); n != 0 {
		t.Fatalf("an invalid emit raised %d approvals", n)
	}
	if n := len(d.Approvals("")); n != 0 {
		t.Fatalf("an invalid emit left %d approvals", n)
	}
	if n := len(d.continuationRuns(orig.FlowRunID)); n != 0 {
		t.Fatalf("an invalid emit started %d runs", n)
	}

	// A valid call raises one approval and starts nothing.
	d.assistSend(fork.ID, "emit reviewed", nil)
	ap := d.WaitApproval("tool_call")
	d.assistWait(fork.ID, "awaiting_approval")
	if n := len(d.pendingApprovals()); n != 1 {
		t.Fatalf("pending approvals = %d, want 1", n)
	}
	var detail struct {
		PermissionRequest struct {
			Title  string `json:"title"`
			Reason string `json:"reason"`
		} `json:"permission_request"`
	}
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&detail)
	if detail.PermissionRequest.Title != "hal tool: emit_reviewed" {
		t.Errorf("approval title = %q", detail.PermissionRequest.Title)
	}
	wantContains(t, "approval reason", detail.PermissionRequest.Reason,
		"flow f", "node work -> port reviewed", "Leads to:", `"body": "chat says hi"`, `"priority": "urgent"`, "new flow run", "kind continuation", "original run "+orig.FlowRunID+" stays unchanged")
	if ap.PermissionRequest == nil || ap.PermissionRequest.Reason != detail.PermissionRequest.Reason {
		t.Errorf("the approval list shows another reason than the approval: %+v", ap.PermissionRequest)
	}
	if ap.AgentRunID == "" || ap.FlowID != "" && ap.FlowID != "f" {
		t.Errorf("approval = %+v", ap)
	}
	if linked := ucApprovalOfShell(d.conversationMessages(fork.ID), "emit_reviewed"); linked != ap.ID {
		t.Errorf("the chat message of the emit call links approval %q, want %q", linked, ap.ID)
	}
	if n := len(d.continuationRuns(orig.FlowRunID)); n != 0 {
		t.Fatalf("a run started before the decision: %d", n)
	}
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine")

	// Approving starts the continuation run, which runs only what is behind the port.
	d.Decide(ap.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	msgs := d.conversationMessages(fork.ID)
	var text string
	for _, m := range msgs {
		if m.ToolCall == nil {
			text += m.text() + "\n"
		}
	}
	m := ucStartedRun.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("the chat does not name the run:\n%s", text)
	}
	contID := m[1]
	wantContains(t, "chat text", text, "approved", "The original run "+orig.FlowRunID+" is unchanged.")
	if linked := ucApprovalOfShell(msgs, "emit_reviewed"); linked != ap.ID {
		t.Errorf("after the decision the emit call links approval %q, want %q", linked, ap.ID)
	}
	det := d.WaitRun(contID, "succeeded")
	if got := nodeIDs(det); len(got) != 4 || !ranNode(det, "pre") || !ranNode(det, "work") || !ranNode(det, "tag") || !ranNode(det, "shown") || ranNode(det, "asked") {
		t.Errorf("node runs of the continuation: %v", got)
	}
	e.wantUpstreamReused(origDetail, det, "pre")
	if w := d.Node(det, "work"); w.Decision != "emitted" || len(w.Ports) != 1 || w.Ports[0] != "reviewed" || !strings.Contains(w.Reason, fork.ID) {
		t.Errorf("the emitting node of the continuation: %+v", w)
	}
	var run ucContinuation
	d.Get("/api/flow-runs/" + contID).Want(200).JSON(&run)
	c := run.FlowRun.Continuation
	if run.FlowRun.Kind != "continuation" || c == nil || c.OfRunID != orig.FlowRunID || c.ConversationID != fork.ID || c.NodeID != "work" || c.Port != "reviewed" || c.ApprovalID == nil || *c.ApprovalID != ap.ID {
		t.Errorf("flow run over the API: %+v", run.FlowRun)
	}
	if runs := d.continuationRuns(orig.FlowRunID); len(runs) != 1 || runs[0].ID != contID || runs[0].Kind != "continuation" {
		t.Errorf("runs continuing the original: %+v", runs)
	}
	var byConv struct {
		Items []FlowRun `json:"items"`
	}
	d.Get("/api/flow-runs?conversation=" + fork.ID).Want(200).JSON(&byConv)
	if len(byConv.Items) != 1 || byConv.Items[0].ID != contID {
		t.Errorf("runs of the conversation: %+v", byConv.Items)
	}
	// The downstream nodes got the emitted payload shaped like a real emission.
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi")
	var payload struct {
		Input map[string]any `json:"input"`
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(d.NodeDetail(d.Node(det, "tag").ID).Stdin.Text), &payload); err != nil {
		t.Fatal(err)
	}
	out, _ := payload.Agent["output"].(map[string]any)
	if payload.Input["id"] != "7" || payload.Agent["port"] != "reviewed" || payload.Agent["run_id"] != orig.ID || out["body"] != "chat says hi" || out["priority"] != "urgent" {
		t.Errorf("payload of the continuation: %+v", payload)
	}

	// The original run, its node runs and its agent run are as they were.
	again := d.GetRun(orig.FlowRunID, 0)
	if strings.Join(nodeIDs(again), ",") != strings.Join(nodeIDs(origDetail), ",") || again.FlowRun.Status != "succeeded" || again.FlowRun.Kind != origDetail.FlowRun.Kind {
		t.Errorf("original run changed: %v, was %v", nodeIDs(again), nodeIDs(origDetail))
	}
	if n := len(d.AgentEvents(orig.ID)); n != origEvents {
		t.Errorf("the original agent run has %d events, had %d", n, origEvents)
	}
	if ars := d.AgentRuns(orig.NodeRunID); len(ars) != 1 || ars[0].Status != "succeeded" {
		t.Errorf("agent runs of the original node: %+v", ars)
	}

	// The chat's own system prompt states the rule.
	runs := d.assistRuns(fork.ID)
	wantContains(t, "rendered prompt", runs[0].RenderedPrompt,
		"This chat is a continuation of agent run "+orig.ID+" of flow `f`, node `work`",
		"It asks the user for approval, and an approval starts a new flow run from this node's output",
		"Call one only when the user explicitly asks you to send or emit the result to the flow; never on your own",
		"Tell the user once, in one sentence, that you can do this when they want it")

	// A denial is a tool error: the chat goes on and no run starts. The target node asks first when the next run gets there.
	d.assistSend(fork.ID, "emit needs human", nil)
	denied := d.WaitApproval("tool_call")
	d.Decide(denied.ID, false, map[string]any{"comment": "not now"})
	d.assistWait(fork.ID, "idle")
	runs = d.assistRuns(fork.ID)
	tr = d.Transcript(runs[len(runs)-1].ID)
	wantContains(t, "denied emit transcript", tr, "mcp[e1] is_error=true", "denied by the user: not now", "Asked to emit.")
	if n := len(d.continuationRuns(orig.FlowRunID)); n != 1 {
		t.Errorf("a denied emit started a run: %d continuation runs", n)
	}
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi")

	// An approved emit on the other port: the continuation run still asks at the downstream node that requires approval.
	d.assistSend(fork.ID, "emit needs human", nil)
	ap2 := d.WaitApproval("tool_call")
	d.Decide(ap2.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	var id2 string
	d.Eventually("the second continuation run", func() bool {
		for _, r := range d.continuationRuns(orig.FlowRunID) {
			if r.ID != contID {
				id2 = r.ID
			}
		}
		return id2 != ""
	})
	parked := d.WaitRun(id2, "awaiting_approval")
	if ranNode(parked, "shown") || ranNode(parked, "tag") || len(parked.NodeRuns) != 3 {
		t.Errorf("the second run ran nodes behind the other port: %v", nodeIDs(parked))
	}
	e.wantUpstreamReused(origDetail, parked, "pre")
	gate := d.gateApproval()
	if gate.FlowRun != id2 || !gate.Request.Gate {
		t.Errorf("the gate of the downstream node: %+v", gate)
	}
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi")
	d.Decide(gate.ID, true, nil)
	done := d.WaitRun(id2, "succeeded")
	if !ranNode(done, "asked") {
		t.Errorf("node runs of the second run: %v", nodeIDs(done))
	}
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi", "Needs please look")
}

// A node without declared ports has one emit tool, emit_out: its payload is the final message, and the run behind the
// out port gets it.
func TestUnifiedChatEmitOutOfANodeWithoutPorts(t *testing.T) {
	e := newUC(t, ucOpts{load: false, noPorts: true})
	d, orig := e.d, e.orig
	sameTitles(t, d.Inbox(), "Out Reviewed fine")
	fork := d.fork(orig.ID)
	_, _ = e.ucTurn(fork.ID, "read the file") // opens the agent session so its tools can be listed
	tools := d.halTools()
	if _, ok := tools["emit_out"]; !ok {
		t.Fatalf("no emit_out in %v", keys(tools))
	}
	for name := range tools {
		if strings.HasPrefix(name, "emit_") && name != "emit_out" {
			t.Errorf("unexpected emit tool %q", name)
		}
	}
	wantContains(t, "emit_out description", tools["emit_out"], "ONLY when the user explicitly asked you to send or emit the result to the flow", "Leads to:")

	d.assistSend(fork.ID, "emit out", nil)
	ap := d.WaitApproval("tool_call")
	var detail struct {
		PermissionRequest struct {
			Reason string `json:"reason"`
		} `json:"permission_request"`
	}
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&detail)
	wantContains(t, "approval reason", detail.PermissionRequest.Reason, "node work -> port out", `"final_message": "chat result"`)
	d.Decide(ap.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	var id string
	d.Eventually("the continuation run", func() bool {
		if rs := d.continuationRuns(orig.FlowRunID); len(rs) == 1 {
			id = rs[0].ID
		}
		return id != ""
	})
	det := d.WaitRun(id, "succeeded")
	if !ranNode(det, "pre") || !ranNode(det, "work") || !ranNode(det, "shown") || len(det.NodeRuns) != 3 {
		t.Errorf("node runs: %v", nodeIDs(det))
	}
	e.wantUpstreamReused(e.run, det, "pre")
	sameTitles(t, d.Inbox(), "Out Reviewed fine", "Out chat result")
}

// A continuation run is a flow run like any other: its agent node runs can be continued in a chat too, and an emit from
// there starts a continuation of the continuation.
func TestUnifiedChatContinuesAContinuationRun(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.IdleSensor("src")
		d.Runtime("fake", script{LoadSession: true, Turns: []turn{
			{When: when("emit ok$"), Steps: []step{toolCall("emit_ok", "e1", `{"priority":"normal","body":"from chat a"}`), say("Asked.")}},
			{When: when("emit done$"), Steps: []step{toolCall("emit_done", "e1", `{"priority":"normal","body":"from chat b"}`), say("Asked.")}},
			{When: when("First"), Steps: []step{toolCall("emit_ok", "f1", `{"priority":"urgent","body":"a says ok"}`), say("a done")}},
			{When: when("Second"), Steps: []step{toolCall("emit_done", "f2", `{"priority":"normal","body":"b says done"}`), say("b done")}},
		}})
		d.Flow("g", `id: g
enabled: true
subscribe: [src]
nodes:
  - id: a
    type: agent
    runtime: fake
    mode: readonly
    instructions: |
      First {{.input.id}}
    ports:
      ok:
        description: A is fine.
        schema: `+reviewedPortSchema+`
  - id: b
    type: agent
    runtime: fake
    mode: readonly
    instructions: |
      Second {{.input.agent.output.body}}
    ports:
      done:
        description: B is fine.
        schema: `+reviewedPortSchema+`
  - {id: shown, type: inbox, title: "Done {{.input.agent.output.body}}"}
edges:
  - {from: a, port: ok, to: b}
  - {from: b, port: done, to: shown}
`)
	})
	run := d.RunOf("src", map[string]any{"id": "7"})
	sameTitles(t, d.Inbox(), "Done b says done")
	aRun := d.AgentRuns(d.Node(run, "a").ID)[0]

	// First continuation: from agent node a, which runs b again with the chat's payload.
	forkA := d.fork(aRun.ID)
	d.assistSend(forkA.ID, "emit ok", nil)
	d.Decide(d.WaitApproval("tool_call").ID, true, nil)
	d.assistWait(forkA.ID, "idle")
	var c1 string
	d.Eventually("the first continuation run", func() bool {
		if rs := d.continuationRuns(run.FlowRun.ID); len(rs) == 1 {
			c1 = rs[0].ID
		}
		return c1 != ""
	})
	det1 := d.WaitRun(c1, "succeeded")
	if len(det1.NodeRuns) != 3 || !ranNode(det1, "a") || !ranNode(det1, "b") || !ranNode(det1, "shown") {
		t.Fatalf("node runs of the first continuation: %v", nodeIDs(det1))
	}
	bRun := d.AgentRuns(d.Node(det1, "b").ID)
	if len(bRun) != 1 || !strings.Contains(bRun[0].RenderedPrompt, "Second from chat a") {
		t.Fatalf("agent run of b in the continuation: %+v", bRun)
	}

	// Second continuation: from agent node b of the first continuation.
	forkB := d.fork(bRun[0].ID)
	if c := d.conversation(forkB.ID); c.ForkedFrom == nil || c.ForkedFrom.AgentRunID != bRun[0].ID || *c.ForkedFrom.NodeID != "b" {
		t.Fatalf("fork of the continuation's agent run: %+v", c)
	}
	d.assistSend(forkB.ID, "emit done", nil)
	d.Decide(d.WaitApproval("tool_call").ID, true, nil)
	d.assistWait(forkB.ID, "idle")
	var c2 string
	d.Eventually("the second continuation run", func() bool {
		if rs := d.continuationRuns(c1); len(rs) == 1 {
			c2 = rs[0].ID
		}
		return c2 != ""
	})
	det2 := d.WaitRun(c2, "succeeded")
	var run2 ucContinuation
	d.Get("/api/flow-runs/" + c2).Want(200).JSON(&run2)
	c := run2.FlowRun.Continuation
	if c == nil || c.OfRunID != c1 || c.ConversationID != forkB.ID || c.NodeID != "b" || c.Port != "done" {
		t.Errorf("second continuation: %+v", run2.FlowRun)
	}
	if got := nodeIDs(det2); len(got) != 3 || !ranNode(det2, "a") || !ranNode(det2, "b") || !ranNode(det2, "shown") {
		t.Errorf("node runs of the second continuation: %v", got)
	}
	// The original run has exactly one continuation, the first continuation exactly one.
	if n := len(d.continuationRuns(run.FlowRun.ID)); n != 1 {
		t.Errorf("continuations of the original run: %d", n)
	}
	sameTitles(t, d.Inbox(), "Done b says done", "Done b says done", "Done from chat b")
}

// The emit tools do not depend on the sandbox: a node without one gets them too, and none of the sandbox tools.
func TestUnifiedChatEmitFromANodeWithoutSandbox(t *testing.T) {
	e := newUCWith(t, ucOpts{load: true, noSandbox: true}, nil)
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	d.assistSend(fork.ID, "emit reviewed", nil)
	ap := d.WaitApproval("tool_call")
	tools := d.halTools()
	for _, name := range []string{"shell", "fs_read", "fs_write"} {
		if _, ok := tools[name]; ok {
			t.Errorf("a node without sandbox got %s", name)
		}
	}
	for _, name := range []string{"emit_reviewed", "emit_needs_human", "shout", "flows_list"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("the chat lacks %s: %v", name, keys(tools))
		}
	}
	d.Decide(ap.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	var id string
	d.Eventually("the continuation run", func() bool {
		if rs := d.continuationRuns(orig.FlowRunID); len(rs) == 1 {
			id = rs[0].ID
		}
		return id != ""
	})
	d.WaitRun(id, "succeeded")
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi")
	if sandboxes := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log"); len(sandboxes.Sessions) != 0 {
		t.Errorf("sandbox sessions of a chat without sandbox: %+v", sandboxes.Sessions)
	}
}

// With the kill switch engaged an approved emit still starts the run, but it waits in the queue and the chat says so.
func TestUnifiedChatEmitWhilePausedQueuesTheRun(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	d.assistSend(fork.ID, "emit reviewed", nil)
	ap := d.WaitApproval("tool_call")
	d.Post("/api/killswitch/pause", map[string]any{}).Want(200)
	d.Decide(ap.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	var text string
	for _, m := range d.conversationMessages(fork.ID) {
		text += m.text() + "\n"
	}
	wantContains(t, "chat text", text, "The kill switch is engaged, so the run waits in the queue until it is released.")
	m := ucStartedRun.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("the chat does not name the run:\n%s", text)
	}
	time.Sleep(200 * time.Millisecond)
	if got := d.GetRun(m[1], 0); got.FlowRun.Status != "queued" {
		t.Fatalf("the run while paused: %s", describeRun(got))
	}
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine")
	d.Post("/api/killswitch/resume", nil).Want(200)
	d.WaitRun(m[1], "succeeded")
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi")
}

// The continuation runs the flow version the original run executed: after the flow was edited, and after it was
// deleted, the chat still emits into the version of its run.
func TestUnifiedChatEmitUsesTheFlowVersionOfTheRun(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	loaded := d.GetFlow("f")
	edited := strings.Replace(strings.Replace(ucFlow(false), `title: "Reviewed`, `title: "EDITED`, 1), "{from: tag, to: shown}", "{from: tag, to: shown}\n  - {from: work, port: reviewed, to: asked}", 1)
	d.Put("/api/flows/f", map[string]any{"expected_hash": loaded.Hash, "yaml": edited}).Want(200)
	if d.GetFlow("f").Hash == loaded.Hash {
		t.Fatal("the flow was not edited")
	}

	fork := d.fork(orig.ID)
	d.assistSend(fork.ID, "emit reviewed", nil)
	d.Decide(d.WaitApproval("tool_call").ID, true, nil)
	d.assistWait(fork.ID, "idle")
	var first string
	d.Eventually("the first continuation run", func() bool {
		if rs := d.continuationRuns(orig.FlowRunID); len(rs) == 1 {
			first = rs[0].ID
		}
		return first != ""
	})
	det := d.WaitRun(first, "succeeded")
	if det.FlowRun.FlowHash != loaded.Hash || len(det.NodeRuns) != 4 || ranNode(det, "asked") {
		t.Errorf("the continuation did not run the version of the original run (hash %s, want %s): %v", det.FlowRun.FlowHash, loaded.Hash, nodeIDs(det))
	}
	sameTitles(t, d.Inbox(), "Reviewed Flow says fine", "Reviewed chat says hi")

	// The flow is deleted: the fork and its emit tools still work, from the stored version.
	d.Delete("/api/flows/f?expected_hash=" + d.GetFlow("f").Hash).Want(204)
	d.assistSend(fork.ID, "emit reviewed", nil)
	d.Decide(d.WaitApproval("tool_call").ID, true, nil)
	d.assistWait(fork.ID, "idle")
	var second string
	d.Eventually("the second continuation run", func() bool {
		for _, r := range d.continuationRuns(orig.FlowRunID) {
			if r.ID != first {
				second = r.ID
			}
		}
		return second != ""
	})
	d.WaitRun(second, "succeeded", "failed")
	if got := d.GetRun(second, 0); got.FlowRun.Status != "succeeded" {
		t.Errorf("continuation of a deleted flow:\n%s", describeRun(got))
	}
}
