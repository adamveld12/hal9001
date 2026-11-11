package assist

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"hal9001/internal/agent"
	"hal9001/internal/conversation"
	"hal9001/internal/store"
)

// textFlushDelay batches streamed assistant text: every flush is a store write and an SSE event.
const textFlushDelay = 120 * time.Millisecond

// sessionRecorder wraps the engine's recorder for the agent runs of one session: it persists everything
// as usual (agent_runs, agent_events, permission_requests, raw traffic) and feeds the conversation.
type sessionRecorder struct {
	s     *session
	inner agent.Recorder
}

var _ agent.Recorder = (*sessionRecorder)(nil)

func (r *sessionRecorder) RunUpdated(rec agent.RunRecord) {
	r.inner.RunUpdated(rec)
	r.s.onRunUpdated(rec)
}

func (r *sessionRecorder) Event(ev agent.Event) {
	r.inner.Event(ev)
	r.s.onEvent(ev)
}

func (r *sessionRecorder) Permission(p agent.PermissionRecord) { r.inner.Permission(p) }

func (r *sessionRecorder) Raw(f agent.RawFrame) { r.inner.Raw(f) }

// toolMsg is the conversation entry of one tool call.
type toolMsg struct {
	msg  *store.AssistMessage
	call toolCall
}

// toolCall is the stored form of a tool call (the contract's AssistToolCall).
type toolCall struct {
	ToolCallID string            `json:"tool_call_id"`
	Title      string            `json:"title"`
	Kind       string            `json:"kind"`
	Status     string            `json:"status"`
	RawInput   json.RawMessage   `json:"raw_input"`
	Content    []json.RawMessage `json:"content"`
}

// update is the session/update payload as far as the conversation needs it.
type update struct {
	SessionUpdate string          `json:"sessionUpdate"`
	ToolCallID    string          `json:"toolCallId"`
	Title         *string         `json:"title"`
	Kind          *string         `json:"kind"`
	Status        *string         `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	Content       json.RawMessage `json:"content"`
}

type chunkContent struct {
	Content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// onEvent turns one session/update of the running turn into conversation changes.
func (s *session) onEvent(ev agent.Event) {
	if ev.Kind != "message" && ev.Kind != "tool_call" && ev.Kind != "tool_call_update" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turn
	if t == nil || t.runID != ev.RunID {
		return
	}
	switch ev.Kind {
	case "message":
		var c chunkContent
		if json.Unmarshal(ev.Payload, &c) != nil || c.Content.Type != "text" || c.Content.Text == "" {
			return
		}
		s.appendText(t, c.Content.Text)
	case "tool_call", "tool_call_update":
		var u update
		if json.Unmarshal(ev.Payload, &u) != nil || u.ToolCallID == "" {
			return
		}
		s.flushTextLocked(t) // text after this call starts a new assistant message
		t.text = nil
		s.applyToolUpdate(t, u)
	}
}

// appendText adds streamed assistant text; contiguous chunks merge into one message.
func (s *session) appendText(t *turn, chunk string) {
	if t.text == nil {
		msg := &store.AssistMessage{SessionID: s.id, Role: "assistant", AgentRunID: t.runID, Text: new("")}
		t.text = msg
		t.textBuf.Reset()
		t.textCreated = false
	}
	t.textBuf.WriteString(chunk)
	t.textDirty = true
	if t.textTimer == nil {
		t.textTimer = time.AfterFunc(textFlushDelay, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.flushTextLocked(t)
		})
	}
}

// flushTextLocked writes the pending assistant text. s.mu must be held.
func (s *session) flushTextLocked(t *turn) {
	if t.textTimer != nil {
		t.textTimer.Stop()
		t.textTimer = nil
	}
	if t.text == nil || !t.textDirty {
		return
	}
	t.textDirty = false
	text := s.redact(t.textBuf.String())
	t.text.Text = &text
	var err error
	if t.textCreated {
		err = s.m.st.UpdateAssistMessage(s.m.ctx, t.text)
	} else {
		err = s.m.st.CreateAssistMessage(s.m.ctx, t.text)
		t.textCreated = true
	}
	if err != nil {
		s.m.log.Error("assist: store assistant message", "session", s.id, "err", err)
	}
}

// applyToolUpdate creates or updates the message of a tool call. s.mu must be held.
func (s *session) applyToolUpdate(t *turn, u update) {
	tm := t.tools[u.ToolCallID]
	create := tm == nil
	if create {
		tm = &toolMsg{
			msg:  &store.AssistMessage{SessionID: s.id, Role: "tool", AgentRunID: t.runID, ToolCallID: u.ToolCallID},
			call: toolCall{ToolCallID: u.ToolCallID, Status: "pending", Kind: "other", RawInput: json.RawMessage("null"), Content: []json.RawMessage{}},
		}
		if id, ok := s.approvals[u.ToolCallID]; ok {
			tm.msg.ApprovalID = id
		}
		t.tools[u.ToolCallID] = tm
	}
	c := &tm.call
	if u.Title != nil {
		c.Title = *u.Title
	}
	if u.Kind != nil && *u.Kind != "" {
		c.Kind = *u.Kind
	}
	if u.Status != nil && *u.Status != "" {
		c.Status = *u.Status
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "null" {
		c.RawInput = u.RawInput
	}
	if len(u.Content) > 0 && string(u.Content) != "null" {
		var parts []json.RawMessage
		if json.Unmarshal(u.Content, &parts) == nil {
			c.Content = conversation.ClipContent(parts)
		}
	}
	s.writeToolLocked(tm, create)
}

// writeToolLocked stores a tool message, redacting its JSON. s.mu must be held.
func (s *session) writeToolLocked(tm *toolMsg, create bool) {
	call := tm.call
	if pt := s.pendingTests[call.ToolCallID]; pt != nil {
		call.Content = append(append([]json.RawMessage(nil), call.Content...), pt.content()...)
	}
	b, err := json.Marshal(call)
	if err != nil {
		return
	}
	tm.msg.ToolCall = s.m.redactBytes(b)
	if create {
		err = s.m.st.CreateAssistMessage(s.m.ctx, tm.msg)
	} else {
		err = s.m.st.UpdateAssistMessage(s.m.ctx, tm.msg)
	}
	if err != nil {
		s.m.log.Error("assist: store tool message", "session", s.id, "tool_call", tm.call.ToolCallID, "err", err)
	}
}

// linkApproval records that the tool call is gated by an approval. It reaches the message even when the
// approval is created before the tool call is announced. s.mu must be held.
func (s *session) linkApproval(t *turn, toolCallID, approvalID string) {
	s.approvals[toolCallID] = approvalID
	if tm := t.tools[toolCallID]; tm != nil {
		tm.msg.ApprovalID = approvalID
		s.writeToolLocked(tm, false)
	}
}

// cutUTF8 returns at most n bytes of s, cut on a character boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// recap renders the earlier conversation for a new agent session that cannot load the old one.
func recap(msgs []store.AssistMessage) string {
	const (
		keep    = 24
		perItem = 1500
	)
	if len(msgs) > keep {
		msgs = msgs[len(msgs)-keep:]
	}
	var b strings.Builder
	for _, m := range msgs {
		if m.Text == nil || strings.TrimSpace(*m.Text) == "" {
			continue
		}
		who := ""
		switch m.Role {
		case "user":
			who = "User"
		case "assistant":
			who = "You"
		default:
			continue
		}
		b.WriteString("- " + who + ": " + cutUTF8(strings.TrimSpace(*m.Text), perItem) + "\n")
	}
	return b.String()
}
