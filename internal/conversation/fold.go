package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/store"
)

// The folding rules follow web/src/pages/runs/transcript.ts: adjacent chunks of one kind join into one message, and a
// tool call with its updates is one message whose fields are replaced by the fields each update carries.

// msgID is the id of a folded message: the agent run id and the seq of the first event it was built from (0: the run's
// prompt), zero padded so ids sort in transcript order.
func MessageID(runID string, seq int64) string { return fmt.Sprintf("%s:%08d", runID, seq) }

func startOf(ar *store.AgentRun) time.Time {
	if ar.StartedAt != nil {
		return ar.StartedAt.UTC()
	}
	return ar.CreatedAt.UTC()
}

// toolState is a tool message while its updates are folded in.
type toolState struct {
	idx       int // index of its message in the output
	call      ToolCall
	rawOutput json.RawMessage
}

// foldRun turns one agent run into messages: the prompt is the user message, adjacent chunks of the agent's text are
// one assistant message, a tool call and its updates are one tool message (with the approval that gated it), thoughts,
// plans and bookkeeping events are left out. A proposal approval and, for the last phase of a node, the way the run
// ended are system messages at the end.
func (s Service) foldRun(ctx context.Context, ar *store.AgentRun, head bool) ([]Message, error) {
	runID := ar.ID
	var out []Message
	add := func(m Message) int {
		m.AgentRunID = &runID
		if len(m.ToolCall) == 0 {
			m.ToolCall = json.RawMessage("null")
		}
		out = append(out, m)
		return len(out) - 1
	}
	prompt, err := s.Store.ReadContent(ar.Prompt)
	if err != nil {
		return nil, err
	}
	if p := string(prompt); strings.TrimSpace(p) != "" {
		add(Message{ID: MessageID(runID, 0), Role: "user", Text: &p, CreatedAt: startOf(ar)})
	}
	approvals, err := s.approvalsByToolCall(ctx, runID)
	if err != nil {
		return nil, err
	}

	var (
		last     int64
		textIdx  = -1 // the message whose text is still growing
		textKind string
		text     strings.Builder
		tools    = map[string]*toolState{}
		order    []*toolState
	)
	flushText := func() {
		if textIdx >= 0 {
			t := text.String()
			out[textIdx].Text = &t
		}
		textIdx, textKind = -1, ""
		text.Reset()
	}
	for cursor := int64(0); ; {
		evs, err := s.Store.ListAgentEvents(ctx, store.AgentEventFilter{AgentRunID: runID, AfterSeq: cursor, Limit: 500})
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			cursor, last = ev.Seq, ev.Seq
			raw, err := s.Store.ReadContent(ev.Payload)
			if err != nil {
				return nil, err
			}
			switch ev.Kind {
			case "message", "user_message":
				chunk := chunkText(raw)
				if chunk == "" {
					continue
				}
				if textIdx < 0 || textKind != ev.Kind {
					flushText()
					role := "assistant"
					if ev.Kind == "user_message" {
						role = "user"
					}
					textIdx, textKind = add(Message{ID: MessageID(runID, ev.Seq), Role: role, CreatedAt: ev.TS.UTC()}), ev.Kind
				}
				text.WriteString(chunk)
			case "tool_call", "tool_call_update":
				flushText()
				var p map[string]json.RawMessage
				_ = json.Unmarshal(raw, &p)
				id := ev.ToolCallID
				if id == "" {
					id = jsonString(pick(p, "toolCallId", "tool_call_id"))
				}
				key := id
				if key == "" {
					key = fmt.Sprintf("seq:%d", ev.Seq) // a call without an id cannot be updated
				}
				ts := tools[key]
				if ts == nil {
					title := "Tool call"
					if id != "" {
						title += " " + id
					}
					ts = &toolState{call: ToolCall{ToolCallID: id, Title: title, Kind: "other", Status: "pending", RawInput: json.RawMessage("null"), Content: []json.RawMessage{}}}
					ts.idx = add(Message{ID: MessageID(runID, ev.Seq), Role: "tool", ApprovalID: nonEmpty(approvals[id]), CreatedAt: ev.TS.UTC()})
					tools[key] = ts
					order = append(order, ts)
				}
				applyToolFields(ts, p)
			case "harness":
				if t := harnessText(raw); t != "" {
					flushText()
					add(Message{ID: MessageID(runID, ev.Seq), Role: "system", Text: &t, CreatedAt: ev.TS.UTC()})
				}
			}
		}
		if len(evs) < 500 {
			break
		}
	}
	flushText()
	for _, ts := range order {
		c := ts.call
		if len(c.Content) == 0 && len(ts.rawOutput) > 0 && string(ts.rawOutput) != "null" {
			c.Content = ClipContent([]json.RawMessage{textPart(rawOutputText(ts.rawOutput))})
		}
		b, err := json.Marshal(c)
		if err != nil {
			return nil, fmt.Errorf("conversation: encode tool call: %w", err)
		}
		out[ts.idx].ToolCall = b
	}

	seq := last
	proposals, err := s.proposalMessages(ctx, ar, &seq)
	if err != nil {
		return nil, err
	}
	for _, m := range proposals {
		add(m)
	}
	if head && ended(ar.Status) {
		result, err := s.Store.ReadContent(ar.Result)
		if err != nil {
			return nil, err
		}
		seq++
		t := endText(ar, string(result))
		at := ar.CreatedAt.UTC()
		if ar.EndedAt != nil {
			at = ar.EndedAt.UTC()
		}
		add(Message{ID: MessageID(runID, seq), Role: "system", Text: &t, CreatedAt: at})
	}
	return out, nil
}

