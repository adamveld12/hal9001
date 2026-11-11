package engine

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"hal9001/internal/agent"
	"hal9001/internal/conversation"
	"hal9001/internal/notify"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// ErrDescriptionRequired is the error of an approval raised without a description. It is the text a request_approval
// caller gets back as a tool error, so the agent retries with one.
var ErrDescriptionRequired = tools.ErrDescriptionRequired

// ApprovalSpec is everything RaiseApproval needs to put one approval in the queue. Every approval hal shows comes
// from one: an agent's tool call, its request_approval, a proposal, a node gate, a Request approval node, an MCP
// client's call.
type ApprovalSpec struct {
	Type store.ApprovalType
	// Title is one line: what is asked.
	Title string
	// Description is required, at most store.MaxApprovalDescription characters: what happens if approved and why.
	Description string
	// DescriptionBy says who wrote Description: hal (the default, generated from facts) or the agent.
	DescriptionBy store.DescriptionBy
	// BodyMD, Fields, Command, Tool and Diffs are the details shown next to the description: what will actually
	// happen. Command is the shell command or explanation, extracted from nested tool arguments by the caller.
	BodyMD  string
	Fields  []store.RequestField
	Command string
	Tool    string
	Diffs   []store.ApprovalDiff
	// Priority defaults to normal (high for a tool call).
	Priority store.Priority
	// Editable lists the input paths the approver may edit, with their current values.
	Editable []store.RequestEditable
	// Meta carries the request machinery: OnTimeout, Gate, Detached, Principal. Its Editable is set from Editable.
	Meta   store.RequestApproval
	DryRun bool
	// AgentRunID is the flow agent run or chat turn run that raised the approval; it links the approval to its
	// conversation. NodeRunID is the node run of a Request approval node or a node gate. An approval with an agent
	// run does not store NodeRunID: its lineage is the agent run's.
	AgentRunID string
	NodeRunID  string
	// Origin and OriginLabel override what is derived: an agent run of a chat session is chat, any other flow, an
	// approval without runs must name its origin (mcp). OriginClient names the MCP client.
	Origin       store.ApprovalOrigin
	OriginLabel  string
	OriginClient string
	// Proposal is the text of a proposal approval.
	Proposal string
	// Permission is the permission request of a tool_call approval; RaiseApproval creates it first.
	Permission *store.PermissionRequest
	// HeldNote, when set, raises the approval held (the conversation's steering cap was reached): it offers no
	// steering and the note says why.
	HeldNote  string
	ExpiresAt *time.Time
	// Wait registers a waiter that DecideApproval answers (tool calls, agent-asked requests, gated MCP calls).
	Wait bool
	// FlowID names the flow in the notification; derived from the runs when empty.
	FlowID string
}

// Raised is a stored approval. Decision receives the user's decision when the spec asked to Wait; call Release when
// done waiting.
type Raised struct {
	Approval *store.Approval
	Decision <-chan agent.ToolDecision
	// Release drops the waiter. It is safe to call more than once.
	Release func()
}

