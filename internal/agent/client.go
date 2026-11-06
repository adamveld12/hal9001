package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
)

func osEnviron() []string { return os.Environ() }

// updateKind names a session/update variant for agent_events.kind.
func updateKind(u acp.SessionUpdate) (kind, toolCallID string) {
	switch {
	case u.AgentMessageChunk != nil:
		return "message", ""
	case u.AgentThoughtChunk != nil:
		return "thought", ""
	case u.UserMessageChunk != nil:
		return "user_message", ""
	case u.ToolCall != nil:
		return "tool_call", string(u.ToolCall.ToolCallId)
	case u.ToolCallUpdate != nil:
		return "tool_call_update", string(u.ToolCallUpdate.ToolCallId)
	case u.Plan != nil, u.PlanUpdate != nil, u.PlanRemoved != nil:
		return "plan", ""
	case u.AvailableCommandsUpdate != nil:
		return "available_commands", ""
	case u.CurrentModeUpdate != nil:
		return "mode", ""
	case u.ConfigOptionUpdate != nil:
		return "config_option", ""
	case u.SessionInfoUpdate != nil:
		return "session_info", ""
	case u.UsageUpdate != nil:
		return "usage", ""
	}
	return "unknown", ""
}

// SessionUpdate records every update as an agent event and tracks tool calls
// and the agent's message text.
func (r *run) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	u := n.Update
	kind, tcID := updateKind(u)
	payload, err := json.Marshal(u)
	if err != nil {
		return fmt.Errorf("encode session update: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.replaying {
		return nil // history replayed by session/load is already recorded
	}
	switch {
	case u.AgentMessageChunk != nil:
		if t := u.AgentMessageChunk.Content.Text; t != nil {
			r.cur.WriteString(t.Text)
			r.full.WriteString(t.Text)
		}
	case u.AgentThoughtChunk != nil, u.ToolCall != nil, u.ToolCallUpdate != nil:
		r.cur.Reset()
	}
	switch {
	case u.ToolCall != nil:
		tc := u.ToolCall
		ti := &toolInfo{id: string(tc.ToolCallId), kind: string(tc.Kind), title: tc.Title, rawIn: tc.RawInput, status: string(tc.Status), locations: locationPaths(tc.Locations)}
		ti.content, _ = marshalOpt(tc.Content)
		r.tools[ti.id] = ti
		r.toolOrder = append(r.toolOrder, ti.id)
	case u.ToolCallUpdate != nil:
		tc := u.ToolCallUpdate
		ti := r.tools[string(tc.ToolCallId)]
		if ti == nil {
			ti = &toolInfo{id: string(tc.ToolCallId)}
			r.tools[ti.id] = ti
			r.toolOrder = append(r.toolOrder, ti.id)
		}
		if tc.Kind != nil {
			ti.kind = string(*tc.Kind)
		}
		if tc.Title != nil {
			ti.title = *tc.Title
		}
		if tc.RawInput != nil {
			ti.rawIn = tc.RawInput
		}
		if tc.Status != nil {
			ti.status = string(*tc.Status)
		}
		if len(tc.Content) > 0 {
			ti.content, _ = marshalOpt(tc.Content)
		}
		if len(tc.Locations) > 0 {
			ti.locations = locationPaths(tc.Locations)
		}
	}
	r.seq++
	r.e.Recorder.Event(Event{RunID: r.rec.ID, Seq: r.seq, TS: r.now(), Kind: kind, ToolCallID: tcID, Payload: payload})
	return nil
}

// locationPaths lists the paths of ACP tool call locations.
func locationPaths(locs []acp.ToolCallLocation) []string {
	var out []string
	for _, l := range locs {
		if l.Path != "" {
			out = append(out, l.Path)
		}
	}
	return out
}

func marshalOpt(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" || string(b) == "[]" {
		return nil, err
	}
	return b, nil
}

// verdict is the harness's answer to one permission prompt, independent of
// the wire format it is delivered in.
type verdict struct {
	decision string // DecisionAllow | DecisionReject | DecisionCancelled | DecisionExpired
	optionID string // chosen option for allow/reject; empty for cancelled/expired
}

