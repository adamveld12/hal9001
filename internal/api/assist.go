package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Assist endpoints (AI-assisted sensor authoring). Reads come straight from the
// store: the manager keeps the session row, its draft and its messages current,
// so views need no filesystem or agent access. Everything that starts a turn,
// runs the draft or changes it goes through core.Assist.

const maxAssistText = 64 << 10

// assistAgentHeader marks a draft test requested by the agent (the CLI sends it inside an assist session).
const assistAgentHeader = "X-Hal9001-Assist-Agent"

type assistTargetView struct {
	Kind string  `json:"kind"`
	ID   *string `json:"id"`
}

type assistDraftView struct {
	Kind        string          `json:"kind"`
	YAML        string          `json:"yaml"`
	Hash        string          `json:"hash"`
	SensorID    *string         `json:"sensor_id"`
	FlowID      *string         `json:"flow_id"`
	ToolID      *string         `json:"tool_id"`
	Prompts     json.RawMessage `json:"prompts"`
	Diagnostics json.RawMessage `json:"diagnostics"`
	UpdatedAt   time.Time       `json:"updated_at"`
	UpdatedBy   string          `json:"updated_by"`
}

type assistSessionView struct {
	ID             string           `json:"id"`
	Target         assistTargetView `json:"target"`
	Runtime        string           `json:"runtime"`
	Model          *string          `json:"model"`
	Status         string           `json:"status"`
	Error          *string          `json:"error"`
	Draft          assistDraftView  `json:"draft"`
	LastAgentRunID *string          `json:"last_agent_run_id"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type assistMessageView struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Text       *string         `json:"text"`
	AgentRunID *string         `json:"agent_run_id"`
	ToolCall   json.RawMessage `json:"tool_call"`
	ApprovalID *string         `json:"approval_id"`
	CreatedAt  time.Time       `json:"created_at"`
}

// assistStreamMessageView is the data of `assist.message`: the message and the session it belongs to.
type assistStreamMessageView struct {
	assistMessageView
	SessionID string `json:"session_id"`
}

type assistSessionDetailView struct {
	assistSessionView
	Messages []assistMessageView `json:"messages"`
}

func assistDraft(a store.AssistSession) assistDraftView {
	v := assistDraftView{
		Kind: a.TargetKind, YAML: a.DraftYAML, Hash: a.DraftHash, Prompts: rawArray(a.DraftPrompts), Diagnostics: rawArray(a.DraftDiagnostics),
		UpdatedAt: utc(a.DraftUpdatedAt), UpdatedBy: a.DraftUpdatedBy,
	}
	switch a.TargetKind {
	case store.AssistTargetFlow:
		v.FlowID = nonEmpty(a.DraftID)
	case store.AssistTargetTool:
		v.ToolID = nonEmpty(a.DraftID)
	default:
		v.SensorID = nonEmpty(a.DraftID)
	}
	return v
}

// rawArray returns raw as a JSON array, "[]" when empty.
func rawArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("[]")
	}
	return raw
}

func assistSession(a store.AssistSession) assistSessionView {
	return assistSessionView{
		ID: a.ID, Target: assistTargetView{Kind: a.TargetKind, ID: nonEmpty(a.TargetID)}, Runtime: a.Runtime, Model: nonEmpty(a.Model),
		Status: string(a.Status), Error: nonEmpty(a.Error), Draft: assistDraft(a), LastAgentRunID: nonEmpty(a.LastAgentRunID),
		CreatedAt: utc(a.CreatedAt), UpdatedAt: utc(a.UpdatedAt),
	}
}

func assistMessage(m store.AssistMessage) assistMessageView {
	return assistMessageView{
		ID: m.ID, Role: m.Role, Text: m.Text, AgentRunID: nonEmpty(m.AgentRunID), ToolCall: rawOrNull(m.ToolCall),
		ApprovalID: nonEmpty(m.ApprovalID), CreatedAt: utc(m.CreatedAt),
	}
}

// assist returns the assist service, or answers 503 when the daemon runs without one.
func (s *Server) assist(w http.ResponseWriter) (core.Assist, bool) {
	if s.opts.Assist == nil {
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "AI-assisted authoring is not available in this daemon", nil)
		return nil, false
	}
	return s.opts.Assist, true
}

func (s *Server) listAssistSessions(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	kind := q.enum("target_kind", store.AssistTargetSensor, store.AssistTargetFlow, store.AssistTargetTool)
	targetID := q.slug("target_id")
	if !q.ok(w) {
		return
	}
	page, err := s.opts.Store.ListAssistSessions(r.Context(), store.AssistSessionFilter{
		TargetKind: kind, TargetID: targetID, ListOpts: store.ListOpts{Limit: defaultLimit},
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := struct {
		Items []assistSessionView `json:"items"`
	}{Items: make([]assistSessionView, 0, len(page.Items))}
	for _, a := range page.Items {
		out.Items = append(out.Items, assistSession(a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAssistSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"target"`
		Runtime string  `json:"runtime"`
		Model   string  `json:"model"`
		YAML    *string `json:"yaml"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	switch {
	case req.Target.Kind != store.AssistTargetSensor && req.Target.Kind != store.AssistTargetFlow && req.Target.Kind != store.AssistTargetTool:
		fieldProblem(w, "target.kind", "target.kind must be sensor, flow or tool")
		return
	case req.Target.ID != "" && !spec.ValidID(req.Target.ID):
		fieldProblem(w, "target.id", "target.id must be a sensor, flow or tool id")
		return
	case !spec.ValidID(req.Runtime):
		fieldProblem(w, "runtime", "runtime must be the name of a configured runtime")
		return
	}
	if req.Target.ID != "" {
		ws := s.opts.Workspace()
		if req.Target.Kind == store.AssistTargetFlow && ws.Flow(req.Target.ID) == nil {
			notFound(w, "no flow %q", req.Target.ID)
			return
		}
		if req.Target.Kind == store.AssistTargetTool && ws.Tool(req.Target.ID) == nil {
			notFound(w, "no tool %q", req.Target.ID)
			return
		}
		if req.Target.Kind == store.AssistTargetSensor && ws.Sensor(req.Target.ID) == nil {
			notFound(w, "no sensor %q", req.Target.ID)
			return
		}
	}
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	a, err := svc.CreateSession(r.Context(), core.AssistCreateRequest{
		TargetKind: req.Target.Kind, TargetID: req.Target.ID, Runtime: req.Runtime, Model: req.Model, YAML: req.YAML, Principal: principal(r),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/assist/sessions/"+a.ID)
	writeJSON(w, http.StatusCreated, assistSession(*a))
}

func (s *Server) getAssistSession(w http.ResponseWriter, r *http.Request) {
	a, err := s.opts.Store.GetAssistSession(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	msgs, err := s.opts.Store.ListAssistMessages(r.Context(), a.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := assistSessionDetailView{assistSessionView: assistSession(*a), Messages: make([]assistMessageView, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, assistMessage(m))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteAssistSession(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	if err := svc.Close(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sendAssistMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string  `json:"text"`
		YAML *string `json:"yaml"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	switch {
	case strings.TrimSpace(req.Text) == "":
		fieldProblem(w, "text", "text must not be empty")
		return
	case len(req.Text) > maxAssistText:
		fieldProblem(w, "text", "text is too long")
		return
	}
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	a, err := svc.SendMessage(r.Context(), r.PathValue("id"), req.Text, req.YAML)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, assistSession(*a))
}

