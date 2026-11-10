package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Sandbox session statuses (SandboxSession.Status).
const (
	SandboxProvisioning = "provisioning"
	SandboxReady        = "ready"
	SandboxFailed       = "failed" // provisioning failed
	SandboxClosed       = "closed"
	SandboxInterrupted  = "interrupted" // the daemon stopped while the sandbox was open
)

// Sandbox log entry kinds (SandboxLogEntry.Kind).
const (
	SandboxKindLifecycle = "lifecycle" // provision, ready, clone, teardown
	SandboxKindExec      = "exec"      // a command (shell, custom tool script)
	SandboxKindFS        = "fs"        // fs_read, fs_write, fs_list, fs_search
	SandboxKindDenied    = "denied"    // a call that was not run: denied by policy, by the user, expired
	SandboxKindBackend   = "backend"   // messages from the backend itself (image pull, pod events)
)

// Sandbox log entry levels (SandboxLogEntry.Level).
const (
	SandboxInfo  = "info"
	SandboxWarn  = "warn"
	SandboxError = "error"
)

// SandboxSession is one sandbox an agent run opened: the header of its log. AgentRunID is the id the agent run gets; its row
// may not exist yet (the sandbox is provisioned first) or ever (provisioning failed).
type SandboxSession struct {
	ID          string     `json:"id"`
	NodeRunID   string     `json:"node_run_id"`
	AgentRunID  string     `json:"agent_run_id"`
	Backend     string     `json:"backend"`
	Status      string     `json:"status"`
	SandboxID   string     `json:"sandbox_id,omitempty"`
	SandboxName string     `json:"sandbox_name,omitempty"`
	Image       string     `json:"image,omitempty"`
	Repo        string     `json:"repo,omitempty"`
	Ref         string     `json:"ref,omitempty"`
	Lifecycle   string     `json:"lifecycle,omitempty"`
	Workdir     string     `json:"workdir,omitempty"`
	EnvNames    []string   `json:"env_names"`
	Error       string     `json:"error,omitempty"`
	Truncated   bool       `json:"truncated"`
	EntryCount  int        `json:"entry_count"`
	StartedAt   time.Time  `json:"started_at"`
	ReadyAt     *time.Time `json:"ready_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
}

// SandboxLogData is the kind-specific part of an entry, stored as JSON in SandboxLogEntry.Data.
type SandboxLogData struct {
	// Tool is the hal tool (shell, fs_write, a custom tool id) the entry belongs to.
	Tool string `json:"tool,omitempty"`
	// Approval says how the call was allowed or why it was not run: "auto: <reason>", "approved by user", "denied by policy: ...".
	Approval string `json:"approval,omitempty"`
	// Command is the command line (exec), or the search pattern (fs_search).
	Command    string `json:"command,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Path       string `json:"path,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
	// Bytes is the size of the file read or written, or the number of entries or hits listed.
	Bytes     *int64 `json:"bytes,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Error     string `json:"error,omitempty"`
	// Fields are small key/value details of lifecycle and backend entries (image, repo, ref, duration, ...).
	Fields map[string]string `json:"fields,omitempty"`
}

// SandboxLogEntry is one line of a sandbox's log. ID increases with insertion order across all sessions.
type SandboxLogEntry struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	TS        time.Time       `json:"ts"`
	Kind      string          `json:"kind"`
	Level     string          `json:"level"`
	Title     string          `json:"title"`
	Data      json.RawMessage `json:"data"`
}

// Decode returns the entry's kind-specific data.
func (e SandboxLogEntry) Decode() SandboxLogData {
	var d SandboxLogData
	if len(e.Data) > 0 {
		_ = json.Unmarshal(e.Data, &d)
	}
	return d
}

const sandboxSessionCols = `id, node_run_id, agent_run_id, backend, status, sandbox_id, sandbox_name, image, repo, ref, lifecycle, workdir,
	env_names, error, truncated, entry_count, started_at, ready_at, ended_at`

func scanSandboxSession(r rowScanner) (SandboxSession, error) {
	var (
		s              SandboxSession
		env            string
		trunc          int
		started        string
		ready, endedAt sql.NullString
	)
	if err := r.Scan(&s.ID, &s.NodeRunID, &s.AgentRunID, &s.Backend, &s.Status, &s.SandboxID, &s.SandboxName, &s.Image, &s.Repo, &s.Ref,
		&s.Lifecycle, &s.Workdir, &env, &s.Error, &trunc, &s.EntryCount, &started, &ready, &endedAt); err != nil {
		return SandboxSession{}, scanErr(err)
	}
	var err error
	if s.EnvNames, err = parseStrings(env); err != nil {
		return SandboxSession{}, err
	}
	s.Truncated = trunc != 0
	if s.StartedAt, err = parseTS(started); err != nil {
		return SandboxSession{}, err
	}
	if s.ReadyAt, err = parseTSPtr(ready); err != nil {
		return SandboxSession{}, err
	}
	if s.EndedAt, err = parseTSPtr(endedAt); err != nil {
		return SandboxSession{}, err
	}
	return s, nil
}

// CreateSandboxSession inserts s, assigning ID, StartedAt and Status (provisioning) when empty.
func (s *Store) CreateSandboxSession(ctx context.Context, ss *SandboxSession) error {
	if ss.NodeRunID == "" || ss.AgentRunID == "" || ss.Backend == "" {
		return fmt.Errorf("%w: node_run_id, agent_run_id and backend required", ErrInvalid)
	}
	if ss.ID == "" {
		ss.ID = s.NewID()
	}
	if ss.Status == "" {
		ss.Status = SandboxProvisioning
	}
	if ss.StartedAt.IsZero() {
		ss.StartedAt = s.Now()
	}
	return s.write(ctx, func(t *txn) error {
		_, err := t.ExecContext(ctx, `INSERT INTO sandbox_sessions (`+sandboxSessionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ss.ID, ss.NodeRunID, ss.AgentRunID, ss.Backend, ss.Status, ss.SandboxID, ss.SandboxName, ss.Image, ss.Repo, ss.Ref, ss.Lifecycle,
			ss.Workdir, stringsText(ss.EnvNames), ss.Error, b2i(ss.Truncated), ss.EntryCount, ts(ss.StartedAt), tsOrNil(ss.ReadyAt), tsOrNil(ss.EndedAt))
		if err != nil {
			return fmt.Errorf("store: insert sandbox_session: %w", err)
		}
		t.note(ResSandboxSession, ss.ID, OpCreate)
		return nil
	})
}

// UpdateSandboxSession overwrites the mutable fields of ss (everything but the ids, backend, start time and entry count).
func (s *Store) UpdateSandboxSession(ctx context.Context, ss *SandboxSession) error {
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE sandbox_sessions SET status=?, sandbox_id=?, sandbox_name=?, image=?, repo=?, ref=?, lifecycle=?, workdir=?,
			env_names=?, error=?, truncated=?, ready_at=?, ended_at=? WHERE id=?`,
			ss.Status, ss.SandboxID, ss.SandboxName, ss.Image, ss.Repo, ss.Ref, ss.Lifecycle, ss.Workdir, stringsText(ss.EnvNames), ss.Error,
			b2i(ss.Truncated), tsOrNil(ss.ReadyAt), tsOrNil(ss.EndedAt), ss.ID)
		if err != nil {
			return fmt.Errorf("store: update sandbox_session: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResSandboxSession, ss.ID, OpUpdate)
		return nil
	})
}