// RequestPermission applies the policy (and, when gated, the approval queue)
// to one session/request_permission and records it.
func (r *run) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	tcID := string(p.ToolCall.ToolCallId)
	r.mu.Lock()
	known := r.tools[tcID]
	r.mu.Unlock()
	if known == nil || (known.title == "" && known.rawIn == nil) {
		// Some agents (codex) send a bare request and put the details in the
		// tool_call notification, which can trail the request by a moment.
		if p.ToolCall.Title == nil && p.ToolCall.RawInput == nil {
			known = r.awaitToolDetails(tcID)
		}
	}

	kind, title, rawIn := "", "", p.ToolCall.RawInput
	if p.ToolCall.Kind != nil {
		kind = string(*p.ToolCall.Kind)
	}
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
	content, _ := marshalOpt(p.ToolCall.Content)
	locations := locationPaths(p.ToolCall.Locations)
	if known != nil {
		kind = firstNonEmpty(kind, known.kind)
		title = firstNonEmpty(title, known.title)
		if rawIn == nil {
			rawIn = known.rawIn
		}
		if content == nil {
			content = known.content
		}
		locations = append(locations, known.locations...)
	}
	var opts []PermissionOption
	for _, o := range p.Options {
		opts = append(opts, PermissionOption{ID: string(o.OptionId), Name: o.Name, Kind: string(o.Kind)})
	}
	v := r.decide(permissionCtx{toolCallID: tcID, kind: kind, title: title, rawIn: rawIn, content: content, locations: locations, options: opts})
	if v.decision == DecisionAllow || v.decision == DecisionReject {
		return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: acp.PermissionOptionId(v.optionID)}}}, nil
	}
	return cancelledResponse(), nil
}

func cancelledResponse() acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// permissionCtx is everything known about one prompt for approval.
type permissionCtx struct {
	toolCallID string
	kind       string
	title      string
	name       string // tool name when the transport gives one (elicitation)
	rawIn      any
	content    json.RawMessage
	locations  []string // ACP tool call locations
	options    []PermissionOption
}

// decide runs the policy and records the permission request. For gated tools
// it blocks on the approval broker.
func (r *run) decide(pc permissionCtx) verdict {
	if pc.kind == "" {
		pc.kind = string(acp.ToolKindOther)
	}
	rawJSON, _ := marshalOpt(pc.rawIn)
	pr := PermissionRecord{
		ID:          newID(),
		RunID:       r.rec.ID,
		ToolCallID:  pc.toolCallID,
		ToolKind:    pc.kind,
		Title:       firstNonEmpty(pc.title, pc.name),
		RawInput:    rawJSON,
		Content:     pc.content,
		Options:     pc.options,
		RequestedAt: r.now(),
	}

	if r.ctx.Err() != nil {
		return r.finish1(pr, DecisionCancelled, DecidedByPolicy, "run was already cancelled: "+cancelWhy(r.ctx))
	}
	d := r.engine.Decide(ToolRequest{Kind: pc.kind, Title: pc.title, Name: pc.name, RawInput: pc.rawIn, ToolCallID: pc.toolCallID})
	pr.MatchedRule, pr.Reason = d.Rule, d.Reason
	return r.respond(pr, d.Action == ActionAllow, DecidedByPolicy)
}

func cancelWhy(ctx context.Context) string {
	if c := context.Cause(ctx); c != nil {
		return c.Error()
	}
	return "cancelled"
}

func (r *run) savePermission(pr PermissionRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.e.Recorder.Permission(pr)
}

// finish1 records a final decision that carries no option choice.
func (r *run) finish1(pr PermissionRecord, decision, by, reason string) verdict {
	now := r.now()
	pr.Decision, pr.DecidedBy, pr.DecidedAt, pr.Reason = decision, by, &now, reason
	r.savePermission(pr)
	return verdict{decision: decision}
}

// respond answers allow or reject by choosing the agent's matching option. An
// agent that offers no option of the wanted polarity gets `cancelled`, the
// only reply that cannot grant anything.
func (r *run) respond(pr PermissionRecord, allow bool, by string) verdict {
	id, ok := pickOption(pr.Options, allow)
	if !ok {
		want := map[bool]string{true: "allow", false: "reject"}[allow]
		return r.finish1(pr, DecisionCancelled, by, pr.Reason+"; agent offered no "+want+" option, answered cancelled")
	}
	decision := DecisionReject
	if allow {
		decision = DecisionAllow
	}
	now := r.now()
	pr.Decision, pr.DecidedBy, pr.DecidedAt = decision, by, &now
	r.savePermission(pr)
	if !allow {
		r.mu.Lock()
		r.lastReject = &rejection{by: by, title: pr.Title}
		r.mu.Unlock()
	}
	return verdict{decision: decision, optionID: id}
}

