package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// ForkNode resolves the agent node run that a conversation forked from agentRunID continues: the run, its node run, its
// flow run and the flow version that run executed (by flow_hash, not the current file), and from those the node's
// settings. store.ErrNotFound for an unknown run, a turn of an assist session (it has no node) and a run whose flow
// version no longer exists.
func (h assistHost) ForkNode(ctx context.Context, agentRunID string) (core.ForkNode, error) {
	return h.e.forkNode(ctx, agentRunID)
}

func (e *Engine) forkNode(ctx context.Context, agentRunID string) (core.ForkNode, error) {
	ar, err := e.st.GetAgentRun(ctx, agentRunID)
	if err != nil {
		return core.ForkNode{}, err
	}
	if ar.NodeRunID == "" {
		return core.ForkNode{}, fmt.Errorf("%w: agent run %s is a turn of a chat, not a flow node", store.ErrNotFound, agentRunID)
	}
	nr, err := e.st.GetNodeRun(ctx, ar.NodeRunID)
	if err != nil {
		return core.ForkNode{}, err
	}
	fr, err := e.st.GetFlowRun(ctx, nr.FlowRunID)
	if err != nil {
		return core.ForkNode{}, err
	}
	flow, err := e.flowFor(fr)
	if err != nil {
		return core.ForkNode{}, fmt.Errorf("%w: %w", store.ErrNotFound, err)
	}
	node := flow.NodeByID(nr.NodeID)
	if node == nil || node.Type != spec.NodeAgent {
		return core.ForkNode{}, fmt.Errorf("%w: node %q of flow %s version %s", store.ErrNotFound, nr.NodeID, fr.FlowID, fr.FlowHash)
	}
	f := core.ForkNode{
		AgentRunID: ar.ID, NodeRunID: nr.ID, FlowRunID: fr.ID, FlowID: fr.FlowID, NodeID: nr.NodeID, FlowHash: fr.FlowHash,
		Runtime: node.Runtime, Model: node.Model, Cwd: ar.Cwd, SessionID: ar.SessionID, LoadSession: loadSessionOf(ar.Capabilities),
		Sandbox: node.Sandbox, Image: node.Image, Repo: node.Repo, Ref: node.Ref,
		ToolIDs: toolIDs(node.Tools), ToolApprove: toolApprovals(node.Tools), Ports: forkPorts(flow, node),
	}
	if f.Runtime == "" {
		f.Runtime = ar.Runtime
	}
	if f.Model == "" {
		f.Model = ar.Model
	}
	if in := e.readContent(nr.Stdin); json.Valid([]byte(in)) {
		f.Input = json.RawMessage(in)
	}
	return f, nil
}

// loadSessionOf reads loadSession from the agentCapabilities JSON an agent run recorded.
func loadSessionOf(caps json.RawMessage) bool {
	var c struct {
		LoadSession bool `json:"loadSession"`
	}
	return json.Unmarshal(caps, &c) == nil && c.LoadSession
}

// forkPorts lists the targets a conversation forked from node may emit to: the declared ports that have an outgoing
// edge (the ones whose emit_<port> tool the run had), or a single port `out` for a node without declared ports. Next
// summarizes every node a port leads to with the helper of the Outputs section.
func forkPorts(flow *spec.Flow, node *spec.Node) []core.ForkPort {
	next := func(port string) []string {
		var out []string
		for _, e := range flow.Edges {
			if e.From != node.ID || (e.Port != port && (e.Port != "" || port != spec.PortOut)) {
				continue
			}
			if to := flow.NodeByID(e.To); to != nil {
				out = append(out, spec.LeadsTo(to))
			}
		}
		return out
	}
	if node.Ports.IsZero() {
		return []core.ForkPort{{Name: spec.PortOut, Next: next(spec.PortOut)}}
	}
	var out []core.ForkPort
	for _, t := range flow.EmitTools(node) {
		out = append(out, core.ForkPort{Name: t.Port, Description: t.Description, Next: next(t.Port)})
	}
	return out
}
