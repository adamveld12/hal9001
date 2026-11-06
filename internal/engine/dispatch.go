package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// dispatchLoop turns queued rows into running work: queued flow runs become
// running flow runs with their entry node runs queued, and queued node runs
// are claimed under the concurrency caps and executed. Nothing starts while
// the kill switch is engaged.
func (e *Engine) dispatchLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		e.dispatch()
		select {
		case <-ctx.Done():
			return
		case <-e.kick:
		case <-t.C:
		}
	}
}

func (e *Engine) dispatch() {
	if e.isPaused() || e.stopping.Load() {
		return
	}
	for {
		fr, err := e.st.ClaimNextFlowRun(e.dbCtx, e.maxPerFlow)
		if err != nil {
			e.log.Error("engine: claim flow run", "err", err)
			break
		}
		if fr == nil {
			break
		}
		e.beginFlowRun(fr)
	}
	for !e.isPaused() {
		cfg := e.snapshot().cfg
		nr, err := e.st.ClaimNextNodeRun(e.dbCtx, store.NodeLimits{
			store.NodeCommand: cfg.MaxCommandRuns,
			store.NodeAgent:   cfg.MaxAgentRuns,
			store.NodeInbox:   store.Unlimited,
		})
		if err != nil {
			e.log.Error("engine: claim node run", "err", err)
			return
		}
		if nr == nil {
			return
		}
		if !e.spawn(func(ctx context.Context) { e.execNode(ctx, nr) }) {
			nr.Status, nr.StartedAt = store.NodeQueued, nil
			if err := e.st.UpdateNodeRun(e.dbCtx, nr); err != nil {
				e.log.Error("engine: requeue node run", "node_run", nr.ID, "err", err)
			}
			return
		}
	}
}

// flowFor returns the flow definition a flow run executes: the snapshot stored
// with its version, so a run always follows the YAML it started with.
func (e *Engine) flowFor(fr *store.FlowRun) (*spec.Flow, error) {
	key := fr.FlowID + "@" + fr.FlowHash
	if f, ok := e.flowCache.Load(key); ok {
		return f.(*spec.Flow), nil
	}
	v, err := e.st.GetFlowVersion(e.dbCtx, fr.FlowID, fr.FlowHash)
	if err != nil {
		return nil, fmt.Errorf("load flow version %s: %w", key, err)
	}
	f, ds := spec.ParseFlow([]byte(v.YAML))
	if f == nil {
		return nil, fmt.Errorf("flow version %s does not parse: %v", key, ds)
	}
	e.flowCache.Store(key, f)
	return f, nil
}

// beginFlowRun queues a node run for every entry node of a freshly claimed
// flow run, each with the event as input.
func (e *Engine) beginFlowRun(fr *store.FlowRun) {
	flow, err := e.flowFor(fr)
	if err != nil {
		e.endFlowRun(fr, store.FlowRunFailed, err.Error())
		return
	}
	var payload string
	if fr.EventID != "" {
		ev, err := e.st.GetEvent(e.dbCtx, fr.EventID)
		if err != nil {
			e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("load event %s: %v", fr.EventID, err))
			return
		}
		b, err := e.eventPayload(ev)
		if err != nil {
			e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("read event %s: %v", fr.EventID, err))
			return
		}
		payload = string(b)
	}
	entries := flow.EntryNodes()
	if len(entries) == 0 {
		e.endFlowRun(fr, store.FlowRunSucceeded, "")
		return
	}
	for _, id := range entries {
		n := flow.NodeByID(id)
		nr := &store.NodeRun{FlowRunID: fr.ID, NodeID: id, NodeType: nodeRunType(n), Stdin: store.Inline(payload)}
		if err := e.st.CreateNodeRun(e.dbCtx, nr); err != nil {
			e.endFlowRun(fr, store.FlowRunFailed, fmt.Sprintf("queue node %s: %v", id, err))
			return
		}
	}
	e.wake()
}

func (e *Engine) endFlowRun(fr *store.FlowRun, status store.FlowRunStatus, msg string) {
	now := time.Now().UTC()
	fr.Status, fr.Error, fr.EndedAt = status, msg, &now
	if fr.StartedAt == nil {
		fr.StartedAt = &now
	}
	if err := e.st.UpdateFlowRun(e.dbCtx, fr); err != nil {
		e.log.Error("engine: end flow run", "flow_run", fr.ID, "err", err)
	}
}