// RaiseApproval validates, redacts and stores an approval, registers its waiter, creates the permission request of a
// tool call, links the approval to its conversation and sends the notification. The store's change note makes the
// API publish approval.created. It is the only caller of store.CreateApproval (a test scans the tree).
func (e *Engine) RaiseApproval(ctx context.Context, s ApprovalSpec) (*Raised, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	red := e.snapshot().red
	s.Title = clipTitle(red.String(strings.TrimSpace(s.Title)))
	s.Description = red.String(strings.TrimSpace(s.Description))
	if n := utf8.RuneCountInString(s.Description); n > store.MaxApprovalDescription {
		return nil, fmt.Errorf("%w: description is %d characters; the limit is %d", store.ErrInvalid, n, store.MaxApprovalDescription)
	}

	a := &store.Approval{
		ID: e.st.NewID(), Type: s.Type, Title: s.Title, Description: s.Description, DescriptionBy: s.DescriptionBy, Priority: s.Priority,
		Origin: s.Origin, OriginLabel: s.OriginLabel, OriginClient: s.OriginClient, ExpiresAt: s.ExpiresAt,
		AgentRunID: s.AgentRunID, NodeRunID: s.NodeRunID,
	}
	if a.DescriptionBy == "" {
		a.DescriptionBy = store.DescByHal
	}
	if a.Priority == "" {
		a.Priority = store.PriorityNormal
		if a.Type == store.ApprovalToolCall {
			a.Priority = store.PriorityHigh
		}
	}
	if s.Type == store.ApprovalProposal {
		a.Proposal = store.Inline(red.String(s.Proposal))
	}
	a.Details = store.ApprovalDetails{
		BodyMD: red.String(s.BodyMD), Fields: []store.RequestField{}, Command: red.String(s.Command), Tool: s.Tool,
		Diffs: []store.ApprovalDiff{}, DryRun: s.DryRun,
	}
	for _, f := range s.Fields {
		a.Details.Fields = append(a.Details.Fields, store.RequestField{Key: f.Key, Value: red.String(f.Value)})
	}
	for _, d := range s.Diffs {
		a.Details.Diffs = append(a.Details.Diffs, store.ApprovalDiff{Path: d.Path, Old: red.String(d.Old), New: red.String(d.New)})
	}
	if s.Type == store.ApprovalRequest {
		meta := s.Meta
		meta.Editable = nil
		for _, ed := range s.Editable {
			meta.Editable = append(meta.Editable, store.RequestEditable{Key: ed.Key, Value: red.Bytes(ed.Value)})
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		a.Request = string(raw)
	}

	flowID, dry, err := e.linkApproval(ctx, a, s)
	if err != nil {
		return nil, err
	}
	dry = dry || s.DryRun
	a.Details.DryRun = dry
	if err := e.holdAtSteerCap(ctx, a, s.HeldNote); err != nil {
		return nil, err
	}

	var ch chan agent.ToolDecision
	if s.Wait {
		// Registered before the approval exists, so a decision cannot arrive before its waiter does.
		ch = make(chan agent.ToolDecision, 1)
		e.waitMu.Lock()
		e.waiters[a.ID] = ch
		e.waitMu.Unlock()
	}
	release := func() {
		e.waitMu.Lock()
		delete(e.waiters, a.ID)
		e.waitMu.Unlock()
	}
	fail := func(err error) (*Raised, error) {
		release()
		return nil, err
	}

	if s.Permission != nil {
		p := s.Permission
		if p.AgentRunID == "" {
			p.AgentRunID = s.AgentRunID
		}
		if err := e.st.CreatePermissionRequest(e.dbCtx, p); err != nil {
			return fail(err)
		}
		a.PermissionRequestID = p.ID
	}
	if err := e.st.CreateApproval(e.dbCtx, a); err != nil {
		if s.Permission != nil {
			now := e.st.Now()
			p := *s.Permission
			p.Decision, p.DecidedBy, p.DecidedAt, p.Reason = store.PermReject, store.DecidedByPolicy, &now, "approval queue error: "+err.Error()
			if uerr := e.st.UpdatePermissionRequest(e.dbCtx, &p); uerr != nil {
				e.log.Warn("engine: reject permission request of a failed approval", "permission_request", p.ID, "err", uerr)
			}
		}
		return fail(err)
	}

	if s.Wait && a.Type == store.ApprovalToolCall {
		e.markToolWait(a.AgentRunID, s.NodeRunID, true)
		e.toolApprovalPending(a.AgentRunID, toolCallIDOf(e, a, s.Permission), a.ID)
	}

	noteDesc := a.Description
	if a.Held() {
		noteDesc = "Held for your review: steering limit reached. " + noteDesc
	}
	note := notify.Approval(notify.ApprovalNote{
		FlowID: flowID, ApprovalID: a.ID, Type: a.Type, Title: a.Title, Description: noteDesc,
		Priority: a.Priority, DryRun: dry, Editable: len(s.Editable) > 0,
	}, e.url(ApprovalPath(a.ID)))
	if a.Origin == store.OriginMCP {
		note = notify.FromMCP(note, a.OriginClient, "")
	}
	e.notify(note)
	e.log.Info("engine: approval raised", "approval", a.ID, "type", a.Type, "origin", a.Origin, "title", a.Title)

	r := &Raised{Approval: a, Release: release}
	if ch != nil {
		r.Decision = ch
	}
	return r, nil
}

func (s *ApprovalSpec) validate() error {
	if strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("%w: approval title required", store.ErrInvalid)
	}
	desc := strings.TrimSpace(s.Description)
	if desc == "" {
		return ErrDescriptionRequired
	}
	if n := utf8.RuneCountInString(desc); n > store.MaxApprovalDescription {
		return fmt.Errorf("%w: description is %d characters; the limit is %d: shorten it to one or two sentences", store.ErrInvalid, n, store.MaxApprovalDescription)
	}
	if s.DescriptionBy != "" && s.DescriptionBy != store.DescByHal && s.DescriptionBy != store.DescByAgent {
		return fmt.Errorf("%w: description_by %q", store.ErrInvalid, s.DescriptionBy)
	}
	switch s.Type {
	case store.ApprovalToolCall:
		if s.AgentRunID == "" || s.Permission == nil {
			return fmt.Errorf("%w: a tool call approval needs an agent run and a permission request", store.ErrInvalid)
		}
	case store.ApprovalProposal:
		if s.AgentRunID == "" || strings.TrimSpace(s.Proposal) == "" {
			return fmt.Errorf("%w: a proposal approval needs an agent run and the proposal text", store.ErrInvalid)
		}
	case store.ApprovalRequest:
		if s.AgentRunID == "" && s.NodeRunID == "" && s.Origin != store.OriginMCP {
			return fmt.Errorf("%w: a request approval needs an agent run, a node run or origin mcp", store.ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: approval type %q", store.ErrInvalid, s.Type)
	}
	return nil
}

// linkApproval fills what the runs say about an approval: its origin and label, the conversation it belongs to and
// the message it was raised at. It returns the flow id for the notification and whether the run is a dry run.
func (e *Engine) linkApproval(ctx context.Context, a *store.Approval, s ApprovalSpec) (flowID string, dry bool, err error) {
	nodeRunID := s.NodeRunID
	var chatTitle string
	if s.AgentRunID != "" {
		ar, err := e.st.GetAgentRun(ctx, s.AgentRunID)
		if err != nil {
			return "", false, fmt.Errorf("agent run of the approval: %w", err)
		}
		a.NodeRunID = "" // the agent run carries the lineage
		if nodeRunID == "" {
			nodeRunID = ar.NodeRunID
		}
		if ar.AssistSessionID != "" {
			a.ConversationID, a.ConversationKind = ar.AssistSessionID, store.ConvChat
			sess, err := e.st.GetAssistSession(ctx, ar.AssistSessionID)
			if err != nil {
				return "", false, fmt.Errorf("chat session of the approval: %w", err)
			}
			if a.Origin == "" {
				a.Origin = store.OriginChat
				if sess.ForkedFromAgentRunID != "" {
					a.Origin = store.OriginContinuedChat
				}
			}
			chatTitle = sess.Title
			if a.Anchor, err = e.st.NewestAssistMessageID(ctx, ar.AssistSessionID); err != nil {
				return "", false, err
			}
		} else {
			root, err := e.conversationRoot(ctx, ar)
			if err != nil {
				return "", false, err
			}
			a.ConversationID, a.ConversationKind = root, store.ConvFlow
			if a.Origin == "" {
				a.Origin = store.OriginFlow
			}
			seq, err := e.st.LastAgentEventSeq(ctx, ar.ID)
			if err != nil {
				return "", false, err
			}
			if seq > 0 {
				a.Anchor = conversation.MessageID(ar.ID, seq)
			}
		}
	}
	if a.Origin == "" {
		a.Origin = store.OriginFlow // a node's request approval or gate
	}

	flowID, dry = "assistant", false
	var nodeID string
	switch {
	case a.Origin == store.OriginMCP:
		flowID = notify.MCPFlowID
	case nodeRunID != "":
		nr, err := e.st.GetNodeRun(ctx, nodeRunID)
		if err != nil {
			return "", false, fmt.Errorf("node run of the approval: %w", err)
		}
		nodeID = nr.NodeID
		flowID, dry = e.flowRunInfo(nr.FlowRunID)
	}
	if s.FlowID != "" {
		flowID = s.FlowID
	}
	if a.OriginLabel == "" {
		switch {
		case s.OriginLabel != "":
			a.OriginLabel = s.OriginLabel
		case a.Origin == store.OriginMCP:
			a.OriginLabel = "MCP: " + cmp.Or(a.OriginClient, "client")
		case a.Origin == store.OriginContinuedChat:
			a.OriginLabel = "Continued chat: " + cmp.Or(chatTitle, "untitled")
		case a.ConversationKind == store.ConvChat:
			a.OriginLabel = "Chat: " + cmp.Or(chatTitle, "untitled")
		case nodeID != "":
			a.OriginLabel = flowID + " / " + nodeID
		}
	}
	return flowID, dry, nil
}

// conversationRoot is the id of the conversation of a flow agent run. The revisions of a denied proposal are runs of
// their own that continue the conversation of the run they revise, so the steering cap counts across them; every
// other run is its own conversation.
func (e *Engine) conversationRoot(ctx context.Context, ar *store.AgentRun) (string, error) {
	for ar.Phase == store.PhasePropose && ar.PrevAgentRunID != "" {
		prev, err := e.st.GetAgentRun(ctx, ar.PrevAgentRunID)
		if err != nil {
			return "", fmt.Errorf("earlier proposal run of the conversation: %w", err)
		}
		ar = prev
	}
	return ar.ID, nil
}

// holdAtSteerCap raises an approval of a conversation held when the conversation already took max_steers_per_run
// steered denials (denials with a comment): the user reviews it without steering, and a denial tells the agent to
// stop and wait. forced is a note the caller already decided on. Approvals without a conversation (node gates,
// Request approval nodes, MCP clients) have no cap.
func (e *Engine) holdAtSteerCap(ctx context.Context, a *store.Approval, forced string) error {
	note := forced
	if note == "" && a.ConversationID != "" {
		limit := e.snapshot().cfg.MaxSteers()
		n, err := e.st.CountSteeredDenials(ctx, a.ConversationID)
		if err != nil {
			return err
		}
		if n >= limit {
			note = fmt.Sprintf("Steering limit reached: %d steered denials in this conversation. This action is held for your review; the agent was told to stop and wait.", limit)
		}
	}
	if note == "" {
		return nil
	}
	a.HeldNote = e.snapshot().red.String(note)
	if a.Priority != store.PriorityUrgent {
		a.Priority = store.PriorityHigh
	}
	return nil
}

// clipTitle cuts an approval title to one line of at most 200 characters.
func clipTitle(s string) string {
	return tools.ClipRunes(strings.Join(strings.Fields(s), " "), 200)
}

// toolCallIDOf is the permission request's tool call id, which links the approval to the tool message of a chat.
func toolCallIDOf(e *Engine, a *store.Approval, p *store.PermissionRequest) string {
	if p != nil {
		return p.ToolCallID
	}
	p, err := e.st.GetPermissionRequest(e.dbCtx, a.PermissionRequestID)
	if err != nil {
		return ""
	}
	return p.ToolCallID
}

// awaitDecision blocks on a raised tool call approval: until the user decides it, or ctx ends (the approval timeout
// or a cancelled run), which expires it. It undoes what RaiseApproval did for the waiting run.
func (e *Engine) awaitDecision(ctx context.Context, r *Raised, runID, nodeRunID string) (agent.ToolDecision, error) {
	defer r.Release()
	allowed := false
	defer func() { e.toolApprovalResolved(runID, r.Approval.ID, allowed) }()
	select {
	case d := <-r.Decision:
		allowed = d.Verdict == agent.VerdictAllow
		e.markToolWait(runID, nodeRunID, false)
		return d, nil
	case <-ctx.Done():
		// Nobody answered in time (or the run was cancelled): the approval can no longer be granted.
		_, err := e.st.DecideApproval(e.dbCtx, r.Approval.ID, store.ApprovalDecision{
			Status: store.ApprovalExpired, DecidedBy: "system", Comment: "no decision: " + cancelText(ctx),
		})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			e.log.Warn("engine: expire tool approval", "approval", r.Approval.ID, "err", err)
		}
		return agent.ToolDecision{}, ctx.Err()
	}
}