// ended reports whether an agent run is over.
func ended(s store.AgentStatus) bool {
	switch s {
	case store.AgentQueued, store.AgentRunning, store.AgentAwaitingApproval:
		return false
	}
	return true
}

// endText says how a run ended: the status, the error or stop reason, and the ports it emitted on.
func endText(ar *store.AgentRun, result string) string {
	if ar.Status == store.AgentSucceeded {
		t := "Run finished."
		var env struct {
			Emissions []struct {
				Port string `json:"port"`
			} `json:"emissions"`
		}
		if json.Unmarshal([]byte(result), &env) == nil && len(env.Emissions) > 0 {
			ports := make([]string, len(env.Emissions))
			for i, e := range env.Emissions {
				ports[i] = "`" + e.Port + "`"
			}
			t += " Emitted on " + strings.Join(ports, ", ") + "."
		}
		return t
	}
	t := "Run " + string(ar.Status) + "."
	switch {
	case ar.Error != "":
		t = "Run " + string(ar.Status) + ": " + ar.Error
	case ar.StopReason != "":
		t = "Run " + string(ar.Status) + ": " + ar.StopReason
	}
	return t
}

// proposalMessages are the system messages of the proposal approvals of a propose-phase run.
func (s Service) proposalMessages(ctx context.Context, ar *store.AgentRun, seq *int64) ([]Message, error) {
	var out []Message
	f := store.ApprovalFilter{AgentRunID: ar.ID, Type: store.ApprovalProposal, Slim: true, ListOpts: store.ListOpts{Limit: 100, Oldest: true}}
	page, err := s.Store.ListApprovals(ctx, f)
	if err != nil {
		return nil, err
	}
	for _, a := range page.Items {
		*seq++
		var t string
		switch a.Status {
		case store.ApprovalPending:
			t = "The proposal is waiting for approval."
		default:
			t = "The proposal was " + string(a.Status) + "."
			if a.Comment != "" {
				t += " Comment: " + a.Comment
			}
		}
		at := a.CreatedAt
		if a.DecidedAt != nil {
			at = *a.DecidedAt
		}
		id := a.ID
		out = append(out, Message{ID: MessageID(ar.ID, *seq), Role: "system", Text: &t, ApprovalID: &id, CreatedAt: at.UTC()})
	}
	return out, nil
}

