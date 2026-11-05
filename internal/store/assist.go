package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AssistStatus is the state of an assist session.
type AssistStatus string

// Assist session states.
const (
	AssistIdle             AssistStatus = "idle"
	AssistRunning          AssistStatus = "running"
	AssistAwaitingApproval AssistStatus = "awaiting_approval"
	AssistFailed           AssistStatus = "failed"
	AssistClosed           AssistStatus = "closed"
)

func (s AssistStatus) valid() bool {
	switch s {
	case AssistIdle, AssistRunning, AssistAwaitingApproval, AssistFailed, AssistClosed:
		return true
	}
	return false
}

// AssistTargetSensor is the only assist target kind: a sensor definition.
const AssistTargetSensor = "sensor"

// Who last wrote an assist draft.
const (
	DraftByAgent = "agent"
	DraftByUser  = "user"
)

// AssistSession is one AI-assisted authoring conversation. The draft lives
// in the session's draft directory on disk (the agent edits that file); the
// row holds its latest validated copy so reads and the SSE stream need no
// filesystem access.
type AssistSession struct {
	ID         string       `json:"id"`
	TargetKind string       `json:"target_kind"`
	TargetID   string       `json:"target_id,omitempty"` // empty: a new sensor
	Runtime    string       `json:"runtime"`
	Model      string       `json:"model,omitempty"`
	Status     AssistStatus `json:"status"`
	Error      string       `json:"error,omitempty"`

	DraftYAML        string          `json:"draft_yaml"`
	DraftHash        string          `json:"draft_hash"`
	DraftSensorID    string          `json:"draft_sensor_id,omitempty"`
	DraftDiagnostics json.RawMessage `json:"draft_diagnostics,omitempty"` // JSON array of diagnostics
	DraftUpdatedAt   time.Time       `json:"draft_updated_at"`
	DraftUpdatedBy   string          `json:"draft_updated_by"`

	// LastAgentRunID is the agent run of the latest turn.
	LastAgentRunID string `json:"last_agent_run_id,omitempty"`
	// ACPSessionID is the agent's own session id, kept so a later process can session/load it.
	ACPSessionID string `json:"acp_session_id,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// AssistMessage is one entry of an assist conversation. Text is nil for tool
// messages; ToolCall is nil for text messages.
type AssistMessage struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	Role       string          `json:"role"` // user | assistant | tool | system
	Text       *string         `json:"text,omitempty"`
	AgentRunID string          `json:"agent_run_id,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCall   json.RawMessage `json:"tool_call,omitempty"`
	ApprovalID string          `json:"approval_id,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

func validAssistRole(r string) bool {
	switch r {
	case "user", "assistant", "tool", "system":
		return true
	}
	return false
}

const assistCols = `id, target_kind, target_id, runtime, model, status, error, draft_yaml, draft_hash, draft_sensor_id, draft_diagnostics,
	draft_updated_at, draft_updated_by, last_agent_run_id, acp_session_id, created_at, updated_at, closed_at`

func scanAssistSession(r rowScanner) (AssistSession, error) {
	var (
		a                         AssistSession
		status, diags             string
		draftAt, created, updated string
		closed                    sql.NullString
	)
	if err := r.Scan(&a.ID, &a.TargetKind, &a.TargetID, &a.Runtime, &a.Model, &status, &a.Error, &a.DraftYAML, &a.DraftHash, &a.DraftSensorID,
		&diags, &draftAt, &a.DraftUpdatedBy, &a.LastAgentRunID, &a.ACPSessionID, &created, &updated, &closed); err != nil {
		return AssistSession{}, scanErr(err)
	}
	a.Status, a.DraftDiagnostics = AssistStatus(status), parseRaw(diags)
	var err error
	if a.DraftUpdatedAt, err = parseTS(draftAt); err != nil {
		return AssistSession{}, err
	}
	if a.CreatedAt, err = parseTS(created); err != nil {
		return AssistSession{}, err
	}
	if a.UpdatedAt, err = parseTS(updated); err != nil {
		return AssistSession{}, err
	}
	if a.ClosedAt, err = parseTSPtr(closed); err != nil {
		return AssistSession{}, err
	}
	return a, nil
}

func (a *AssistSession) validate() error {
	switch {
	case a.TargetKind != AssistTargetSensor:
		return fmt.Errorf("%w: assist target kind %q", ErrInvalid, a.TargetKind)
	case a.Runtime == "":
		return fmt.Errorf("%w: assist session runtime required", ErrInvalid)
	case !a.Status.valid():
		return fmt.Errorf("%w: assist status %q", ErrInvalid, a.Status)
	case a.DraftHash == "":
		return fmt.Errorf("%w: assist draft hash required", ErrInvalid)
	case a.DraftUpdatedBy != DraftByAgent && a.DraftUpdatedBy != DraftByUser:
		return fmt.Errorf("%w: assist draft author %q", ErrInvalid, a.DraftUpdatedBy)
	}
	return nil
}

// CreateAssistSession inserts a, assigning ID, Status (idle), CreatedAt, UpdatedAt and DraftUpdatedAt when unset.
func (s *Store) CreateAssistSession(ctx context.Context, a *AssistSession) error {
	if a.Status == "" {
		a.Status = AssistIdle
	}
	if err := a.validate(); err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = s.NewID()
	}
	now := s.Now()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = a.CreatedAt
	if a.DraftUpdatedAt.IsZero() {
		a.DraftUpdatedAt = a.CreatedAt
	}
	diags, err := rawJSONArray(a.DraftDiagnostics)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO assist_sessions (`+assistCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.TargetKind, a.TargetID, a.Runtime, a.Model, a.Status, a.Error, a.DraftYAML, a.DraftHash, a.DraftSensorID, diags,
			ts(a.DraftUpdatedAt), a.DraftUpdatedBy, a.LastAgentRunID, a.ACPSessionID, ts(a.CreatedAt), ts(a.UpdatedAt), tsOrNil(a.ClosedAt))
		if err != nil {
			return fmt.Errorf("store: insert assist_session: %w", err)
		}
		t.note(ResAssistSession, a.ID, OpCreate)
		return nil
	})
}

