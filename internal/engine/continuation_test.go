package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/engine"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// contFlow is a flow whose agent node `work` has two ports. The original run of it is seeded into the store (a real
// agent run needs the MCP bridge of a daemon; see e2e/continuation_emit_test.go for one).
const contFlow = `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: prep, type: command, lang: bash, script: cat}
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: "Review {{.input.id}}"
    ports:
      ship:
        description: Ship the result.
        schema: {type: object, required: [body], properties: {body: {type: string}}}
      hold:
        description: Keep it for later.
        schema: {type: object, required: [why], properties: {why: {type: string}}}
  - {id: announce, type: inbox, title: "TITLE {{.input.agent.output.body}} id={{.input.input.id}} event={{.event.id}} kind={{.run.kind}}"}
  - {id: parked, type: inbox, title: "parked {{.input.agent.output.why}}"}
edges:
  - {from: prep, to: work}
  - {from: work, port: ship, to: announce}
  - {from: work, port: hold, to: parked}
`

// seeded is a finished original run: prep -> work, with an agent run behind work.
type seeded struct {
	flowRun, prep, work, agentRun string
	event                         string
}

type seedOpts struct {
	yaml   string
	dryRun bool
	kind   store.FlowRunKind
}

// seedOriginal stores a finished run of yaml (stored as its own version) for the event {"id":"1"}.
func (h *harness) seedOriginal(o seedOpts) seeded {
	h.t.Helper()
	ctx := context.Background()
	if o.kind == "" {
		o.kind = store.KindLive
	}
	ev := &store.Event{SensorID: "src", DedupKey: "k" + h.st.NewID(), Payload: store.Inline(`{"id":"1"}`)}
	if created, err := h.st.IngestEvent(ctx, ev); err != nil || !created {
		h.t.Fatalf("ingest: %v", err)
	}
	fr := &store.FlowRun{
		FlowID: "f", FlowHash: "orig-" + h.st.NewID(), FlowYAML: o.yaml, EventID: ev.ID, Kind: o.kind, DryRun: o.dryRun, Status: store.FlowRunSucceeded,
	}
	if err := h.st.CreateFlowRun(ctx, fr); err != nil {
		h.t.Fatal(err)
	}
	prep := &store.NodeRun{
		FlowRunID: fr.ID, NodeID: "prep", NodeType: store.NodeCommand, Status: store.NodeDone, Decision: store.DecisionEmitted,
		Ports: []string{"out"}, OutputCount: 1, Stdin: store.Inline(`{"id":"1"}`), Stdout: store.Inline(`{"id":"1"}`), Reason: "checked",
	}
	if err := h.st.CreateNodeRun(ctx, prep); err != nil {
		h.t.Fatal(err)
	}
	work := &store.NodeRun{
		FlowRunID: fr.ID, NodeID: "work", NodeType: store.NodeAgent, ParentNodeRunID: prep.ID, Status: store.NodeDone, Decision: store.DecisionEmitted,
		Ports: []string{"hold"}, OutputCount: 1, Stdin: store.Inline(`{"id":"1"}`),
	}
	if err := h.st.CreateNodeRun(ctx, work); err != nil {
		h.t.Fatal(err)
	}
	ar := &store.AgentRun{
		NodeRunID: work.ID, Runtime: "fake", Mode: store.ModeReadonly, Policy: store.PolicyReadonly, Phase: store.PhaseExecute,
		Status: store.AgentSucceeded, Prompt: store.Inline("Review 1"),
	}
	if err := h.st.CreateAgentRun(ctx, ar); err != nil {
		h.t.Fatal(err)
	}
	return seeded{flowRun: fr.ID, prep: prep.ID, work: work.ID, agentRun: ar.ID, event: ev.ID}
}

func (h *harness) continuationHarness(workspaceFlow string) {
	h.t.Helper()
	h.idleSensor()
	h.runtime("fake", script{})
	h.flow("f", workspaceFlow)
	h.start()
}