// approvalsByToolCall maps the tool call ids of a run to the approval that gated them (through the permission request
// the tool call raised).
func (s Service) approvalsByToolCall(ctx context.Context, runID string) (map[string]string, error) {
	byPerm := map[string]string{}
	f := store.ApprovalFilter{AgentRunID: runID, Type: store.ApprovalToolCall, Slim: true, ListOpts: store.ListOpts{Limit: 500, Oldest: true}}
	for {
		page, err := s.Store.ListApprovals(ctx, f)
		if err != nil {
			return nil, err
		}
		for _, a := range page.Items {
			if a.PermissionRequestID != "" {
				byPerm[a.PermissionRequestID] = a.ID
			}
		}
		if page.NextCursor == "" {
			break
		}
		f.Cursor = page.NextCursor
	}
	out := map[string]string{}
	pf := store.PermissionRequestFilter{AgentRunID: runID, Slim: true, ListOpts: store.ListOpts{Limit: 500, Oldest: true}}
	for {
		page, err := s.Store.ListPermissionRequests(ctx, pf)
		if err != nil {
			return nil, err
		}
		for _, p := range page.Items {
			if id, ok := byPerm[p.ID]; ok && p.ToolCallID != "" {
				out[p.ToolCallID] = id
			}
		}
		if page.NextCursor == "" {
			break
		}
		pf.Cursor = page.NextCursor
	}
	return out, nil
}

// ---- payloads (ACP session/update, camelCase; snake_case accepted) ----

func pick(p map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := p[k]; ok {
			return v
		}
	}
	return nil
}

func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// chunkText is the text of a message chunk; a chunk that is not text becomes a marker like "[image]".
func chunkText(payload []byte) string {
	var p map[string]json.RawMessage
	if json.Unmarshal(payload, &p) != nil {
		return ""
	}
	c := pick(p, "content", "text")
	if s := jsonString(c); s != "" {
		return s
	}
	var o map[string]json.RawMessage
	if json.Unmarshal(c, &o) != nil {
		return ""
	}
	if _, ok := o["text"]; ok {
		return jsonString(o["text"])
	}
	if t := jsonString(o["type"]); t != "" {
		return "[" + t + "]"
	}
	return "[content]"
}

// applyToolFields replaces the fields of a tool call by those the update carries, as ACP defines.
func applyToolFields(ts *toolState, p map[string]json.RawMessage) {
	c := &ts.call
	if v, ok := p["title"]; ok {
		if t := jsonString(v); t != "" {
			c.Title = t
		}
	}
	if v := pick(p, "kind", "tool_kind"); v != nil {
		if k := jsonString(v); k != "" {
			c.Kind = k
		}
	}
	if v, ok := p["status"]; ok {
		if st := jsonString(v); st != "" {
			c.Status = st
		}
	}
	if v := pick(p, "rawInput", "raw_input"); len(v) > 0 && string(v) != "null" {
		c.RawInput = v
	}
	if v := pick(p, "rawOutput", "raw_output"); len(v) > 0 && string(v) != "null" {
		ts.rawOutput = v
	}
	if v, ok := p["content"]; ok {
		var parts []json.RawMessage
		if json.Unmarshal(v, &parts) == nil {
			c.Content = ClipContent(parts)
		}
	}
}

func textPart(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": text}})
	return b
}

// rawOutputText shows a tool's raw output: a string as it is, anything else as compact JSON.
func rawOutputText(raw json.RawMessage) string {
	if s := jsonString(raw); s != "" {
		return s
	}
	return string(raw)
}

// harnessText is the text of the follow-up prompts hal itself sends to the agent (recovery and output prompts); the
// other harness events (mode and model changes) are bookkeeping.
func harnessText(raw []byte) string {
	var h struct {
		Action string `json:"action"`
		Prompt string `json:"prompt"`
	}
	if json.Unmarshal(raw, &h) != nil || (h.Action != "recovery_prompt" && h.Action != "output_prompt") || strings.TrimSpace(h.Prompt) == "" {
		return ""
	}
	return "hal sent the agent a follow-up: " + h.Prompt
}