// rawJSONArray returns m as stored text, "[]" when empty.
func rawJSONArray(m json.RawMessage) (string, error) {
	if len(m) == 0 {
		return "[]", nil
	}
	if !json.Valid(m) {
		return "", fmt.Errorf("%w: diagnostics are not valid JSON", ErrInvalid)
	}
	return string(m), nil
}

// UpdateAssistSession writes every mutable column (status, error, the draft,
// last_agent_run_id, acp_session_id, closed_at) and sets UpdatedAt to now.
func (s *Store) UpdateAssistSession(ctx context.Context, a *AssistSession) error {
	if err := a.validate(); err != nil {
		return err
	}
	diags, err := rawJSONArray(a.DraftDiagnostics)
	if err != nil {
		return err
	}
	a.UpdatedAt = s.Now()
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE assist_sessions SET model=?, status=?, error=?, draft_yaml=?, draft_hash=?, draft_sensor_id=?, draft_diagnostics=?,
			draft_updated_at=?, draft_updated_by=?, last_agent_run_id=?, acp_session_id=?, updated_at=?, closed_at=? WHERE id=?`,
			a.Model, a.Status, a.Error, a.DraftYAML, a.DraftHash, a.DraftSensorID, diags,
			ts(a.DraftUpdatedAt), a.DraftUpdatedBy, a.LastAgentRunID, a.ACPSessionID, ts(a.UpdatedAt), tsOrNil(a.ClosedAt), a.ID)
		if err != nil {
			return fmt.Errorf("store: update assist_session: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResAssistSession, a.ID, OpUpdate)
		return nil
	})
}

// GetAssistSession returns one session.
func (s *Store) GetAssistSession(ctx context.Context, id string) (*AssistSession, error) {
	a, err := scanAssistSession(s.rdb.QueryRowContext(ctx, "SELECT "+assistCols+" FROM assist_sessions WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// AssistSessionFilter selects sessions for ListAssistSessions.
type AssistSessionFilter struct {
	TargetKind string
	TargetID   string
	Statuses   []AssistStatus
	// IncludeClosed also lists closed sessions (they are omitted by default).
	IncludeClosed bool
	ListOpts
}

// ListAssistSessions lists sessions, newest first.
func (s *Store) ListAssistSessions(ctx context.Context, f AssistSessionFilter) (Page[AssistSession], error) {
	var w where
	w.eq("target_kind", f.TargetKind)
	w.eq("target_id", f.TargetID)
	w.in("status", strs(f.Statuses))
	if !f.IncludeClosed {
		w.add("status <> 'closed'")
	}
	return listPage(ctx, s.rdb, "SELECT "+assistCols+" FROM assist_sessions", &w, f.ListOpts, "id",
		scanAssistSession, func(a AssistSession) string { return a.ID })
}

const assistMsgCols = `id, session_id, role, text, agent_run_id, tool_call_id, tool_call, approval_id, created_at`

func scanAssistMessage(r rowScanner) (AssistMessage, error) {
	var (
		m              AssistMessage
		text, toolCall sql.NullString
		created        string
	)
	if err := r.Scan(&m.ID, &m.SessionID, &m.Role, &text, &m.AgentRunID, &m.ToolCallID, &toolCall, &m.ApprovalID, &created); err != nil {
		return AssistMessage{}, scanErr(err)
	}
	if text.Valid {
		m.Text = &text.String
	}
	if toolCall.Valid {
		m.ToolCall = json.RawMessage(toolCall.String)
	}
	var err error
	m.CreatedAt, err = parseTS(created)
	return m, err
}

func (m *AssistMessage) validate() error {
	switch {
	case m.SessionID == "":
		return fmt.Errorf("%w: assist message session_id required", ErrInvalid)
	case !validAssistRole(m.Role):
		return fmt.Errorf("%w: assist message role %q", ErrInvalid, m.Role)
	case len(m.ToolCall) > 0 && !json.Valid(m.ToolCall):
		return fmt.Errorf("%w: assist message tool_call is not valid JSON", ErrInvalid)
	}
	return nil
}

func (m *AssistMessage) toolCallArg() any {
	if len(m.ToolCall) == 0 {
		return nil
	}
	return string(m.ToolCall)
}

func (m *AssistMessage) textArg() any {
	if m.Text == nil {
		return nil
	}
	return *m.Text
}

// CreateAssistMessage inserts m, assigning ID and CreatedAt when unset.
func (s *Store) CreateAssistMessage(ctx context.Context, m *AssistMessage) error {
	if err := m.validate(); err != nil {
		return err
	}
	if m.ID == "" {
		m.ID = s.NewID()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.Now()
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO assist_messages (`+assistMsgCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
			m.ID, m.SessionID, m.Role, m.textArg(), m.AgentRunID, m.ToolCallID, m.toolCallArg(), m.ApprovalID, ts(m.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: insert assist_message: %w", err)
		}
		t.note(ResAssistMessage, m.ID, OpCreate)
		return nil
	})
}

