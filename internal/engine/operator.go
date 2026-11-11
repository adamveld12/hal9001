package engine

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// raiseMCPApproval raises an approval for an MCP client's call: origin mcp, the client named, the caller's principal
// kept for the record, no conversation.
func (e *Engine) raiseMCPApproval(ctx context.Context, s ApprovalSpec, c tools.OperatorCaller, expires time.Time) (*Raised, error) {
	s.Type, s.Origin, s.OriginClient = store.ApprovalRequest, store.OriginMCP, c.Client
	s.Meta.Principal, s.Meta.OnTimeout = c.Principal, "denied"
	s.ExpiresAt, s.Wait = &expires, true
	r, err := e.RaiseApproval(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("create approval: %w", err)
	}
	e.log.Info("engine: mcp approval raised", "approval", r.Approval.ID, "title", r.Approval.Title, "client", c.Client, "principal", c.Principal)
	return r, nil
}

// expireWaitedApproval records the expiry of an approval a call waited on or that outlives its call. It reports
// false when the user decided at the same moment.
func (e *Engine) expireWaitedApproval(id, comment string) bool {
	_, err := e.st.DecideApproval(e.dbCtx, id, store.ApprovalDecision{Status: store.ApprovalExpired, DecidedBy: "system", Comment: comment})
	if err != nil && !errors.Is(err, store.ErrConflict) {
		e.log.Warn("engine: expire approval", "approval", id, "err", err)
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
		fields = append(fields, store.RequestField{Key: f.Key, Value: f.Value})
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
			{Key: "arguments", Value: args},
			{Key: "requested by", Value: req.Caller.RequestedBy()},
		}
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = tools.DefaultOperatorApprovalTimeout
	}
	desc := cmp.Or(strings.TrimSpace(req.Description), req.Caller.Label()+" asked to run "+req.Tool+".")
	r, err := e.raiseMCPApproval(ctx, ApprovalSpec{
		Title: title, Description: tools.ClipRunes(desc, store.MaxApprovalDescription), BodyMD: body, Fields: fields,
		Command: tools.ExtractCommand(req.Args), Tool: req.Tool,
	}, req.Caller, e.clock.Now().Add(timeout))
	if err != nil {
		return tools.Decision{}, err
	}
	defer r.Release()
	ap, ch := r.Approval, r.Decision

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
		if !e.expireWaitedApproval(ap.ID, comment) {
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
		if classifyRequest(&p) == requestMCPDetached {
			open++
		}
	}
	if open >= tools.MaxPendingRequests {
		return "", fmt.Errorf("%d request_approval approvals are already waiting in the Inbox (the limit); wait for the user to decide them", open)
	}

	var editable []store.RequestEditable
	for _, f := range q.Editable {
		editable = append(editable, store.RequestEditable{Key: f.Key, Value: f.Value})
	}
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = tools.DefaultRequestTimeout
	}
	r, err := e.raiseMCPApproval(ctx, ApprovalSpec{
		Title: q.Title, Description: q.Description, DescriptionBy: store.DescByAgent, BodyMD: q.BodyMD,
		Fields:   []store.RequestField{{Key: "requested by", Value: q.Caller.RequestedBy()}},
		Priority: store.Priority(q.Priority), Editable: editable, Meta: store.RequestApproval{Detached: true},
	}, q.Caller, e.clock.Now().Add(timeout))
	if err != nil {
		return "", err
	}
	defer r.Release()
	ap, ch := r.Approval, r.Decision

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
			e.expireWaitedApproval(ap.ID, "no decision within "+timeout.String())
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