// pickOption prefers the one-shot options so a decision never persists in the
// agent's own settings.
func pickOption(opts []PermissionOption, allow bool) (string, bool) {
	// Only one-shot allows: allow_always / allow-for-session options persist a
	// rule in the agent's own settings (codex's execpolicy amendment).
	order := []string{"reject_once", "reject_always"}
	if allow {
		order = []string{"allow_once"}
	}
	for _, kind := range order {
		for _, o := range opts {
			if o.Kind == kind {
				return o.ID, true
			}
		}
	}
	return "", false
}

// Elicitation: some runtimes (omp) ask for approval of non-execute tools
// through an `elicitation/create` form instead of session/request_permission.
// It is only advertised when RuntimeConfig.Elicitation is set.

const elicitationToolPrefix = "Allow tool: "

// UnstableCreateElicitation answers a permission-style form elicitation under
// the same policy as session/request_permission; anything else is declined.
func (r *run) UnstableCreateElicitation(_ context.Context, req acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	decline := acp.UnstableCreateElicitationResponse{Decline: &acp.UnstableCreateElicitationDecline{Action: "decline"}}
	if !r.p.req.Config.Elicitation || req.Form == nil || !strings.HasPrefix(req.Form.Message, elicitationToolPrefix) {
		return decline, nil
	}
	first, _, _ := strings.Cut(req.Form.Message, "\n")
	name := strings.TrimSpace(strings.TrimPrefix(first, elicitationToolPrefix))
	field, allowValue := elicitationChoice(req.Form.RequestedSchema)

	ti := r.pendingTool()
	pc := permissionCtx{
		name: name,
		options: []PermissionOption{
			{ID: allowValue, Name: allowValue, Kind: string(acp.PermissionOptionKindAllowOnce)},
			{ID: "decline", Name: "Deny", Kind: string(acp.PermissionOptionKindRejectOnce)},
		},
	}
	if ti != nil {
		pc.toolCallID, pc.kind, pc.title, pc.rawIn, pc.content, pc.locations = ti.id, ti.kind, ti.title, ti.rawIn, ti.content, ti.locations
	}
	v := r.decide(pc)
	switch v.decision {
	case DecisionAllow:
		return acp.UnstableCreateElicitationResponse{Accept: &acp.UnstableCreateElicitationAccept{Action: "accept", Content: map[string]any{field: v.optionID}}}, nil
	case DecisionReject:
		return decline, nil
	}
	return acp.UnstableCreateElicitationResponse{Cancel: &acp.UnstableCreateElicitationCancel{Action: "cancel"}}, nil
}

// pendingTool finds the tool call an elicitation is about: the latest one that
// has not finished. The tool_call notification can trail the request by a
// moment, so wait briefly for it; without it the kind stays unknown ("other"),
// which the policy treats conservatively.
func (r *run) pendingTool() *toolInfo {
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		for i := len(r.toolOrder) - 1; i >= 0; i-- {
			if ti := r.tools[r.toolOrder[i]]; ti.status != "completed" && ti.status != "failed" {
				cp := *ti
				r.mu.Unlock()
				return &cp
			}
		}
		r.mu.Unlock()
		if time.Now().After(deadline) || r.ctx.Err() != nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// elicitationChoice finds the form's select field and the value meaning
// "approve this call" (exactly "Approve", else the first Approve*/Allow*).
func elicitationChoice(schema acp.UnstableElicitationSchema) (field, allowValue string) {
	field, allowValue = "value", "Approve"
	b, err := json.Marshal(schema)
	if err != nil {
		return field, allowValue
	}
	var s struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if json.Unmarshal(b, &s) != nil {
		return field, allowValue
	}
	for name, p := range s.Properties {
		if len(p.Enum) == 0 {
			continue
		}
		field = name
		for _, v := range p.Enum {
			if v == "Approve" {
				return field, v
			}
		}
		for _, v := range p.Enum {
			if strings.HasPrefix(v, "Approve") || strings.HasPrefix(v, "Allow") {
				return field, v
			}
		}
	}
	return field, allowValue
}

// UnstableCompleteElicitation ignores URL-elicitation completion notices.
func (r *run) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil
}

// noCapabilities answers the client methods of capabilities we never
// advertise (fs, terminal). run and the session's router embed it.
type noCapabilities struct{}

func (noCapabilities) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsReadTextFile)
}

func (noCapabilities) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound(acp.ClientMethodFsWriteTextFile)
}

func (noCapabilities) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalCreate)
}

func (noCapabilities) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalKill)
}

func (noCapabilities) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalOutput)
}

func (noCapabilities) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalRelease)
}

func (noCapabilities) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound(acp.ClientMethodTerminalWaitForExit)
}