// UpdateAssistMessage rewrites the text, tool call and approval link of a message.
func (s *Store) UpdateAssistMessage(ctx context.Context, m *AssistMessage) error {
	if err := m.validate(); err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE assist_messages SET text=?, tool_call=?, approval_id=? WHERE id=?`,
			m.textArg(), m.toolCallArg(), m.ApprovalID, m.ID)
		if err != nil {
			return fmt.Errorf("store: update assist_message: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResAssistMessage, m.ID, OpUpdate)
		return nil
	})
}

// GetAssistMessage returns one message.
func (s *Store) GetAssistMessage(ctx context.Context, id string) (*AssistMessage, error) {
	m, err := scanAssistMessage(s.rdb.QueryRowContext(ctx, "SELECT "+assistMsgCols+" FROM assist_messages WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListAssistMessages returns the whole conversation of a session, oldest first.
func (s *Store) ListAssistMessages(ctx context.Context, sessionID string) ([]AssistMessage, error) {
	rows, err := s.rdb.QueryContext(ctx, "SELECT "+assistMsgCols+" FROM assist_messages WHERE session_id = ? ORDER BY id", sessionID)
	if err != nil {
		return nil, fmt.Errorf("store: list assist_messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AssistMessage{}
	for rows.Next() {
		m, err := scanAssistMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AssistApprovalFor returns the id of the tool-call approval raised by the
// permission request of toolCallID in agentRunID, or "" when there is none.
func (s *Store) AssistApprovalFor(ctx context.Context, agentRunID, toolCallID string) (string, error) {
	var id string
	err := s.rdb.QueryRowContext(ctx, `SELECT a.id FROM approvals a JOIN permission_requests p ON p.id = a.permission_request_id
		WHERE a.agent_run_id = ? AND p.tool_call_id = ? ORDER BY a.id DESC LIMIT 1`, agentRunID, toolCallID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}
