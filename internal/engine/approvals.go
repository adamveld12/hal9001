package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/notify"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// storeBroker connects the agent client to the approval queue in the store
// (agent.ApprovalBroker). Proposals are plain rows that outlive the agent
// process; per-tool requests keep a waiter that DecideApproval answers.
type storeBroker struct{ e *Engine }

var _ agent.ApprovalBroker = (*storeBroker)(nil)

// SubmitProposal registers a pending proposal approval and notifies.
func (b *storeBroker) SubmitProposal(_ context.Context, p agent.ProposalApproval) (string, error) {
	e := b.e
	red := e.snapshot().red
	a := &store.Approval{AgentRunID: p.RunID, Type: store.ApprovalProposal, Proposal: store.Inline(red.String(p.Proposal))}
	if err := e.st.CreateApproval(e.dbCtx, a); err != nil {
		return "", err
	}
	flowID, dry := e.flowOfNodeRun(p.NodeRunID)
	e.notify(notify.Proposal(flowID, a.ID, p.Proposal, dry, e.url(ApprovalPath(a.ID))))
	return a.ID, nil
}

// AwaitToolApproval registers a pending tool-call approval, parks the node as
// awaiting_approval (the agent process stays alive and keeps its slot) and
// blocks until a user decides or ctx ends.
func (b *storeBroker) AwaitToolApproval(ctx context.Context, t agent.ToolApproval) (agent.ToolDecision, error) {
	e := b.e
	expires := t.ExpiresAt
	a := &store.Approval{
		AgentRunID: t.RunID, Type: store.ApprovalToolCall, PermissionRequestID: t.PermissionRequestID, ExpiresAt: &expires,
	}
	if err := e.st.CreateApproval(e.dbCtx, a); err != nil {
		return agent.ToolDecision{}, err
	}
	ch := make(chan agent.ToolDecision, 1)
	e.waitMu.Lock()
	e.waiters[a.ID] = ch
	e.waitMu.Unlock()
	defer func() {
		e.waitMu.Lock()
		delete(e.waiters, a.ID)
		e.waitMu.Unlock()
	}()

	e.markToolWait(t.RunID, t.NodeRunID, true)
	e.toolApprovalPending(t.RunID, t.ToolCallID, a.ID)
	allowed := false
	defer func() { e.toolApprovalResolved(t.RunID, a.ID, allowed) }()
	flowID, dry := e.flowOfNodeRun(t.NodeRunID)
	tool := t.Title
	if tool == "" {
		tool = t.ToolKind
	}
	e.notify(notify.ToolCall(flowID, a.ID, tool, t.RawInput, dry, e.url(ApprovalPath(a.ID))))
	select {
	case d := <-ch:
		allowed = d.Verdict == agent.VerdictAllow
		e.markToolWait(t.RunID, t.NodeRunID, false)
		return d, nil
	case <-ctx.Done():
		// Nobody answered in time (or the run was cancelled): the approval can
		// no longer be granted. The agent records the permission request itself.
		_, err := e.st.DecideApproval(e.dbCtx, a.ID, store.ApprovalDecision{
			Status: store.ApprovalExpired, DecidedBy: "system", Comment: "no decision: " + cancelText(ctx),
		})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			e.log.Warn("engine: expire tool approval", "approval", a.ID, "err", err)
		}
		return agent.ToolDecision{}, ctx.Err()
	}
}

func cancelText(ctx context.Context) string {
	if c := context.Cause(ctx); c != nil {
		return c.Error()
	}
	return "cancelled"
}

// flowOfNodeRun is the flow id and dry-run flag of a node run's flow run, for notifications. A turn of an assist
// session has no node run: it is reported as "assistant".
func (e *Engine) flowOfNodeRun(nodeRunID string) (flowID string, dryRun bool) {
	if nodeRunID == "" {
		return "assistant", false
	}
	nr, err := e.st.GetNodeRun(e.dbCtx, nodeRunID)
	if err != nil {
		return "?", false
	}
	return e.flowRunInfo(nr.FlowRunID)
}

func (e *Engine) flowRunInfo(flowRunID string) (flowID string, dryRun bool) {
	fr, err := e.st.GetFlowRun(e.dbCtx, flowRunID)
	if err != nil {
		return "?", false
	}
	return fr.FlowID, fr.DryRun
}

// markToolWait flips a node and its agent run between running and
// awaiting_approval around a per-tool approval.
func (e *Engine) markToolWait(runID, nodeRunID string, waiting bool) {
	as, ns := store.AgentRunning, store.NodeRunning
	if waiting {
		as, ns = store.AgentAwaitingApproval, store.NodeAwaitingApproval
	}
	if a, err := e.st.GetAgentRun(e.dbCtx, runID); err == nil && (a.Status == store.AgentRunning || a.Status == store.AgentAwaitingApproval) {
		a.Status = as
		if err := e.st.UpdateAgentRun(e.dbCtx, a); err != nil {
			e.log.Warn("engine: mark agent run", "agent_run", runID, "err", err)
		}
	}
	if nodeRunID == "" {
		return // an assist turn: the assist manager tracks its session
	}
	nr, err := e.st.GetNodeRun(e.dbCtx, nodeRunID)
	if err != nil {
		return
	}
	if nr.Status == store.NodeRunning || nr.Status == store.NodeAwaitingApproval {
		nr.Status = ns
		if err := e.st.UpdateNodeRun(e.dbCtx, nr); err != nil {
			e.log.Warn("engine: mark node run", "node_run", nodeRunID, "err", err)
		}
	}
	e.reconcileFlowRun(nr.FlowRunID)
}