func (h *harness) startContinuation(s seeded, port, payload string) (string, error) {
	return h.eng.StartContinuation(context.Background(), engine.ContinuationRequest{
		OriginalAgentRunID: s.agentRun, ConversationID: "01CONVERSATION000000000000", Port: port, Payload: json.RawMessage(payload), ApprovalID: "01APPROVAL0000000000000000",
	})
}

func (h *harness) nodeIDs(runID string) []string {
	var out []string
	for _, n := range h.nodeRuns(runID) {
		out = append(out, n.NodeID)
	}
	return out
}

func sameIDs(got []string, want ...string) bool {
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// Only the nodes behind the emitted port run, with the emission as their input; the upstream result is reused, the
// agent node is recorded as emitted, and the original run is untouched. The stored flow version is the one that runs.
func TestContinuationRunsOnlyWhatFollowsThePort(t *testing.T) {
	h := newHarness(t)
	orig := strings.ReplaceAll(contFlow, "TITLE", "stored")
	h.continuationHarness(strings.ReplaceAll(contFlow, "TITLE", "edited-since")) // the workspace flow was edited after the original run
	s := h.seedOriginal(seedOpts{yaml: orig})
	before := h.dumpRun(s.flowRun)

	id, err := h.startContinuation(s, "ship", `{"body":"hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	fr := h.waitRun(id, store.FlowRunSucceeded)
	if fr.Kind != store.KindContinuation || fr.FlowID != "f" || fr.EventID != s.event || fr.DryRun {
		t.Fatalf("continuation run: %+v", fr)
	}
	want := store.Continuation{OfRunID: s.flowRun, ConversationID: "01CONVERSATION000000000000", NodeID: "work", Port: "ship", ApprovalID: "01APPROVAL0000000000000000"}
	if fr.Continuation == nil || *fr.Continuation != want {
		t.Fatalf("continuation link = %+v, want %+v", fr.Continuation, want)
	}
	if origRun := h.run(s.flowRun); origRun.FlowHash != fr.FlowHash || origRun.Continuation != nil {
		t.Errorf("the run executes the version of the original run: %q vs %q", fr.FlowHash, origRun.FlowHash)
	}
	if got := h.nodeIDs(id); !sameIDs(got, "prep", "work", "announce") {
		t.Fatalf("node runs %v, want prep, work, announce only\n%s", got, h.dumpRun(id))
	}

	prep, work, announce := h.node(id, "prep"), h.node(id, "work"), h.node(id, "announce")
	if prep.Status != store.NodeDone || prep.ID == s.prep || prep.ParentNodeRunID != "" || !strings.HasPrefix(prep.Reason, "reused from flow run "+s.flowRun) ||
		h.text(prep.Stdout) != `{"id":"1"}` || h.text(prep.Stdin) != `{"id":"1"}` {
		t.Errorf("reused upstream node: %+v", prep)
	}
	if work.Status != store.NodeDone || work.Decision != store.DecisionEmitted || work.ParentNodeRunID != prep.ID || len(work.Ports) != 1 || work.Ports[0] != "ship" ||
		work.Reason != "emitted from conversation 01CONVERSATION000000000000" || work.OutputCount != 1 || h.text(work.Stdin) != `{"id":"1"}` {
		t.Errorf("emitting node: %+v", work)
	}
	var payload struct {
		Input map[string]any `json:"input"`
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(h.text(announce.Stdin)), &payload); err != nil {
		t.Fatal(err)
	}
	out, _ := payload.Agent["output"].(map[string]any)
	if payload.Input["id"] != "1" || payload.Agent["run_id"] != s.agentRun || payload.Agent["status"] != "succeeded" || payload.Agent["port"] != "ship" || out["body"] != "hello" {
		t.Errorf("emission payload: %+v", payload)
	}
	for _, k := range []string{"final_message", "note", "proposal", "comment"} {
		if _, ok := payload.Agent[k]; !ok {
			t.Errorf("payload agent has no %q: %v", k, payload.Agent)
		}
	}
	if h.text(work.Stdout) != h.text(announce.Stdin) || announce.ParentNodeRunID != work.ID || announce.Status != store.NodeDone {
		t.Errorf("announce node %+v", announce)
	}
	if items := h.inboxItems(); len(items) != 1 || items[0].Title != "stored hello id=1 event=1 kind=continuation" {
		t.Errorf("inbox = %+v, want one item from the stored version", items)
	}
	if got := h.agentRuns(work.ID); len(got) != 0 {
		t.Errorf("the emitting node was run again: %d agent runs", len(got))
	}
	if after := h.dumpRun(s.flowRun); after != before {
		t.Errorf("the original run changed:\nbefore %s\nafter %s", before, after)
	}

	// Listing: by original run, by conversation, and in the default list next to live runs.
	for _, f := range []store.FlowRunFilter{{ContinuationOf: s.flowRun}, {Conversation: "01CONVERSATION000000000000"}, {Kinds: []store.FlowRunKind{store.KindContinuation}}, {}} {
		f.Limit = 10
		page, err := h.st.ListFlowRuns(context.Background(), f)
		if err != nil || len(page.Items) == 0 || page.Items[0].ID != id {
			t.Errorf("list %+v: %v %v", f, page.Items, err)
		}
	}
}

// The other port of the node, and ports and nodes that do not exist, are errors; nothing is created.
func TestContinuationRejectsWhatCannotRun(t *testing.T) {
	h := newHarness(t)
	yaml := strings.ReplaceAll(contFlow, "TITLE", "t")
	h.continuationHarness(yaml)
	s := h.seedOriginal(seedOpts{yaml: yaml})
	count := func() int {
		page, err := h.st.ListFlowRuns(context.Background(), store.FlowRunFilter{Kinds: []store.FlowRunKind{store.KindContinuation}, ListOpts: store.ListOpts{Limit: 50}})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}

	// A port that has no edge in the stored version.
	noEdge := h.seedOriginal(seedOpts{yaml: strings.Replace(yaml, "  - {from: work, port: hold, to: parked}\n", "", 1)})
	if _, err := h.startContinuation(noEdge, "hold", `{"why":"x"}`); !errors.Is(err, store.ErrInvalid) || !strings.Contains(err.Error(), "no outgoing edge") {
		t.Errorf("port without edge: %v", err)
	}
	if _, err := h.startContinuation(s, "nope", `{}`); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("unknown port: %v", err)
	}
	if _, err := h.startContinuation(s, "out", `{}`); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("out on a node with declared ports: %v", err)
	}
	// The agent node is not in the stored version.
	gone := h.seedOriginal(seedOpts{yaml: "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: only, type: inbox, title: x}\nedges: []\n"})
	if _, err := h.startContinuation(gone, "ship", `{"body":"x"}`); !errors.Is(err, core.ErrUnknownRef) {
		t.Errorf("missing node: %v", err)
	}
	if _, err := h.eng.StartContinuation(context.Background(), engine.ContinuationRequest{OriginalAgentRunID: "01MISSING00000000000000000", ConversationID: "c", Port: "ship"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown agent run: %v", err)
	}
	test := h.seedOriginal(seedOpts{yaml: yaml, kind: store.KindTest})
	if _, err := h.startContinuation(test, "ship", `{"body":"x"}`); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("test run: %v", err)
	}
	chat := &store.AssistSession{
		TargetKind: store.AssistTargetGeneral, Runtime: "fake", DraftYAML: "id: x\n", DraftHash: strings.Repeat("a", 64), DraftUpdatedBy: store.DraftByUser,
	}
	if err := h.st.CreateAssistSession(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
	assist := &store.AgentRun{AssistSessionID: chat.ID, Runtime: "fake", Mode: store.ModeReadonly, Policy: store.PolicyReadonly, Phase: store.PhaseExecute, Status: store.AgentSucceeded}
	if err := h.st.CreateAgentRun(context.Background(), assist); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.StartContinuation(context.Background(), engine.ContinuationRequest{OriginalAgentRunID: assist.ID, ConversationID: "c", Port: "ship"}); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("an assist turn: %v", err)
	}
	if n := count(); n != 0 {
		t.Errorf("%d continuation runs were created by failed calls", n)
	}
}

const gatedFlow = `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: prep, type: command, lang: bash, script: cat}
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: "Review"
    ports:
      ship: {description: Ship it., schema: {type: object, required: [body], properties: {body: {type: string}}}}
      hold: {description: Hold., schema: {type: object, required: [why], properties: {why: {type: string}}}}
  - {id: gated, type: inbox, title: "gated {{.input.agent.output.body}}", requires_approval: true}
  - {id: confirm, type: approval, title: "Hold {{.input.agent.output.why}}?", description: "Hold the shipment", body: "why"}
  - {id: after, type: inbox, title: "after {{.input.approval.decision}}"}
edges:
  - {from: prep, to: work}
  - {from: work, port: ship, to: gated}
  - {from: work, port: hold, to: confirm}
  - {from: confirm, port: approved, to: after}
`

// Gates and approval nodes after the port work as in a normal run: they park the run and wait.
func TestContinuationHonoursApprovalsDownstream(t *testing.T) {
	h := newHarness(t)
	h.continuationHarness(gatedFlow)

	gate := h.seedOriginal(seedOpts{yaml: gatedFlow})
	id, err := h.startContinuation(gate, "ship", `{"body":"b"}`)
	if err != nil {
		t.Fatal(err)
	}
	h.waitRun(id, store.FlowRunAwaitingApproval)
	if h.nodeStatus(id, "gated") != store.NodeAwaitingApproval || len(h.inboxItems()) != 0 {
		t.Fatalf("a requires_approval node runs only once approved\n%s", h.dumpRun(id))
	}
	pending := h.approvals(store.ApprovalPending)
	if len(pending) != 1 || pending[0].Type != store.ApprovalRequest || pending[0].NodeRunID != h.node(id, "gated").ID {
		t.Fatalf("pending approvals: %+v", pending)
	}
	if _, err := h.eng.DecideApproval(context.Background(), pending[0].ID, core.ApprovalDecision{Approve: true, Principal: "tester"}); err != nil {
		t.Fatal(err)
	}
	h.waitRun(id, store.FlowRunSucceeded)
	if items := h.inboxItems(); len(items) != 1 || items[0].Title != "gated b" {
		t.Errorf("inbox after approving the gate: %+v", items)
	}

	ask := h.seedOriginal(seedOpts{yaml: gatedFlow})
	id2, err := h.startContinuation(ask, "hold", `{"why":"later"}`)
	if err != nil {
		t.Fatal(err)
	}
	h.waitRun(id2, store.FlowRunAwaitingApproval)
	pending = h.approvals(store.ApprovalPending)
	if len(pending) != 1 || pending[0].Type != store.ApprovalRequest || pending[0].Title != "Hold later?" {
		t.Fatalf("approval node did not ask: %+v", pending)
	}
	if _, err := h.eng.DecideApproval(context.Background(), pending[0].ID, core.ApprovalDecision{Approve: true, Principal: "tester"}); err != nil {
		t.Fatal(err)
	}
	h.waitRun(id2, store.FlowRunSucceeded)
	if !sameIDs(h.nodeIDs(id2), "prep", "work", "confirm", "after") {
		t.Errorf("node runs %v", h.nodeIDs(id2))
	}
}

// A dry-run original gives a dry-run continuation: its inbox items are tagged dry-run.
func TestContinuationInheritsDryRun(t *testing.T) {
	h := newHarness(t)
	yaml := strings.ReplaceAll(contFlow, "TITLE", "t")
	h.continuationHarness(yaml)
	s := h.seedOriginal(seedOpts{yaml: yaml, dryRun: true})
	id, err := h.startContinuation(s, "ship", `{"body":"b"}`)
	if err != nil {
		t.Fatal(err)
	}
	if fr := h.waitRun(id, store.FlowRunSucceeded); !fr.DryRun {
		t.Errorf("continuation of a dry run is not dry: %+v", fr)
	}
	if items := h.inboxItems(); len(items) != 1 || !items[0].DryRun {
		t.Errorf("inbox: %+v", items)
	}
}

// With the kill switch engaged the run is accepted and stays queued, with its seed recorded and nothing started.
func TestContinuationWaitsForTheKillSwitch(t *testing.T) {
	h := newHarness(t)
	yaml := strings.ReplaceAll(contFlow, "TITLE", "t")
	h.continuationHarness(yaml)
	s := h.seedOriginal(seedOpts{yaml: yaml})
	if _, err := h.eng.Pause(t.Context(), false, "tester"); err != nil {
		t.Fatal(err)
	}
	id, err := h.startContinuation(s, "ship", `{"body":"b"}`)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if fr := h.run(id); fr.Status != store.FlowRunQueued || !sameIDs(h.nodeIDs(id), "prep", "work") || len(h.inboxItems()) != 0 {
		t.Fatalf("while paused:\n%s", h.dumpRun(id))
	}
	if _, err := h.eng.Resume(t.Context(), "tester"); err != nil {
		t.Fatal(err)
	}
	h.waitRun(id, store.FlowRunSucceeded)
	if len(h.inboxItems()) != 1 {
		t.Errorf("inbox after resume: %+v", h.inboxItems())
	}
}

const fanFlow = `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: prep, type: command, lang: bash, script: cat}
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: "Review"
    ports:
      ship: {description: Ship it., schema: {type: object, required: [body], properties: {body: {type: string}}}}
      hold: {description: Hold., schema: {type: object, required: [why], properties: {why: {type: string}}}}
  - {id: left, type: inbox, title: "left {{.input.agent.output.body}}"}
  - {id: right, type: inbox, title: "right {{.input.agent.output.body}}"}
  - {id: join, type: command, lang: jq, script: '{joined: .agent.output.body}'}
  - {id: end, type: inbox, title: "end {{.input.joined}}"}
  - {id: merge, type: inbox, title: "merge {{.input.agent.output.body}}"}
  - {id: other, type: inbox, title: "other"}
edges:
  - {from: prep, to: work}
  - {from: left, to: merge}
  - {from: right, to: merge}
  - {from: work, port: ship, to: left}
  - {from: work, port: ship, to: right}
  - {from: work, port: ship, to: join}
  - {from: work, port: hold, to: other}
  - {from: join, to: end}
`

// Fan-out from the port starts every target; a node reached through other ports does not run.
func TestContinuationFansOutLikeAnyRun(t *testing.T) {
	h := newHarness(t)
	h.continuationHarness(fanFlow)
	s := h.seedOriginal(seedOpts{yaml: fanFlow})
	id, err := h.startContinuation(s, "ship", `{"body":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	h.waitRun(id, store.FlowRunSucceeded)
	got := map[string]int{}
	for _, n := range h.nodeRuns(id) {
		got[n.NodeID]++
	}
	if got["left"] != 1 || got["right"] != 1 || got["merge"] != 2 || got["join"] != 1 || got["end"] != 1 || got["other"] != 0 || got["work"] != 1 || got["prep"] != 1 {
		t.Errorf("node runs per node: %v\n%s", got, h.dumpRun(id))
	}
	titles := map[string]bool{}
	for _, it := range h.inboxItems() {
		titles[it.Title] = true
	}
	if len(titles) != 4 || !titles["left x"] || !titles["right x"] || !titles["end x"] || !titles["merge x"] {
		t.Errorf("inbox: %v", titles)
	}
}

const outFlow = `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: prep, type: command, lang: bash, script: cat}
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: "Review {{.input.id}}"
  - {id: after, type: inbox, title: "after {{.input.agent.final_message}} / {{.input.agent.note.title}}"}
edges:
  - {from: prep, to: work}
  - {from: work, to: after}
`

// A node without declared ports emits on out: the final message and note fill the emission like a real run's.
func TestContinuationEmitsOutOfANodeWithoutPorts(t *testing.T) {
	h := newHarness(t)
	h.continuationHarness(outFlow)
	s := h.seedOriginal(seedOpts{yaml: outFlow})
	id, err := h.startContinuation(s, "out", `{"final_message":"all good","note":{"title":"T","summary_md":"S"}}`)
	if err != nil {
		t.Fatal(err)
	}
	h.waitRun(id, store.FlowRunSucceeded)
	if items := h.inboxItems(); len(items) != 1 || items[0].Title != "after all good / T" {
		t.Errorf("inbox: %+v", items)
	}
	if _, err := h.startContinuation(s, "ship", `{}`); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("a node without ports has only out: %v", err)
	}
}