// GetSandboxSession returns one sandbox session.
func (s *Store) GetSandboxSession(ctx context.Context, id string) (*SandboxSession, error) {
	ss, err := scanSandboxSession(s.rdb.QueryRowContext(ctx, "SELECT "+sandboxSessionCols+" FROM sandbox_sessions WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &ss, nil
}

// SandboxFilter selects the sessions (and so the log entries) of an agent run or of a node run. Set one of them.
type SandboxFilter struct {
	AgentRunID string
	NodeRunID  string
}

func (f SandboxFilter) valid() error {
	if (f.AgentRunID == "") == (f.NodeRunID == "") {
		return fmt.Errorf("%w: set exactly one of agent_run_id and node_run_id", ErrInvalid)
	}
	return nil
}

// ListSandboxSessions returns the sessions of an agent run or node run, oldest first.
func (s *Store) ListSandboxSessions(ctx context.Context, f SandboxFilter) ([]SandboxSession, error) {
	if err := f.valid(); err != nil {
		return nil, err
	}
	var w where
	w.eq("agent_run_id", f.AgentRunID)
	w.eq("node_run_id", f.NodeRunID)
	out, err := queryAll(ctx, s.rdb, scanSandboxSession, sqlJoin("SELECT ", sandboxSessionCols, " FROM sandbox_sessions", w.clause(), " ORDER BY id"), w.args...)
	if err != nil {
		return nil, fmt.Errorf("store: list sandbox_sessions: %w", err)
	}
	if out == nil {
		out = []SandboxSession{}
	}
	return out, nil
}

// AppendSandboxLogEntry adds one entry to session sessionID, assigning e.ID and e.TS (when zero), and counts it on the session.
func (s *Store) AppendSandboxLogEntry(ctx context.Context, e *SandboxLogEntry) error {
	if e.SessionID == "" || e.Kind == "" {
		return fmt.Errorf("%w: session_id and kind required", ErrInvalid)
	}
	if e.TS.IsZero() {
		e.TS = s.Now()
	}
	if e.Level == "" {
		e.Level = SandboxInfo
	}
	if len(e.Data) == 0 {
		e.Data = json.RawMessage(`{}`)
	}
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `INSERT INTO sandbox_log_entries (session_id, ts, kind, level, title, data) VALUES (?,?,?,?,?,?)`,
			e.SessionID, ts(e.TS), e.Kind, e.Level, e.Title, string(e.Data))
		if err != nil {
			return fmt.Errorf("store: insert sandbox_log_entry: %w", err)
		}
		if e.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `UPDATE sandbox_sessions SET entry_count = entry_count + 1 WHERE id = ?`, e.SessionID); err != nil {
			return fmt.Errorf("store: count sandbox_log_entry: %w", err)
		}
		t.note(ResSandboxLog, fmt.Sprint(e.ID), OpCreate)
		return nil
	})
}

