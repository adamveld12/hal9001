package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// ContinuationRequest asks for a flow run that continues from an agent node of an earlier run, with a result the user
// approved in a chat that continued that run.
type ContinuationRequest struct {
	// OriginalAgentRunID is the agent run of the flow node the chat continues.
	OriginalAgentRunID string
	// ConversationID is the assist session of the chat (recorded on the run).
	ConversationID string
	// Port is the port of the agent node to emit on: a declared port, or "out" for a node without declared ports.
	Port string
	// Payload is the validated arguments of the emit tool. For a declared port they are the emission's output. For
	// "out" they are {"final_message": string, "note": <note>}: the final message of the agent and, for a node whose
	// output is a note, its note (it becomes the emission's note and output).
	Payload json.RawMessage
	// ApprovalID is the tool_call approval the user granted (recorded on the run; may be empty).
	ApprovalID string
}

// StartContinuation starts a flow run of kind continuation. It is the engine side of an approved emit_<port> call of a
// chat that continues an agent run.
//
// The run executes the flow version the original run executed (stored by hash, so node ids and edges line up); the
// prompts, tools, sensors and runtimes the downstream nodes use come from the current workspace, as for any run. It
// is seeded from the original run: the node runs on the path from the entry node to the agent node are copied
// (their inputs and outputs are what the original produced), the agent node is recorded done on the emitted port with
// a payload shaped like a real emission, and only the nodes downstream of that port are queued. The run keeps the
// original run's event, so {{.event}} resolves as before, and its dry-run flag. The original run and its node runs are
// not changed.
//
// The run is queued like any other: it waits for the kill switch to be released and for a free slot of the flow's
// max_concurrent. Errors: store.ErrNotFound (unknown agent run, or the flow version is gone),
// core.ErrInvalidState (the agent run is not a run of a flow node, or of a test run), core.ErrUnknownRef (the node is
// not in the version) and store.ErrInvalid (the port does not exist or has no edge).
func (e *Engine) StartContinuation(ctx context.Context, req ContinuationRequest) (string, error) {
	if req.OriginalAgentRunID == "" || req.ConversationID == "" || req.Port == "" {
		return "", fmt.Errorf("%w: agent run, conversation and port are required", store.ErrInvalid)
	}
	ar, err := e.st.GetAgentRun(ctx, req.OriginalAgentRunID)
	if err != nil {
		return "", err
	}
	if ar.NodeRunID == "" {
		return "", fmt.Errorf("%w: agent run %s is not a run of a flow node", core.ErrInvalidState, ar.ID)
	}
	nr, err := e.st.GetNodeRun(ctx, ar.NodeRunID)
	if err != nil {
		return "", err
	}
	orig, err := e.st.GetFlowRun(ctx, nr.FlowRunID)
	if err != nil {
		return "", err
	}
	if orig.Kind == store.KindTest {
		return "", fmt.Errorf("%w: flow run %s is a test run", core.ErrInvalidState, orig.ID)
	}
	flow, err := e.flowFor(orig)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf("%w: flow version %s of flow run %s is no longer stored", store.ErrNotFound, orig.FlowHash, orig.ID)
		}
		return "", fmt.Errorf("%w: %w", core.ErrInvalidState, err)
	}
	node := flow.NodeByID(nr.NodeID)
	if node == nil || node.Type != spec.NodeAgent {
		return "", fmt.Errorf("%w: agent node %q in flow %q (version %s)", core.ErrUnknownRef, nr.NodeID, flow.ID, orig.FlowHash)
	}
	if err := checkEmitPort(flow, node, req.Port, orig.FlowHash); err != nil {
		return "", err
	}

	snap := e.snapshot()
	input := strings.TrimSpace(e.readContent(nr.Stdin))
	payload, err := emissionPayload(snap, input, ar.ID, req.Port, req.Payload, node.Ports.IsZero())
	if err != nil {
		return "", err
	}

	chain, err := e.ancestorChain(ctx, nr)
	if err != nil {
		return "", err
	}
	var seeds []*store.NodeRun
	parent := ""
	for _, old := range chain[:len(chain)-1] {
		c := copyNodeRun(old, orig.ID)
		c.ID, c.ParentNodeRunID = e.st.NewID(), parent
		seeds = append(seeds, c)
		parent = c.ID
	}
	now := time.Now().UTC()
	emitter := &store.NodeRun{
		ID: e.st.NewID(), NodeID: nr.NodeID, NodeType: nr.NodeType, ParentNodeRunID: parent,
		Status: store.NodeDone, Decision: store.DecisionEmitted, Reason: "emitted from conversation " + req.ConversationID,
		Ports: []string{req.Port}, OutputCount: 1, Stdin: nr.Stdin, Stdout: store.Inline(string(payload)),
		QueuedAt: now, StartedAt: &now, EndedAt: &now,
	}
	zero := int64(0)
	emitter.DurationMS = &zero
	seeds = append(seeds, emitter)

	dry := orig.DryRun
	if fd := snap.flows[orig.FlowID]; fd != nil && fd.flow.DryRun {
		dry = true // the flow was switched to dry_run since the original run: honour it
	}
	fr := &store.FlowRun{
		FlowID: orig.FlowID, FlowHash: orig.FlowHash, EventID: orig.EventID, DryRun: dry,
		Continuation: &store.Continuation{
			OfRunID: orig.ID, ConversationID: req.ConversationID, NodeID: nr.NodeID, Port: req.Port, ApprovalID: req.ApprovalID,
		},
	}
	if err := e.st.CreateContinuationRun(ctx, fr, seeds); err != nil {
		return "", err
	}
	e.wake()
	return fr.ID, nil
}

