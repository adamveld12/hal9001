package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/notify"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// OperatorHost is the engine as hal's operator tools use it: it queues the calls of an external MCP client as request
// approvals that belong to no flow or agent run, and posts its notes to the Inbox.
func (e *Engine) OperatorHost() tools.OperatorHost { return operatorHost{e} }

type operatorHost struct{ e *Engine }

var _ tools.OperatorHost = operatorHost{}

var errOperatorApprovalTimeout = errors.New("operator approval timeout")

// isDetached reports whether a request approval's content (its stored JSON) says it outlives the MCP call that raised it.
func isDetached(request string) bool {
	var rq store.RequestApproval
	return json.Unmarshal([]byte(request), &rq) == nil && rq.Detached
}

// mcpSource marks everything an external MCP client causes: approvals (RequestApproval.Source), inbox notes
// (store.SourceMCP) and notifications (flow id "mcp").
const mcpSource = "mcp"

// operatorRequest is the content of an operator approval, redacted, with the caller recorded.
func (e *Engine) operatorRequest(title, bodyMD string, fields []store.RequestField, c tools.OperatorCaller) store.RequestApproval {
	red := e.snapshot().red
	return store.RequestApproval{
		Title: red.String(title), BodyMD: red.String(bodyMD), Fields: fields, Priority: store.PriorityNormal, OnTimeout: "denied",
		Source: mcpSource, Client: c.Client, Principal: c.Principal,
	}
}

// raiseOperatorApproval stores a pending operator approval and notifies. It returns the approval and the registered
// waiter, which DecideApproval answers; the caller deletes it with dropWaiter.
func (e *Engine) raiseOperatorApproval(rq store.RequestApproval, expires time.Time, c tools.OperatorCaller) (*store.Approval, chan agent.ToolDecision, error) {
	raw, err := json.Marshal(rq)
	if err != nil {
		return nil, nil, err
	}
	ap := &store.Approval{Type: store.ApprovalRequest, Request: string(raw), ExpiresAt: &expires}
	if err := e.st.CreateApproval(e.dbCtx, ap); err != nil {
		return nil, nil, fmt.Errorf("create approval: %w", err)
	}
	ch := make(chan agent.ToolDecision, 1)
	e.waitMu.Lock()
	e.waiters[ap.ID] = ch
	e.waitMu.Unlock()
	e.notify(notify.FromMCP(notify.Request(notify.MCPFlowID, ap.ID, rq, e.url(ApprovalPath(ap.ID))), c.Client, ""))
	e.log.Info("engine: mcp approval raised", "approval", ap.ID, "title", rq.Title, "client", c.Client, "principal", c.Principal)
	return ap, ch, nil
}

func (e *Engine) dropWaiter(id string) {
	e.waitMu.Lock()
	delete(e.waiters, id)
	e.waitMu.Unlock()
}

// expireOperatorApproval records an expiry. It reports false when the user decided at the same moment.
func (e *Engine) expireOperatorApproval(id, comment string) bool {
	_, err := e.st.DecideApproval(e.dbCtx, id, store.ApprovalDecision{Status: store.ApprovalExpired, DecidedBy: "system", Comment: comment})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		e.log.Warn("engine: expire operator approval", "approval", id, "err", err)
	}
	return err == nil
}

// ApproveOperator registers a request approval in the Inbox for one tool call and blocks for the decision, the
// timeout or ctx (the MCP client gave up). DecideApproval answers the waiter registered here.
func (h operatorHost) ApproveOperator(ctx context.Context, req tools.OperatorApproval) (tools.Decision, error) {
	e := h.e
	title, body := req.Title, req.BodyMD
	var fields []store.RequestField
	for _, f := range req.Fields {
		fields = append(fields, store.RequestField{Key: f.Key, Value: e.snapshot().red.String(f.Value)})
	}
	if title == "" {
		title = "MCP client: " + req.Tool
		args := string(req.Args)
		var pretty bytes.Buffer
		if json.Indent(&pretty, req.Args, "", "  ") == nil {
			args = pretty.String()
		}
		body = req.Reason + "\n\nAn MCP client connected to hal9001 asked for this. Approving runs it; denying tells the client it was refused."
		fields = []store.RequestField{
			{Key: "tool", Value: req.Tool},
			{Key: "arguments", Value: e.snapshot().red.String(args)},
			{Key: "requested by", Value: req.Caller.RequestedBy()},
		}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = tools.DefaultOperatorApprovalTimeout
	}
	rq := e.operatorRequest(title, body, fields, req.Caller)
	ap, ch, err := e.raiseOperatorApproval(rq, e.clock.Now().Add(timeout), req.Caller)
	if err != nil {
		return tools.Decision{}, err
	}
	defer e.dropWaiter(ap.ID)

	actx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := e.clock.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		select {
		case <-timer.C():
			cancel(errOperatorApprovalTimeout)
		case <-actx.Done():
		}
	}()

	select {
	case d := <-ch:
		return tools.Decision{Allowed: d.Verdict == agent.VerdictAllow, Comment: d.Comment, RecordID: ap.ID}, nil
	case <-actx.Done():
		expired := errors.Is(context.Cause(actx), errOperatorApprovalTimeout)
		comment := "no decision within " + timeout.String()
		if !expired {
			comment = "the MCP client gave up waiting: " + cancelText(actx)
		}
		if !e.expireOperatorApproval(ap.ID, comment) {
			// The user decided at the same moment; DecideApproval sends the verdict right after it stores it.
			select {
			case d := <-ch:
				return tools.Decision{Allowed: d.Verdict == agent.VerdictAllow, Comment: d.Comment, RecordID: ap.ID}, nil
			case <-time.After(2 * time.Second):
			}
		}
		if expired {
			return tools.Decision{Expired: true, RecordID: ap.ID}, nil
		}
		return tools.Decision{}, actx.Err()
	}
}