// notify delivers a notification without letting a slow notifier hold up the engine for long.
func (e *Engine) notify(n core.Notification) {
	if e.opt.Notifier == nil {
		return
	}
	ctx, cancel := context.WithTimeout(e.dbCtx, 10*time.Second)
	defer cancel()
	if err := e.opt.Notifier.Notify(ctx, n); err != nil {
		e.log.Warn("engine: notification failed", "kind", n.Kind, "err", err)
	}
}

// InboxPath is the UI path of an inbox note.
func InboxPath(id string) string { return "/inbox/" + id }

// ApprovalPath is the UI path of an approval, opened inside the Inbox.
func ApprovalPath(id string) string { return "/inbox/approval/" + id }

// url builds an absolute UI deep link.
func (e *Engine) url(path string) string {
	return strings.TrimRight(e.opt.UIBaseURL, "/") + path
}

// DecideApproval applies a user's decision. A proposal approval resumes the
// agent (queued behind the concurrency cap and the kill switch); a denial
// routes the node to port denied. A tool-call decision unblocks the waiting
// agent process. Deciding an approval that is not pending is core.ErrInvalidState.
func (e *Engine) DecideApproval(ctx context.Context, approvalID string, d core.ApprovalDecision) (*store.Approval, error) {
	ap, err := e.st.GetApproval(ctx, approvalID)
	if err != nil {
		return nil, err
	}
	if ap.Status != store.ApprovalPending {
		return nil, fmt.Errorf("%w: approval %s is already %s", core.ErrInvalidState, approvalID, ap.Status)
	}
	dec := store.ApprovalDecision{Status: store.ApprovalDenied, Comment: d.Comment, DecidedBy: d.Principal}
	if d.Approve {
		dec.Status = store.ApprovalApproved
	}

	if ap.Type == store.ApprovalRequest {
		if d.CancelRun {
			return nil, fmt.Errorf("%w: cancel_run applies to tool_call approvals", core.ErrInvalidState)
		}
		if d.Approve && len(d.Edits) > 0 { // a denial ignores edits
			rq, err := recordEdits(ap.Request, d.Edits)
			if err != nil {
				return nil, err
			}
			dec.Request = rq
		}
		var wait chan agent.ToolDecision
		if ap.NodeRunID == "" { // an operator approval: an MCP call is blocked on it
			e.waitMu.Lock()
			wait = e.waiters[ap.ID]
			e.waitMu.Unlock()
			if wait == nil {
				return nil, fmt.Errorf("%w: the MCP client is no longer waiting for this approval", core.ErrInvalidState)
			}
		}
		decided, err := e.st.DecideApproval(ctx, ap.ID, dec)
		if err != nil {
			return nil, mapDecideErr(approvalID, err)
		}
		if wait != nil {
			verdict := agent.VerdictDeny
			if d.Approve {
				verdict = agent.VerdictAllow
			}
			wait <- agent.ToolDecision{Verdict: verdict, Comment: d.Comment}
			return decided, nil
		}
		e.applyRequestDecision(decided.NodeRunID)
		return decided, nil
	}

	if ap.Type == store.ApprovalToolCall {
		e.waitMu.Lock()
		ch := e.waiters[ap.ID]
		e.waitMu.Unlock()
		if ch == nil {
			return nil, fmt.Errorf("%w: %w", core.ErrInvalidState, errNoWaiter)
		}
		verdict := agent.VerdictDeny
		dec.PermissionDecision, dec.PermissionReason = store.PermReject, "denied by user"
		switch {
		case d.Approve:
			verdict, dec.PermissionDecision, dec.PermissionReason = agent.VerdictAllow, store.PermAllow, "approved by user"
		case d.CancelRun:
			verdict, dec.PermissionDecision, dec.PermissionReason = agent.VerdictDenyCancel, store.PermCancelled, "denied by user and the run cancelled"
		}
		if d.Comment != "" {
			dec.PermissionReason += ": " + d.Comment
		}
		decided, err := e.st.DecideApproval(ctx, ap.ID, dec)
		if err != nil {
			return nil, mapDecideErr(approvalID, err)
		}
		ch <- agent.ToolDecision{Verdict: verdict, Comment: d.Comment}
		return decided, nil
	}

	decided, err := e.st.DecideApproval(ctx, ap.ID, dec)
	if err != nil {
		return nil, mapDecideErr(approvalID, err)
	}
	run, err := e.st.GetAgentRun(ctx, decided.AgentRunID)
	if err != nil {
		return decided, nil //nolint:nilerr // the decision is recorded; reconcileProposals applies it on the next start
	}
	e.applyProposalDecision(run.NodeRunID)
	return decided, nil
}