// reconcileFlowRun recomputes a flow run's status from its node runs (§8):
// running while any node is queued or running, awaiting_approval while only
// approvals are outstanding, and otherwise terminal: killed if any node was
// killed, else interrupted, else failed if any node failed without an error
// route, else succeeded. Nodes still queued behind a killed or interrupted
// one are skipped, so the run ends instead of waiting for work that was cut off.
func (e *Engine) reconcileFlowRun(id string) {
	unlock := e.locks.lock("flowrun:" + id)
	defer unlock()
	fr, err := e.st.GetFlowRun(e.dbCtx, id)
	if err != nil {
		e.log.Error("engine: reconcile flow run", "flow_run", id, "err", err)
		return
	}
	if fr.Status.Terminal() {
		return
	}
	nodes, err := e.nodeRuns(id)
	if err != nil {
		e.log.Error("engine: list node runs", "flow_run", id, "err", err)
		return
	}
	var cut, failed *store.NodeRun
	var killed, interrupted bool
	for i := range nodes {
		switch n := &nodes[i]; n.Status {
		case store.NodeKilled:
			killed, cut = true, n
		case store.NodeInterrupted:
			interrupted = true
			if cut == nil {
				cut = n
			}
		case store.NodeFailed:
			if failed == nil {
				failed = n
			}
		}
	}
	if killed || interrupted {
		for i := range nodes {
			if n := &nodes[i]; n.Status == store.NodeQueued {
				e.skipNode(n, fmt.Sprintf("flow run %s", map[bool]string{true: "killed", false: "interrupted"}[killed]))
			}
		}
	}
	var running, waiting bool
	for i := range nodes {
		switch nodes[i].Status {
		case store.NodeQueued, store.NodeRunning:
			running = true
		case store.NodeAwaitingApproval:
			waiting = true
		}
	}
	switch {
	case running:
		e.setFlowStatus(fr, store.FlowRunRunning)
	case waiting:
		e.setFlowStatus(fr, store.FlowRunAwaitingApproval)
	case killed:
		e.endFlowRun(fr, store.FlowRunKilled, nodeMsg(cut))
	case interrupted:
		e.endFlowRun(fr, store.FlowRunInterrupted, nodeMsg(cut))
	case failed != nil:
		e.endFlowRun(fr, store.FlowRunFailed, nodeMsg(failed))
	default:
		e.endFlowRun(fr, store.FlowRunSucceeded, "")
	}
}

func nodeMsg(n *store.NodeRun) string {
	if n == nil {
		return ""
	}
	msg := n.Error
	if msg == "" {
		msg = string(n.Status)
	}
	return fmt.Sprintf("node %s: %s", n.NodeID, msg)
}

func (e *Engine) setFlowStatus(fr *store.FlowRun, st store.FlowRunStatus) {
	if fr.Status == st {
		return
	}
	fr.Status = st
	if err := e.st.UpdateFlowRun(e.dbCtx, fr); err != nil {
		e.log.Error("engine: update flow run", "flow_run", fr.ID, "err", err)
	}
}

func (e *Engine) skipNode(n *store.NodeRun, reason string) {
	now := time.Now().UTC()
	n.Status, n.Decision, n.Reason, n.EndedAt = store.NodeSkipped, store.DecisionSkipped, reason, &now
	if err := e.st.UpdateNodeRun(e.dbCtx, n); err != nil {
		e.log.Error("engine: skip node run", "node_run", n.ID, "err", err)
	}
}

// nodeRuns lists every node run of a flow run in creation order.
func (e *Engine) nodeRuns(flowRunID string) ([]store.NodeRun, error) {
	var out []store.NodeRun
	cursor := ""
	for {
		page, err := e.st.ListNodeRuns(e.dbCtx, store.NodeRunFilter{FlowRunID: flowRunID, Slim: true, ListOpts: store.ListOpts{Oldest: true, Limit: 500, Cursor: cursor}})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

// successors returns the node runs to queue for every edge leaving nr's node
// on each output's port, carrying that output's payload. A port without edges
// ends the branch. finishNode stores them in the transaction that records nr
// as finished.
func (e *Engine) successors(nr *store.NodeRun, flow *spec.Flow, outs []runner.Output) ([]*store.NodeRun, error) {
	at := e.assistTestOf(nr.FlowRunID)
	var next []*store.NodeRun
	for _, out := range outs {
		for _, edge := range flow.Edges {
			if edge.From != nr.NodeID || edge.Port != out.Port {
				continue
			}
			target := flow.NodeByID(edge.To)
			if target == nil {
				return nil, fmt.Errorf("edge to unknown node %q", edge.To)
			}
			if at != nil {
				at.recordEdge(edge.From, edge.To, edge.Port)
			}
			next = append(next, &store.NodeRun{
				FlowRunID: nr.FlowRunID, NodeID: target.ID, NodeType: nodeRunType(target),
				ParentNodeRunID: nr.ID, Stdin: store.Inline(string(out.Payload)),
			})
		}
	}
	return next, nil
}

// hasEdge reports whether any edge leaves node on port.
func hasEdge(flow *spec.Flow, node, port string) bool {
	for _, edge := range flow.Edges {
		if edge.From == node && edge.Port == port {
			return true
		}
	}
	return false
}

// readContent returns the full bytes of stored content.
func (e *Engine) readContent(c store.Content) string {
	b, err := e.st.ReadContent(c)
	if err != nil {
		e.log.Warn("engine: read stored content", "ref", c.Ref, "err", err)
		return ""
	}
	return string(b)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
