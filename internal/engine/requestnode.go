package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Approval nodes ("Request approval") raise a request approval and park the node as awaiting_approval, holding no
// concurrency slot. The decision (or its expiry) is applied by applyRequestDecision straight from DecideApproval, from
// finishNode when the decision beat the parking, and from reconcileRequests after a restart; it emits the node's input
// plus the decision on port approved or denied. Test runs (node tests and flow tests) never wait: they take the
// decision from the simulate_approval parameter.

const (
	errApprovalTimeout = "approval_timeout"

	// dryRunNotice is the first line of the body of a dry run's approval.
	dryRunNotice = "This is a dry run: approving or denying only exercises the flow's branches."

	// requestExpiryInterval is how often pending request approvals are checked for their timeout.
	requestExpiryInterval = 500 * time.Millisecond
)

// approvalInfo is the "approval" object of an approval node's output.
type approvalInfo struct {
	Decision  string  `json:"decision"` // approved | denied | timeout
	Comment   string  `json:"comment"`
	DecidedBy string  `json:"decided_by"`
	DecidedAt *string `json:"decided_at"`
}

// SimulatedApproval values for core.TestNodeRequest.SimulateApproval and core.FlowTestRequest.SimulateApproval.
const (
	SimulateApproved = "approved"
	SimulateDenied   = "denied"
)

func (e *Engine) runApproval(ne *nodeEnv) nodeOutcome {
	n := ne.node
	title, err := ne.render("title", n.Title)
	if err != nil {
		return failure(errTemplate, err.Error())
	}
	body, err := ne.render("body", n.Body)
	if err != nil {
		return failure(errTemplate, err.Error())
	}
	red := ne.snap.red
	req := store.RequestApproval{
		Title: strings.TrimSpace(red.String(title)), BodyMD: red.String(body), Priority: store.Priority(n.Priority),
		DryRun: ne.fr.DryRun, Fields: []store.RequestField{}, OnTimeout: n.OnTimeout,
	}
	if req.Title == "" {
		return failure(errTemplate, "title rendered empty")
	}
	if req.Priority == "" {
		req.Priority = store.PriorityNormal
	}
	for _, path := range n.Fields {
		req.Fields = append(req.Fields, store.RequestField{Key: path, Value: red.String(fieldText(lookupPath(ne.inputV, path)))})
	}
	if req.DryRun {
		req.BodyMD = strings.TrimRight(dryRunNotice+"\n\n"+req.BodyMD, "\n")
	}

	if ne.fr.Kind == store.KindTest {
		decision := SimulateApproved
		if v, ok := e.simulations.Load(ne.fr.ID); ok {
			decision = v.(string)
		}
		if e.assistTestOf(ne.fr.ID) == nil {
			e.simulations.Delete(ne.fr.ID) // a node test runs one node; TestFlow deletes its own
		}
		now := time.Now().UTC()
		return e.requestOutcome(ne, decision, "", "simulated", &now, "simulated approval: "+decision+": "+truncate(req.Title, 120))
	}

	raw, err := json.Marshal(req)
	if err != nil {
		return failure(errConfig, err.Error())
	}
	ap := &store.Approval{NodeRunID: ne.nr.ID, Type: store.ApprovalRequest, Request: string(raw)}
	if n.Timeout > 0 {
		exp := time.Now().UTC().Add(n.Timeout.Std())
		ap.ExpiresAt = &exp
	}
	if err := e.st.CreateApproval(e.dbCtx, ap); err != nil {
		return failure(errConfig, "create approval: "+err.Error())
	}
	title = req.Title
	if req.DryRun {
		title = "[dry run] " + title
	}
	e.notify(core.Notification{
		Kind: "approval", Title: title, Body: truncate(firstLine(strings.TrimSpace(strings.TrimPrefix(req.BodyMD, dryRunNotice))), 200),
		URL: e.url(ApprovalPath(ap.ID)),
	})
	return nodeOutcome{status: store.NodeAwaitingApproval, reason: "awaiting approval " + ap.ID}
}

// requestOutcome is the outcome of a decided approval node: the input plus the approval, on port approved or denied.
func (e *Engine) requestOutcome(ne *nodeEnv, decision, comment, by string, at *time.Time, reason string) nodeOutcome {
	info := approvalInfo{Decision: decision, Comment: comment, DecidedBy: by}
	if at != nil {
		s := at.UTC().Format(time.RFC3339)
		info.DecidedAt = &s
	}
	payload, err := json.Marshal(map[string]any{"input": json.RawMessage(nonEmptyJSON(ne.input)), "approval": info})
	if err != nil {
		return failure(errConfig, err.Error())
	}
	port := spec.PortDenied
	if decision == "approved" {
		port = spec.PortApproved
	}
	return nodeOutcome{
		status: store.NodeDone, decision: store.DecisionEmitted, reason: reason,
		outputs: []runner.Output{{Port: port, Payload: payload}},
	}
}

