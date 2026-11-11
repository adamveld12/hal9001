package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// assistTestAgentSkip is the reason recorded on agent nodes in a flow test.
const assistTestAgentSkip = "assist test: agent nodes are not run"

// previewLimit bounds the stdout and stderr a flow test result carries per node.
const previewLimit = 4096

// assistTest is the in-memory side of a flow test run: what the nodes emitted and which edges were taken.
// Outputs are not stored on node runs (only a command's raw stdout is), so the test captures them as they are
// routed.
type assistTest struct {
	mu      sync.Mutex
	outputs map[string][]runner.Output // node run id -> emitted outputs
	would   map[string]*core.AssistInboxPreview
	edges   []core.AssistFlowTestEdge
}

func (e *Engine) assistTestOf(flowRunID string) *assistTest {
	if v, ok := e.assistTests.Load(flowRunID); ok {
		return v.(*assistTest)
	}
	return nil
}

// routes reports whether the outputs of a node of this run feed downstream nodes: live and replay runs do;
// "run node on sample" test runs do not; a flow test (at != nil) does.
func routes(fr *store.FlowRun, at *assistTest) bool {
	return fr.Kind != store.KindTest || at != nil
}

func (a *assistTest) recordOutputs(nodeRunID string, outs []runner.Output) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.outputs[nodeRunID] = append([]runner.Output(nil), outs...)
}

func (a *assistTest) recordEdge(from, to, port string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	edge := core.AssistFlowTestEdge{From: from, To: to, Port: port}
	for _, x := range a.edges {
		if x == edge {
			return
		}
	}
	a.edges = append(a.edges, edge)
}

func (a *assistTest) recordInbox(nodeRunID string, it *store.InboxItem) {
	p := &core.AssistInboxPreview{
		Title: it.Title, Priority: string(it.Priority), SummaryMD: it.SummaryMD, SuggestedReply: it.SuggestedReply,
		Links: []core.AssistInboxPreviewLink{},
	}
	for _, l := range it.Links {
		p.Links = append(p.Links, core.AssistInboxPreviewLink{Label: l.Label, URL: l.URL})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.would[nodeRunID] = p
}

func inboxReason(preview bool, it *store.InboxItem) string {
	if preview {
		return "would create inbox item: " + truncate(it.Title, 120)
	}
	return "inbox item " + it.ID
}

// TestFlow runs a flow draft once on one input as a kind=test flow run with dry_run forced, following the
// edges. Command and transform nodes execute for real (dry run skips side_effect commands); agent nodes are
// skipped and end their branch; inbox nodes record the item they would create and create none. It waits for the
// run to finish. req.Flow must already be validated by the caller.
func (e *Engine) TestFlow(ctx context.Context, req core.FlowTestRequest) (core.AssistFlowTestResult, error) {
	var zero core.AssistFlowTestResult
	flow := req.Flow.Clone()
	flow.ApplyDefaults()
	if err := validateSimulatedEmits(&flow, req.SimulateEmit); err != nil {
		return zero, err
	}
	raw, err := spec.MarshalFlow(flow)
	if err != nil {
		return zero, fmt.Errorf("engine: marshal flow: %w", err)
	}
	input := []byte(strings.TrimSpace(string(req.Input)))
	if req.EventID != "" {
		ev, err := e.st.GetEvent(ctx, req.EventID)
		if err != nil {
			return zero, err
		}
		if len(input) == 0 {
			if input, err = e.eventPayload(ev); err != nil {
				return zero, err
			}
		}
	}
	if len(input) == 0 {
		return zero, fmt.Errorf("%w: a test input is required", store.ErrInvalid)
	}

	now := time.Now().UTC()
	fr := &store.FlowRun{
		FlowID: flow.ID, FlowHash: spec.Hash(raw), FlowYAML: string(raw), EventID: req.EventID,
		Kind: store.KindTest, DryRun: true, Status: store.FlowRunRunning, StartedAt: &now,
	}
	if err := e.st.CreateFlowRun(ctx, fr); err != nil {
		return zero, err
	}
	at := &assistTest{outputs: map[string][]runner.Output{}, would: map[string]*core.AssistInboxPreview{}}
	e.assistTests.Store(fr.ID, at)
	defer e.assistTests.Delete(fr.ID)
	e.simulations.Store(fr.ID, simulatedDecision(req.SimulateApproval))
	defer e.simulations.Delete(fr.ID)
	if len(req.SimulateEmit) > 0 {
		e.simEmits.Store(fr.ID, req.SimulateEmit)
		defer e.simEmits.Delete(fr.ID)
	}

	entries := flow.EntryNodes()
	if len(entries) == 0 {
		e.endFlowRun(fr, store.FlowRunSucceeded, "")
	} else {
		// One transaction: the dispatcher may already run the first entry node
		// while the next is being queued, and a run whose queued nodes have all
		// finished is over.
		first := make([]*store.NodeRun, len(entries))
		for i, id := range entries {
			first[i] = &store.NodeRun{FlowRunID: fr.ID, NodeID: id, NodeType: nodeRunType(flow.NodeByID(id)), Stdin: store.Inline(string(input))}
		}
		if err := e.st.CreateNodeRuns(ctx, first); err != nil {
			e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("queue entry nodes: %v", err))
		}
	}
	e.wake()

	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		cur, err := e.st.GetFlowRun(e.dbCtx, fr.ID)
		if err != nil {
			return zero, err
		}
		if cur.Status.Terminal() {
			fr = cur
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return zero, context.Cause(ctx)
		}
	}
	switch fr.Status {
	case store.FlowRunKilled:
		return zero, errors.New("engine: the test was cancelled")
	case store.FlowRunInterrupted:
		return zero, errors.New("engine: the daemon stopped during the test")
	}

	slim, err := e.nodeRuns(fr.ID)
	if err != nil {
		return zero, err
	}
	out := core.AssistFlowTestResult{
		Kind: "flow", FlowRunID: fr.ID, Status: string(fr.Status), Input: input,
		Nodes: make([]core.AssistFlowTestNode, 0, len(slim)),
	}
	at.mu.Lock()
	defer at.mu.Unlock()
	for _, s := range slim {
		nr, err := e.st.GetNodeRun(e.dbCtx, s.ID)
		if err != nil {
			return zero, err
		}
		n := core.AssistFlowTestNode{
			NodeID: nr.NodeID, NodeRunID: nr.ID, Status: string(nr.Status),
			Decision: strPtr(string(nr.Decision)), Reason: strPtr(nr.Reason), Error: strPtr(nr.Error),
			Ports: nr.Ports, Outputs: []core.AssistFlowTestOutput{}, WouldCreate: at.would[nr.ID],
			Stdout: strPtr(truncate(e.readContent(nr.Stdout), previewLimit)), Stderr: strPtr(truncate(e.readContent(nr.Stderr), previewLimit)),
			ExitCode: nr.ExitCode, DurationMS: nr.DurationMS, Simulated: strings.HasPrefix(nr.Reason, simulatedReasonPrefix),
		}
		if n.Ports == nil {
			n.Ports = []string{}
		}
		for _, o := range at.outputs[nr.ID] {
			n.Outputs = append(n.Outputs, core.AssistFlowTestOutput{Port: o.Port, Payload: o.Payload})
		}
		out.Nodes = append(out.Nodes, n)
	}
	out.EdgesTaken = append([]core.AssistFlowTestEdge{}, at.edges...)
	return out, nil
}

// simulatedDecision defaults the simulated approval decision to approved.
func simulatedDecision(s string) string {
	if s == SimulateDenied {
		return SimulateDenied
	}
	return SimulateApproved
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