func (s *Server) updateAssistDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		YAML         string `json:"yaml"`
		ExpectedHash string `json:"expected_hash"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.ExpectedHash != "" && !hashRE.MatchString(req.ExpectedHash) {
		fieldProblem(w, "expected_hash", "expected_hash must be the hash of a draft read from this API")
		return
	}
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	a, err := svc.UpdateDraft(r.Context(), r.PathValue("id"), req.YAML, req.ExpectedHash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, assistDraft(*a))
}

func (s *Server) testAssistDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SampleIndex *int            `json:"sample_index"`
		EventID     string          `json:"event_id"`
		Input       json.RawMessage `json:"input"`
		Args        json.RawMessage `json:"args"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if req.SampleIndex != nil && *req.SampleIndex < 0 {
		fieldProblem(w, "sample_index", "sample_index must not be negative")
		return
	}
	if req.EventID != "" && !ulidRE.MatchString(req.EventID) {
		fieldProblem(w, "event_id", "event_id must be a ULID")
		return
	}
	if string(req.Input) == "null" {
		req.Input = nil
	}
	if string(req.Args) == "null" {
		req.Args = nil
	}
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	res, err := svc.TestDraft(r.Context(), r.PathValue("id"), r.Header.Get(assistAgentHeader) != "",
		core.AssistTestRequest{SampleIndex: req.SampleIndex, EventID: req.EventID, Input: req.Input, Args: req.Args})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if res.Sensor != nil && res.Sensor.Items == nil {
		res.Sensor.Items = []core.AssistTestItem{}
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) listAssistSamples(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	items, err := svc.Samples(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []core.AssistSample `json:"items"`
	}{items})
}

func (s *Server) cancelAssistSession(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.assist(w)
	if !ok {
		return
	}
	a, err := svc.Cancel(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, assistSession(*a))
}