// applyRequestDecision carries a decided request approval into its parked node run. It does nothing while the node
// is not parked yet (finishNode calls it again once it is) or the approval is still pending.
func (e *Engine) applyRequestDecision(nodeRunID string) {
	unlock := e.locks.lock("node:" + nodeRunID)
	defer unlock()
	nr, err := e.st.GetNodeRun(e.dbCtx, nodeRunID)
	if err != nil || nr.Status != store.NodeAwaitingApproval || nr.NodeType != store.NodeApproval {
		return
	}
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{NodeRunID: nr.ID, Type: store.ApprovalRequest, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil || len(apps.Items) == 0 || apps.Items[0].Status == store.ApprovalPending {
		return
	}
	ap := apps.Items[0]
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
	var out nodeOutcome
	switch ap.Status {
	case store.ApprovalApproved:
		out = e.requestOutcome(ne, "approved", ap.Comment, ap.DecidedBy, ap.DecidedAt, "approved"+commentSuffix(ap.Comment))
	case store.ApprovalDenied:
		out = e.requestOutcome(ne, "denied", ap.Comment, ap.DecidedBy, ap.DecidedAt, "denied"+commentSuffix(ap.Comment))
	default: // expired
		switch ne.node.OnTimeout {
		case spec.OnTimeoutApproved:
			out = e.requestOutcome(ne, "timeout", ap.Comment, ap.DecidedBy, ap.DecidedAt, "timed out, treated as approved")
			out.outputs[0].Port = spec.PortApproved
		case spec.OnTimeoutError:
			out = failure(errApprovalTimeout, "approval timed out")
		default:
			out = e.requestOutcome(ne, "timeout", ap.Comment, ap.DecidedBy, ap.DecidedAt, "timed out, treated as denied")
		}
	}
	e.finishNode(ne, out)
}

func commentSuffix(c string) string {
	if c == "" {
		return ""
	}
	return ": " + truncate(c, 120)
}

// expireRequests expires pending request approvals whose timeout has passed and applies the outcome.
func (e *Engine) expireRequests() {
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{
		Type: store.ApprovalRequest, Statuses: []store.ApprovalState{store.ApprovalPending}, Slim: true, ListOpts: store.ListOpts{Limit: 500},
	})
	if err != nil {
		e.log.Warn("engine: list request approvals", "err", err)
		return
	}
	now := time.Now().UTC()
	for _, ap := range apps.Items {
		if ap.ExpiresAt == nil || ap.ExpiresAt.After(now) {
			continue
		}
		_, err := e.st.DecideApproval(e.dbCtx, ap.ID, store.ApprovalDecision{Status: store.ApprovalExpired, DecidedBy: "system", Comment: "timed out"})
		if err != nil {
			continue // decided concurrently
		}
		e.applyRequestDecision(ap.NodeRunID)
	}
}

// requestExpiryLoop checks request approvals for timeouts until the engine stops.
func (e *Engine) requestExpiryLoop(ctx context.Context) {
	t := time.NewTicker(requestExpiryInterval)
	defer t.Stop()
	for {
		e.expireRequests()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// reconcileRequests applies request decisions made while the engine was not running (or before the node parked).
func (e *Engine) reconcileRequests(ctx context.Context) error {
	nodes, err := e.st.ListNodeRuns(ctx, store.NodeRunFilter{
		NodeType: store.NodeApproval, Statuses: []store.NodeRunStatus{store.NodeAwaitingApproval}, Slim: true, ListOpts: store.ListOpts{Limit: 500},
	})
	if err != nil {
		return err
	}
	for _, n := range nodes.Items {
		e.applyRequestDecision(n.ID)
	}
	return nil
}

// lookupPath follows a dot-path (a.b.0.c) through decoded JSON; a missing step gives nil.
func lookupPath(v any, path string) any {
	for _, seg := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[seg]
		case []any:
			var i int
			if _, err := fmt.Sscanf(seg, "%d", &i); err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

// fieldText is the table text of a field value: strings as they are, null as empty, the rest as compact JSON.
func fieldText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
