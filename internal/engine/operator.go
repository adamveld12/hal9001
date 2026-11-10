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

// OperatorHost is the engine as hal's operator tools use it: it queues the call of an external MCP client as a
// request approval that belongs to no flow or agent run.
func (e *Engine) OperatorHost() tools.OperatorHost { return operatorHost{e} }

type operatorHost struct{ e *Engine }

var _ tools.OperatorHost = operatorHost{}

var errOperatorApprovalTimeout = errors.New("operator approval timeout")

// ApproveOperator registers a request approval in the Inbox and blocks for the decision, the timeout or ctx (the MCP
// client gave up). DecideApproval answers the waiter registered here.
func (h operatorHost) ApproveOperator(ctx context.Context, req tools.OperatorApproval) (tools.Decision, error) {
	e := h.e
	red := e.snapshot().red
	args := string(req.Args)
	var pretty bytes.Buffer
	if json.Indent(&pretty, req.Args, "", "  ") == nil {
		args = pretty.String()
	}
	rq := store.RequestApproval{
		Title:  "MCP client: " + req.Tool,
		BodyMD: red.String(req.Reason + "\n\nAn MCP client connected to hal9001 asked for this. Approving runs it; denying tells the client it was refused."),
		Fields: []store.RequestField{
			{Key: "tool", Value: req.Tool},
			{Key: "arguments", Value: red.String(args)},
			{Key: "requested by", Value: req.Caller},
		},
		Priority:  store.PriorityNormal,
		OnTimeout: "denied",
	}
	raw, err := json.Marshal(rq)
	if err != nil {
		return tools.Decision{}, err
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = tools.DefaultOperatorApprovalTimeout
	}
	expires := e.clock.Now().Add(timeout)
	ap := &store.Approval{Type: store.ApprovalRequest, Request: string(raw), ExpiresAt: &expires}
	if err := e.st.CreateApproval(e.dbCtx, ap); err != nil {
		return tools.Decision{}, fmt.Errorf("create approval: %w", err)
	}
	ch := make(chan agent.ToolDecision, 1)
	e.waitMu.Lock()
	e.waiters[ap.ID] = ch
	e.waitMu.Unlock()
	defer func() {
		e.waitMu.Lock()
		delete(e.waiters, ap.ID)
		e.waitMu.Unlock()
	}()
	e.notify(notify.Request("mcp", ap.ID, rq, e.url(ApprovalPath(ap.ID))))

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
		_, derr := e.st.DecideApproval(e.dbCtx, ap.ID, store.ApprovalDecision{Status: store.ApprovalExpired, DecidedBy: "system", Comment: comment})
		if derr != nil && !errors.Is(derr, store.ErrConflict) {
			e.log.Warn("engine: expire operator approval", "approval", ap.ID, "err", derr)
		}
		if errors.Is(derr, store.ErrConflict) {
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