const sandboxEntryCols = `e.id, e.session_id, e.ts, e.kind, e.level, e.title, e.data`

func scanSandboxEntry(r rowScanner) (SandboxLogEntry, error) {
	var (
		e    SandboxLogEntry
		at   string
		data string
	)
	if err := r.Scan(&e.ID, &e.SessionID, &at, &e.Kind, &e.Level, &e.Title, &data); err != nil {
		return SandboxLogEntry{}, scanErr(err)
	}
	e.Data = json.RawMessage(data)
	var err error
	e.TS, err = parseTS(at)
	return e, err
}

// GetSandboxLogEntry returns one entry.
func (s *Store) GetSandboxLogEntry(ctx context.Context, id int64) (*SandboxLogEntry, error) {
	e, err := scanSandboxEntry(s.rdb.QueryRowContext(ctx, "SELECT "+sandboxEntryCols+" FROM sandbox_log_entries e WHERE e.id = ?", id))
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// SandboxEntryFilter selects entries of an agent run's or node run's sessions, in insertion order.
type SandboxEntryFilter struct {
	SandboxFilter
	// AfterID returns only entries with a greater id (exclusive).
	AfterID int64
	// Tail returns the last Limit entries (after AfterID) instead of the first.
	Tail  bool
	Limit int // default 200, max 1000
	// Slim leaves out stdout and stderr (the stream event carries only metadata).
	Slim bool
}

// ListSandboxLogEntries returns up to Limit entries in ascending id order, and whether more follow (not set with Tail,
// which returns the newest entries and says whether older ones were left out).
func (s *Store) ListSandboxLogEntries(ctx context.Context, f SandboxEntryFilter) (entries []SandboxLogEntry, more bool, err error) {
	if err := f.valid(); err != nil {
		return nil, false, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 1000)
	var w where
	w.add("e.id > ?", f.AfterID)
	if f.AgentRunID != "" {
		w.add("e.session_id IN (SELECT id FROM sandbox_sessions WHERE agent_run_id = ?)", f.AgentRunID)
	} else {
		w.add("e.session_id IN (SELECT id FROM sandbox_sessions WHERE node_run_id = ?)", f.NodeRunID)
	}
	order := "ASC"
	if f.Tail {
		order = "DESC"
	}
	rows, err := queryAll(ctx, s.rdb, scanSandboxEntry,
		sqlJoin("SELECT ", sandboxEntryCols, " FROM sandbox_log_entries e", w.clause(), " ORDER BY e.id ", order, " LIMIT ?"),
		append(append([]any{}, w.args...), limit+1)...)
	if err != nil {
		return nil, false, fmt.Errorf("store: list sandbox_log_entries: %w", err)
	}
	if more = len(rows) > limit; more {
		rows = rows[:limit]
	}
	if f.Tail {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	if f.Slim {
		for i := range rows {
			d := rows[i].Decode()
			d.Stdout, d.Stderr = "", ""
			rows[i].Data, _ = json.Marshal(d)
		}
	}
	if rows == nil {
		rows = []SandboxLogEntry{}
	}
	return rows, more, nil
}
