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

// SubmitProposal registers a pending proposal approval and notifies. Its title and description are the proposal's
// summary line.
func (b *storeBroker) SubmitProposal(ctx context.Context, p agent.ProposalApproval) (string, error) {
	summary := proposalSummary(p.Proposal)
	r, err := b.e.RaiseApproval(ctx, ApprovalSpec{
		Type: store.ApprovalProposal, Title: summary, Description: summary, Proposal: p.Proposal,
		AgentRunID: p.RunID, NodeRunID: p.NodeRunID,
	})
	if err != nil {
		return "", err
	}
	return r.Approval.ID, nil
}

// proposalSummary is the first non-empty line of a proposal with the markdown removed, at most 200 characters.
func proposalSummary(text string) string {
	line, _, _ := strings.Cut(notify.Body(text), "\n")
	if line == "" {
		return "Proposal from the agent"
	}
	return line
}

func cancelText(ctx context.Context) string {
	if c := context.Cause(ctx); c != nil {
		return c.Error()
	}
	return "cancelled"
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

// DecideApproval applies a user's decision. An approved proposal resumes the
// agent (queued behind the concurrency cap and the kill switch); a proposal
// denied with steering makes the agent revise it and propose again, and any
// other denial routes the node to port denied. A tool-call decision unblocks
// the waiting agent process. The comment of a denial is the user's steering.
// Deciding an approval that is not pending is core.ErrInvalidState.
func (e *Engine) DecideApproval(ctx context.Context, approvalID string, d core.ApprovalDecision) (*store.Approval, error) {
	ap, err := e.st.GetApproval(ctx, approvalID)
	if err != nil {
		return nil, err
	}
	if ap.Status != store.ApprovalPending {
		return nil, fmt.Errorf("%w: approval %s is already %s", core.ErrInvalidState, approvalID, ap.Status)
	}
	d.Comment = strings.TrimSpace(d.Comment) // the steering counter counts denials with a non-empty comment
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
		kind := classifyRequest(ap)
		var wait chan agent.ToolDecision
		if kind.waited() {
			e.waitMu.Lock()
			wait = e.waiters[ap.ID]
			e.waitMu.Unlock()
			if wait == nil && !kind.detached() { // a gated call: it is blocked on this approval
				return nil, fmt.Errorf("%w: the MCP client is no longer waiting for this approval", core.ErrInvalidState)
			}
		}
		decided, err := e.st.DecideApproval(ctx, ap.ID, dec)
		if err != nil {
			return nil, mapDecideErr(approvalID, err)
		}
		if kind.waited() {
			if wait != nil {
				verdict := agent.VerdictDeny
				if d.Approve {
					verdict = agent.VerdictAllow
				}
				select {
				case wait <- agent.ToolDecision{Verdict: verdict, Comment: d.Comment, Held: ap.Held()}:
				default:
				}
			}
			e.log.Info("engine: approval decided", "approval", ap.ID, "kind", kind, "status", decided.Status, "by", d.Principal)
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
		ch <- agent.ToolDecision{Verdict: verdict, Comment: d.Comment, Held: ap.Held()}
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

// requestKind classifies a request approval by what its row says, never by which ids are missing.
type requestKind int

const (
	requestNode        requestKind = iota // a Request approval node
	requestGate                           // the gate of a node with requires_approval
	requestMCP                            // a gated call of an MCP client: its call blocks on the decision
	requestMCPDetached                    // an MCP client's request_approval: outlives the call
	requestAsked                          // an agent run's request_approval: its tool call waits up to wait_seconds
)

func (k requestKind) String() string {
	return [...]string{"node", "gate", "mcp", "mcp_detached", "asked"}[k]
}

// waited reports whether the decision goes to a waiter (an MCP call or an agent's tool call) instead of a node run.
func (k requestKind) waited() bool { return k >= requestMCP }

// detached reports whether the approval stays open after the call that raised it stops waiting: it is decided and
// expires on its own.
func (k requestKind) detached() bool { return k >= requestMCPDetached }

func classifyRequest(ap *store.Approval) requestKind {
	var meta store.RequestApproval
	_ = json.Unmarshal([]byte(ap.Request), &meta) // a malformed record reads as a plain node request
	switch {
	case ap.Origin == store.OriginMCP && meta.Detached:
		return requestMCPDetached
	case ap.Origin == store.OriginMCP:
		return requestMCP
	case ap.AgentRunID != "":
		return requestAsked
	case meta.Gate:
		return requestGate
	}
	return requestNode
}

func mapDecideErr(id string, err error) error {
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("%w: approval %s was decided concurrently", core.ErrInvalidState, id)
	}
	return err
}

// applyProposalDecision carries a decided proposal approval into its node run:
// approved requeues the node (it resumes the session when claimed), denied
// with steering requeues it to revise the proposal, any other denial ends it
// on port denied. It does nothing while the node is not parked yet;
// finishNode calls it again once the node is.
func (e *Engine) applyProposalDecision(nodeRunID string) {
	unlock := e.locks.lock("node:" + nodeRunID)
	defer unlock()
	nr, err := e.st.GetNodeRun(e.dbCtx, nodeRunID)
	if err != nil || nr.Status != store.NodeAwaitingApproval {
		return
	}
	runs, err := e.st.ListAgentRuns(e.dbCtx, store.AgentRunFilter{NodeRunID: nr.ID, ListOpts: store.ListOpts{Oldest: true, Limit: 500}})
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
	switch ap := apps.Items[0]; {
	case ap.Status == store.ApprovalApproved, ap.Status == store.ApprovalDenied && steersProposal(&ap):
		// Approved: the node resumes the session and executes. Denied with steering: it runs another read-only
		// turn that revises the proposal and parks the node again on a new approval.
		reason := "proposal approved"
		if ap.Status == store.ApprovalDenied {
			reason = "proposal denied with steering; the agent revises it"
		}
		nr.Status, nr.EndedAt, nr.Reason = store.NodeQueued, nil, reason
		if err := e.st.UpdateNodeRun(e.dbCtx, nr); err != nil {
			e.log.Error("engine: requeue decided node run", "node_run", nr.ID, "err", err)
			return
		}
		e.reconcileFlowRun(nr.FlowRunID)
		e.wake()
	case ap.Status == store.ApprovalDenied:
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