// ---- the emit tools, through a tools-hub session ----

type emitSession struct {
	h    *harness
	s    seeded
	sess *tools.Session
	tl   map[string]tools.Tool
}

func (h *harness) emitSession(s seeded, ports ...core.ForkPort) *emitSession {
	h.t.Helper()
	ctx := context.Background()
	chat := &store.AssistSession{
		TargetKind: store.AssistTargetGeneral, Runtime: "fake", DraftYAML: "id: x\n", DraftHash: strings.Repeat("a", 64), DraftUpdatedBy: store.DraftByUser,
	}
	if err := h.st.CreateAssistSession(ctx, chat); err != nil {
		h.t.Fatal(err)
	}
	turn := &store.AgentRun{
		AssistSessionID: chat.ID, Runtime: "fake", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated,
		Phase: store.PhaseExecute, Status: store.AgentRunning, Prompt: store.Inline("hi"),
	}
	if err := h.st.CreateAgentRun(ctx, turn); err != nil {
		h.t.Fatal(err)
	}
	fork := core.ForkNode{
		AgentRunID: s.agentRun, NodeRunID: s.work, FlowRunID: s.flowRun, FlowID: "f", NodeID: "work", Ports: ports,
	}
	extra, err := h.eng.AssistHost().EmitTools(ctx, fork, chat.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	sess, err := h.eng.ToolHub().Open(ctx, tools.RunSpec{
		Kind: tools.KindAssist, RunID: chat.ID, Info: func() tools.RunInfo { return tools.RunInfo{AgentRunID: turn.ID, AssistSessionID: chat.ID} },
		ApprovalTimeout: time.Minute, ExtraTools: extra,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(sess.Close)
	es := &emitSession{h: h, s: s, sess: sess, tl: map[string]tools.Tool{}}
	for _, t := range extra {
		es.tl[t.Name] = t
	}
	return es
}

type callResult struct {
	res tools.Result
	err error
}

func (es *emitSession) callAsync(name, args string) <-chan callResult {
	ch := make(chan callResult, 1)
	go func() {
		res, err := es.sess.Call(context.Background(), name, json.RawMessage(args))
		ch <- callResult{res, err}
	}()
	return ch
}

func (es *emitSession) continuationRuns() []store.FlowRun {
	es.h.t.Helper()
	page, err := es.h.st.ListFlowRuns(context.Background(), store.FlowRunFilter{ContinuationOf: es.s.flowRun, ListOpts: store.ListOpts{Oldest: true, Limit: 50}})
	if err != nil {
		es.h.t.Fatal(err)
	}
	return page.Items
}

func (es *emitSession) waitToolApproval() store.Approval {
	es.h.t.Helper()
	var got store.Approval
	es.h.eventually("a pending tool_call approval", func() bool {
		for _, a := range es.h.approvals(store.ApprovalPending) {
			if a.Type == store.ApprovalToolCall {
				got = a
				return true
			}
		}
		return false
	})
	return got
}

func (es *emitSession) decide(a store.Approval, approve bool) {
	es.h.t.Helper()
	if _, err := es.h.eng.DecideApproval(context.Background(), a.ID, core.ApprovalDecision{Approve: approve, Principal: "tester", Comment: map[bool]string{true: "", false: "not now"}[approve]}); err != nil {
		es.h.t.Fatal(err)
	}
}

func (es *emitSession) wait(ch <-chan callResult) callResult {
	es.h.t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(waitFor):
		es.h.t.Fatal("the emit call did not return")
		return callResult{}
	}
}

var shipHold = []core.ForkPort{{Name: "ship", Description: "Ship the result."}, {Name: "hold", Description: "Keep it for later."}}

// An emit call is checked first (invalid: refused without asking), then held for approval; only an approved call starts
// a run, one per call, and a denied call starts none.
func TestEmitToolsStartRunsOnlyWhenApproved(t *testing.T) {
	h := newHarness(t)
	yaml := strings.ReplaceAll(contFlow, "TITLE", "t")
	h.continuationHarness(yaml)
	s := h.seedOriginal(seedOpts{yaml: yaml})
	es := h.emitSession(s, shipHold...)

	if len(es.tl) != 2 || es.tl["emit_ship"].Name == "" || es.tl["emit_hold"].Name == "" {
		t.Fatalf("emit tools = %v, want emit_ship and emit_hold", es.tl)
	}
	desc := es.tl["emit_ship"].Description
	for _, want := range []string{"Ship the result.", `Leads to: inbox note "t ... id=... event=... kind=..."`, "Call this ONLY when the user explicitly asked you to send or emit the result to the flow.", "starts a new flow run after the user approves it", "does not change the original run"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q:\n%s", want, desc)
		}
	}

	// Invalid payload: refused by policy with the path and reason, no approval, no run.
	r := es.wait(es.callAsync("emit_ship", `{"body": 7}`))
	if r.err == nil || !strings.Contains(r.err.Error(), "denied by policy") || !strings.Contains(r.err.Error(), "/body: ") || !strings.Contains(r.err.Error(), "call emit_ship again") {
		t.Fatalf("invalid call: %v", r.err)
	}
	r = es.wait(es.callAsync("emit_ship", `{}`))
	if r.err == nil || !strings.Contains(r.err.Error(), "body") {
		t.Fatalf("call without the required field: %v", r.err)
	}
	if pend := h.approvals(store.ApprovalPending); len(pend) != 0 || len(es.continuationRuns()) != 0 {
		t.Fatalf("an invalid call raised %d approvals and %d runs", len(pend), len(es.continuationRuns()))
	}

	// A valid call waits for the user. Nothing runs before the decision.
	ch := es.callAsync("emit_ship", `{"body":"hello"}`)
	ap := es.waitToolApproval()
	time.Sleep(100 * time.Millisecond)
	if len(es.continuationRuns()) != 0 {
		t.Fatal("a run was started before the approval")
	}
	pr, err := h.st.GetPermissionRequest(context.Background(), ap.PermissionRequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"flow f: node work -> port ship", `Leads to: inbox note "t ... id=... event=... kind=..."`, `"body": "hello"`,
		"starts a new flow run of f (kind continuation)", "The original run " + s.flowRun + " stays unchanged.",
	} {
		if !strings.Contains(pr.Reason, want) {
			t.Errorf("approval reason lacks %q:\n%s", want, pr.Reason)
		}
	}
	if pr.Title != "hal tool: emit_ship" || !strings.Contains(h.text(pr.RawInput), `"body":"hello"`) {
		t.Errorf("permission request: %+v", pr)
	}
	es.decide(ap, true)
	r = es.wait(ch)
	if r.err != nil {
		t.Fatal(r.err)
	}
	runs := es.continuationRuns()
	if len(runs) != 1 {
		t.Fatalf("%d continuation runs after one approval, want 1", len(runs))
	}
	if !strings.HasPrefix(r.res.Text, "ok; approved; started flow run "+runs[0].ID+" (kind continuation)") {
		t.Errorf("result text: %q", r.res.Text)
	}
	if c := runs[0].Continuation; c == nil || c.ApprovalID != ap.ID || c.NodeID != "work" || c.Port != "ship" || c.OfRunID != s.flowRun || c.ConversationID == "" {
		t.Errorf("link of the started run: %+v (approval %s)", c, ap.ID)
	}
	h.waitRun(runs[0].ID, store.FlowRunSucceeded)

	// Denied: a tool error, no new run.
	ch = es.callAsync("emit_hold", `{"why":"wait"}`)
	ap = es.waitToolApproval()
	es.decide(ap, false)
	r = es.wait(ch)
	var denied *tools.DeniedError
	if !errors.As(r.err, &denied) || denied.Comment != "not now" {
		t.Fatalf("denied call: %v", r.err)
	}
	if n := len(es.continuationRuns()); n != 1 {
		t.Fatalf("%d runs after a denial, want 1", n)
	}

	// A second approved call starts a second run.
	ch = es.callAsync("emit_ship", `{"body":"again"}`)
	es.decide(es.waitToolApproval(), true)
	if r = es.wait(ch); r.err != nil {
		t.Fatal(r.err)
	}
	if runs = es.continuationRuns(); len(runs) != 2 || runs[0].ID == runs[1].ID {
		t.Fatalf("runs after two approvals: %+v", runs)
	}
	h.waitRun(runs[1].ID, store.FlowRunSucceeded)
}

// emit_out of a node without ports takes a final message, and a note only when the node's output is a note.
func TestEmitOutSchemaFollowsTheNodeOutput(t *testing.T) {
	h := newHarness(t)
	h.continuationHarness(outFlow)
	s := h.seedOriginal(seedOpts{yaml: outFlow})
	es := h.emitSession(s, core.ForkPort{Name: "out", Description: ""})
	props := func(name string) map[string]any {
		return es.tl[name].InputSchema["properties"].(map[string]any)
	}
	if p := props("emit_out"); p["final_message"] == nil || p["note"] == nil {
		t.Errorf("note-mode node: emit_out properties %v, want final_message and note", p)
	}
	if r := es.wait(es.callAsync("emit_out", `{"note":{"title":"t","summary_md":"s"}}`)); r.err == nil || !strings.Contains(r.err.Error(), "final_message") {
		t.Errorf("emit_out without final_message: %v", r.err)
	}
	ch := es.callAsync("emit_out", `{"final_message":"done","note":{"title":"Title","summary_md":"s"}}`)
	es.decide(es.waitToolApproval(), true)
	if r := es.wait(ch); r.err != nil || !strings.Contains(r.res.Text, "started flow run") {
		t.Fatalf("emit_out: %+v", r)
	}
	runs := es.continuationRuns()
	if len(runs) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	h.waitRun(runs[0].ID, store.FlowRunSucceeded)
	if items := h.inboxItems(); len(items) != 1 || items[0].Title != "after done / Title" {
		t.Errorf("inbox: %+v", items)
	}

	// With output: none the note is not offered.
	none := strings.Replace(outFlow, "    mode: readonly\n", "    mode: readonly\n    output: none\n", 1)
	h2 := newHarness(t)
	h2.continuationHarness(none)
	s2 := h2.seedOriginal(seedOpts{yaml: none})
	es2 := h2.emitSession(s2, core.ForkPort{Name: "out"})
	if p := es2.tl["emit_out"].InputSchema["properties"].(map[string]any); p["final_message"] == nil || p["note"] != nil {
		t.Errorf("output: none: emit_out properties %v, want only final_message", p)
	}
}

// A chat whose node has no emit targets gets no emit tools, and one asking for a port the flow lacks gets an error.
func TestEmitToolsForUnknownPortsFail(t *testing.T) {
	h := newHarness(t)
	yaml := strings.ReplaceAll(contFlow, "TITLE", "t")
	h.continuationHarness(yaml)
	s := h.seedOriginal(seedOpts{yaml: yaml})
	fork := core.ForkNode{AgentRunID: s.agentRun, NodeRunID: s.work, FlowRunID: s.flowRun, FlowID: "f", NodeID: "work"}
	if got, err := h.eng.AssistHost().EmitTools(context.Background(), fork, "c"); err != nil || len(got) != 0 {
		t.Errorf("no ports: %v %v", got, err)
	}
	fork.Ports = []core.ForkPort{{Name: "bogus"}}
	if _, err := h.eng.AssistHost().EmitTools(context.Background(), fork, "c"); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("unknown port: %v", err)
	}
}