func mapDecideErr(id string, err error) error {
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("%w: approval %s was decided concurrently", core.ErrInvalidState, id)
	}
	return err
}

// applyProposalDecision carries a decided proposal approval into its node run:
// approved requeues the node (it resumes the session when claimed), denied
// ends it on port denied. It does nothing while the node is not parked yet;
// finishNode calls it again once the node is.
func (e *Engine) applyProposalDecision(nodeRunID string) {
	unlock := e.locks.lock("node:" + nodeRunID)
	defer unlock()
	nr, err := e.st.GetNodeRun(e.dbCtx, nodeRunID)
	if err != nil || nr.Status != store.NodeAwaitingApproval {
		return
	}
	runs, err := e.st.ListAgentRuns(e.dbCtx, store.AgentRunFilter{NodeRunID: nr.ID, ListOpts: store.ListOpts{Oldest: true, Limit: 50}})
	if err != nil || len(runs.Items) == 0 {
		return
	}
	run := runs.Items[len(runs.Items)-1]
	if run.Phase != store.PhasePropose || run.Status != store.AgentAwaitingApproval {
		return
	}
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{AgentRunID: run.ID, Type: store.ApprovalProposal, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil || len(apps.Items) == 0 {
		return
	}
	switch ap := apps.Items[0]; ap.Status {
	case store.ApprovalApproved:
		nr.Status, nr.EndedAt, nr.Reason = store.NodeQueued, nil, "proposal approved"
		if err := e.st.UpdateNodeRun(e.dbCtx, nr); err != nil {
			e.log.Error("engine: requeue approved node run", "node_run", nr.ID, "err", err)
			return
		}
		e.reconcileFlowRun(nr.FlowRunID)
		e.wake()
	case store.ApprovalDenied:
		e.finishDenied(nr, &run, &ap)
	}
}

// finishDenied records a denied proposal and routes the node to port denied.
func (e *Engine) finishDenied(nr *store.NodeRun, run *store.AgentRun, ap *store.Approval) {
	fr, err := e.st.GetFlowRun(e.dbCtx, nr.FlowRunID)
	if err != nil {
		e.log.Error("engine: load flow run", "flow_run", nr.FlowRunID, "err", err)
		return
	}
	ne, err := e.newNodeEnv(fr, nr)
	if err != nil {
		e.failNodeRun(nr, err.Error())
		e.reconcileFlowRun(fr.ID)
		return
	}
	var started time.Time
	if run.StartedAt != nil {
		started = *run.StartedAt
	}
	ex := &agent.Executor{
		Recorder: &runRecorder{e: e, red: ne.snap.red, model: run.Model, worktree: run.WorktreePath},
		Clock:    e.clock,
		Log:      e.log,
	}
	ex.DenyProposal(agent.RunRecord{
		ID: run.ID, NodeRunID: run.NodeRunID, ParentRunID: run.PrevAgentRunID, Runtime: run.Runtime,
		Mode: agent.Mode(run.Mode), Approve: agent.ApproveStyle(run.ApproveStyle), Policy: agent.Policy(run.Policy), Phase: agent.Phase(run.Phase),
		Cwd: run.Cwd, SessionID: run.SessionID, Status: agent.StatusAwaitingApproval, StartedAt: started,
	})
	if run.WorktreePath != "" {
		if err := e.wt.Remove(e.dbCtx, nr.ID); err != nil {
			e.log.Warn("engine: remove worktree of denied proposal", "node_run", nr.ID, "err", err)
		}
	}
	reason := "proposal denied"
	if ap.Comment != "" {
		reason += ": " + ap.Comment
	}
	e.finishNode(ne, nodeOutcome{
		status: store.NodeDone, decision: store.DecisionEmitted, class: agent.ReasonDenied, reason: reason,
		outputs: []runner.Output{{Port: spec.PortDenied, Payload: e.deniedPayload(ne, run, ap)}},
	})
}

// reconcileProposals applies approvals that were decided while the engine was
// not running (or between the decision and its application): the proposal
// surviving a restart is the whole point of parking it in the store.
func (e *Engine) reconcileProposals(ctx context.Context) error {
	runs, err := e.st.ListAgentRuns(ctx, store.AgentRunFilter{Statuses: []store.AgentStatus{store.AgentAwaitingApproval}, Slim: true, ListOpts: store.ListOpts{Limit: 500}})
	if err != nil {
		return err
	}
	for _, r := range runs.Items {
		if r.Phase == store.PhasePropose && r.ApproveStyle == store.StyleProposal {
			e.applyProposalDecision(r.NodeRunID)
		}
	}
	return nil
}