// checkEmitPort reports an error unless port of node is an emit target with an outgoing edge in flow (version hash): a
// declared port, or "out" for a node without declared ports.
func checkEmitPort(flow *spec.Flow, node *spec.Node, port, hash string) error {
	switch {
	case node.Ports.IsZero() && port != spec.PortOut:
		return fmt.Errorf("%w: node %q declares no ports, so it can only emit on %q, not %q", store.ErrInvalid, node.ID, spec.PortOut, port)
	case !node.Ports.IsZero() && !declaresPort(node, port):
		return fmt.Errorf("%w: node %q declares no port %q", store.ErrInvalid, node.ID, port)
	case !hasEdge(flow, node.ID, port):
		return fmt.Errorf("%w: port %q of node %q has no outgoing edge in flow version %s, so there is nothing to run", store.ErrInvalid, port, node.ID, hash)
	}
	return nil
}

func declaresPort(node *spec.Node, port string) bool {
	for _, d := range node.Ports.Decls {
		if d.Name == port {
			return true
		}
	}
	return false
}

// ancestorChain returns the node runs from the entry node run down to nr, following parent links.
func (e *Engine) ancestorChain(ctx context.Context, nr *store.NodeRun) ([]*store.NodeRun, error) {
	chain := []*store.NodeRun{nr}
	for cur := nr; cur.ParentNodeRunID != ""; {
		p, err := e.st.GetNodeRun(ctx, cur.ParentNodeRunID)
		if err != nil {
			return nil, fmt.Errorf("load ancestor of node run %s: %w", cur.ID, err)
		}
		chain = append([]*store.NodeRun{p}, chain...)
		cur = p
	}
	return chain, nil
}

// copyNodeRun copies a node run of the original run into the continuation run: same node, outcome, input and output
// (content is shared by reference), marked as reused. The caller sets the ID and the parent.
func copyNodeRun(old *store.NodeRun, origRunID string) *store.NodeRun {
	c := *old
	c.ID, c.ParentNodeRunID = "", ""
	c.Reason = "reused from flow run " + origRunID
	if old.Reason != "" {
		c.Reason += ": " + old.Reason
	}
	return &c
}

// emissionPayload is the payload of an emission shaped like a real one (agentPayload): {"input": <node input>, "agent":
// {run_id, status, port, output, final_message, note, proposal, comment}}. For a declared port output is the emit
// tool's arguments. For the port of a node without declared ports (noPorts) the arguments are
// {"final_message", "note"}; a note becomes both the note and the output, as with a real submit_output call.
func emissionPayload(snap *snapshot, input, agentRunID, port string, args json.RawMessage, noPorts bool) (json.RawMessage, error) {
	var output any
	var note *agent.Note
	finalMessage := ""
	if noPorts {
		var a struct {
			FinalMessage string          `json:"final_message"`
			Note         json.RawMessage `json:"note"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("%w: emit payload: %w", store.ErrInvalid, err)
		}
		finalMessage = a.FinalMessage
		if len(a.Note) > 0 && string(a.Note) != "null" {
			n, err := agent.NoteFromJSON(a.Note)
			if err != nil {
				return nil, fmt.Errorf("%w: note: %w", store.ErrInvalid, err)
			}
			note, output = &n, a.Note
		}
	} else {
		output = outputValue(args)
	}
	b, err := json.Marshal(map[string]any{
		"input": rawOrString([]byte(input)),
		"agent": map[string]any{
			"run_id": agentRunID, "status": string(agent.StatusSucceeded), "port": port, "final_message": finalMessage,
			"note": note, "output": output, "proposal": "", "comment": "",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode emission: %w", err)
	}
	return snap.red.Bytes(b), nil
}

// beginContinuation starts a claimed continuation run: its seed already holds the recorded emission of the agent
// node, so only the node runs behind the emitted port are queued, each with the emission as input.
func (e *Engine) beginContinuation(fr *store.FlowRun, flow *spec.Flow) {
	c := fr.Continuation
	if c == nil {
		e.endFlowRun(fr, store.FlowRunFailed, "continuation run without its link")
		return
	}
	nodes, err := e.nodeRuns(fr.ID)
	if err != nil {
		e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("list seed node runs: %v", err))
		return
	}
	var emitter *store.NodeRun
	for i := range nodes {
		if n := &nodes[i]; n.NodeID == c.NodeID && n.Status == store.NodeDone && n.Decision == store.DecisionEmitted {
			emitter = n // the emission is the newest seed
		}
	}
	if emitter == nil {
		e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("node %s has no recorded emission", c.NodeID))
		return
	}
	full, err := e.st.GetNodeRun(e.dbCtx, emitter.ID)
	if err != nil {
		e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("load emission: %v", err))
		return
	}
	next, err := e.successors(full, flow, []runner.Output{{Port: c.Port, Payload: json.RawMessage(e.readContent(full.Stdout))}})
	if err != nil || len(next) == 0 {
		msg := fmt.Sprintf("port %s of node %s has no outgoing edge", c.Port, c.NodeID)
		if err != nil {
			msg = err.Error()
		}
		e.endFlowRun(fr, store.FlowRunFailed, msg)
		return
	}
	if err := e.st.CreateNodeRuns(e.dbCtx, next); err != nil {
		e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("queue node runs: %v", err))
		return
	}
	e.wake()
}