// AskOperator registers a detached request approval (request_approval) and waits for q.Wait. The approval stays open
// after the call returns: it is decided through the Inbox like any other and expires after q.Timeout, and a daemon
// restart does not expire it. The caller reads the outcome from the approval.
func (h operatorHost) AskOperator(ctx context.Context, q tools.OperatorQuestion) (string, error) {
	e := h.e
	pending, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{
		Type: store.ApprovalRequest, Statuses: []store.ApprovalState{store.ApprovalPending}, Slim: true, ListOpts: store.ListOpts{Limit: 500},
	})
	if err != nil {
		return "", err
	}
	open := 0
	for _, p := range pending.Items {
		var rq store.RequestApproval
		if p.NodeRunID == "" && json.Unmarshal([]byte(p.Request), &rq) == nil && rq.Detached {
			open++
		}
	}
	if open >= tools.MaxPendingRequests {
		return "", fmt.Errorf("%d request_approval approvals are already waiting in the Inbox (the limit); wait for the user to decide them", open)
	}

	red := e.snapshot().red
	rq := e.operatorRequest(q.Title, q.BodyMD, []store.RequestField{{Key: "requested by", Value: q.Caller.RequestedBy()}}, q.Caller)
	rq.Title = red.String(q.Title)
	rq.Detached, rq.Priority = true, store.Priority(q.Priority)
	if rq.Priority == "" {
		rq.Priority = store.PriorityNormal
	}
	for _, f := range q.Editable {
		rq.Editable = append(rq.Editable, store.RequestEditable{Key: f.Key, Value: f.Value})
	}
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = tools.DefaultRequestTimeout
	}
	ap, ch, err := e.raiseOperatorApproval(rq, e.clock.Now().Add(timeout), q.Caller)
	if err != nil {
		return "", err
	}
	defer e.dropWaiter(ap.ID)

	wait := q.Wait
	if wait > timeout {
		wait = timeout
	}
	timer := e.clock.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch: // decided; the caller reads the decision from the approval
	case <-timer.C():
		if wait >= timeout {
			e.expireOperatorApproval(ap.ID, "no decision within "+timeout.String())
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return ap.ID, nil
}

// PostOperatorNote creates an inbox note for an MCP client and raises its notification.
func (h operatorHost) PostOperatorNote(ctx context.Context, n tools.OperatorNote) (tools.OperatorNoted, error) {
	e := h.e
	red := e.snapshot().red
	it := &store.InboxItem{
		SourceType: store.SourceMCP, Title: red.String(n.Title), Priority: store.Priority(n.Priority), SummaryMD: red.String(n.BodyMD),
		Links: []store.InboxLink{}, Client: n.Caller.Client, ThreadKey: n.Thread,
	}
	if n.URL != "" {
		label := n.URLLabel
		if label == "" {
			label = n.URL
		}
		it.Links = append(it.Links, store.InboxLink{Label: label, URL: n.URL})
	}
	if err := e.st.CreateInboxItem(ctx, it); err != nil {
		return tools.OperatorNoted{}, fmt.Errorf("create inbox item: %w", err)
	}
	link := e.url(InboxPath(it.ID))
	e.notify(notify.FromMCP(notify.Note(notify.MCPFlowID, it, link), n.Caller.Client, n.Thread))
	e.log.Info("engine: mcp note posted", "item", it.ID, "client", n.Caller.Client, "principal", n.Caller.Principal, "priority", it.Priority)
	return tools.OperatorNoted{ItemID: it.ID, URL: link}, nil
}
