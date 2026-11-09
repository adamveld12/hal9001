package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ---- agent runs ----

func agentRunCols(slim bool) string {
	return "id, COALESCE(node_run_id, ''), COALESCE(assist_session_id, ''), COALESCE(prev_agent_run_id, ''), runtime, agent_info, capabilities, mode, COALESCE(approve_style, ''), policy, phase, model, session_id, " +
		"COALESCE(resumed_via, ''), cwd, worktree_path, " + contentCols("prompt", slim) + ", argv, status, stop_reason, error, " +
		contentCols("result", slim) + ", " + contentCols("raw_jsonrpc", slim) + ", created_at, started_at, ended_at"
}

func scanAgentRun(r rowScanner) (AgentRun, error) {
	var (
		a                                           AgentRun
		info, caps, argv                            string
		mode, style, policy, phase, status, resumed string
		created                                     string
		started, ended                              sql.NullString
	)
	dests := []any{&a.ID, &a.NodeRunID, &a.AssistSessionID, &a.PrevAgentRunID, &a.Runtime, &info, &caps, &mode, &style, &policy, &phase, &a.Model, &a.SessionID,
		&resumed, &a.Cwd, &a.WorktreePath}
	dests = append(dests, scanDests(&a.Prompt)...)
	dests = append(dests, &argv, &status, &a.StopReason, &a.Error)
	dests = append(dests, scanDests(&a.Result)...)
	dests = append(dests, scanDests(&a.RawJSONRPC)...)
	dests = append(dests, &created, &started, &ended)
	if err := r.Scan(dests...); err != nil {
		return AgentRun{}, scanErr(err)
	}
	a.AgentInfo, a.Capabilities = parseRaw(info), parseRaw(caps)
	a.Mode, a.ApproveStyle, a.Policy, a.Phase = AgentMode(mode), ApproveStyle(style), Policy(policy), AgentPhase(phase)
	a.Status, a.ResumedVia = AgentStatus(status), ResumedVia(resumed)
	var err error
	if a.Argv, err = parseStrings(argv); err != nil {
		return AgentRun{}, err
	}
	if a.CreatedAt, err = parseTS(created); err != nil {
		return AgentRun{}, err
	}
	if a.StartedAt, err = parseTSPtr(started); err != nil {
		return AgentRun{}, err
	}
	if a.EndedAt, err = parseTSPtr(ended); err != nil {
		return AgentRun{}, err
	}
	return a, nil
}

func (a *AgentRun) validate() error {
	switch {
	case (a.NodeRunID == "") == (a.AssistSessionID == ""):
		return fmt.Errorf("%w: an agent run belongs to exactly one of node_run_id and assist_session_id", ErrInvalid)
	case a.Runtime == "":
		return fmt.Errorf("%w: runtime required", ErrInvalid)
	case a.Mode != ModeAuto && a.Mode != ModeApprove && a.Mode != ModeReadonly:
		return fmt.Errorf("%w: agent mode %q", ErrInvalid, a.Mode)
	case a.ApproveStyle != "" && a.ApproveStyle != StyleProposal && a.ApproveStyle != StylePerTool:
		return fmt.Errorf("%w: approve style %q", ErrInvalid, a.ApproveStyle)
	case a.Policy != PolicyReadonly && a.Policy != PolicyGated && a.Policy != PolicyAuto:
		return fmt.Errorf("%w: policy %q", ErrInvalid, a.Policy)
	case a.Phase != PhasePropose && a.Phase != PhaseExecute:
		return fmt.Errorf("%w: phase %q", ErrInvalid, a.Phase)
	case a.ResumedVia != "" && a.ResumedVia != ResumedLoad && a.ResumedVia != ResumedNewSession:
		return fmt.Errorf("%w: resumed_via %q", ErrInvalid, a.ResumedVia)
	case !a.Status.valid():
		return fmt.Errorf("%w: agent run status %q", ErrInvalid, a.Status)
	}
	return nil
}

// CreateAgentRun inserts a, assigning ID, Status (queued), Phase (execute for
// auto/readonly modes is the caller's choice; Phase is required) and CreatedAt.
func (s *Store) CreateAgentRun(ctx context.Context, a *AgentRun) error {
	if a.Status == "" {
		a.Status = AgentQueued
	}
	if err := a.validate(); err != nil {
		return err
	}
	if a.ID == "" {
		a.ID = s.NewID()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.Now()
	}
	info, err := rawText(a.AgentInfo)
	if err != nil {
		return err
	}
	caps, err := rawText(a.Capabilities)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.prep(ctx, "agent_runs", "", contentField{"prompt", &a.Prompt}, contentField{"result", &a.Result}, contentField{"raw_jsonrpc", &a.RawJSONRPC}); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO agent_runs (id, node_run_id, assist_session_id, prev_agent_run_id, runtime, agent_info, capabilities, mode, approve_style, policy, phase, model,
			session_id, resumed_via, cwd, worktree_path, prompt_text, prompt_ref, prompt_size, argv, status, stop_reason, error,
			result_text, result_ref, result_size, raw_jsonrpc_text, raw_jsonrpc_ref, raw_jsonrpc_size, created_at, started_at, ended_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, nullStr(a.NodeRunID), nullStr(a.AssistSessionID), nullStr(a.PrevAgentRunID), a.Runtime, info, caps, a.Mode, nullStr(string(a.ApproveStyle)), a.Policy, a.Phase, a.Model,
			a.SessionID, nullStr(string(a.ResumedVia)), a.Cwd, a.WorktreePath, a.Prompt.Text, a.Prompt.Ref, a.Prompt.Size, stringsText(a.Argv), a.Status, a.StopReason, a.Error,
			a.Result.Text, a.Result.Ref, a.Result.Size, a.RawJSONRPC.Text, a.RawJSONRPC.Ref, a.RawJSONRPC.Size,
			ts(a.CreatedAt), tsOrNil(a.StartedAt), tsOrNil(a.EndedAt))
		if err != nil {
			return fmt.Errorf("store: insert agent_run: %w", err)
		}
		t.note(ResAgentRun, a.ID, OpCreate)
		return nil
	})
}

// UpdateAgentRun writes every column except id, node_run_id, prev_agent_run_id
// and created_at.
func (s *Store) UpdateAgentRun(ctx context.Context, a *AgentRun) error {
	if err := a.validate(); err != nil {
		return err
	}
	info, err := rawText(a.AgentInfo)
	if err != nil {
		return err
	}
	caps, err := rawText(a.Capabilities)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.prep(ctx, "agent_runs", a.ID, contentField{"prompt", &a.Prompt}, contentField{"result", &a.Result}, contentField{"raw_jsonrpc", &a.RawJSONRPC}); err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `UPDATE agent_runs SET runtime=?, agent_info=?, capabilities=?, mode=?, approve_style=?, policy=?, phase=?, model=?, session_id=?,
			resumed_via=?, cwd=?, worktree_path=?, prompt_text=?, prompt_ref=?, prompt_size=?, argv=?, status=?, stop_reason=?, error=?,
			result_text=?, result_ref=?, result_size=?, raw_jsonrpc_text=?, raw_jsonrpc_ref=?, raw_jsonrpc_size=?, started_at=?, ended_at=? WHERE id=?`,
			a.Runtime, info, caps, a.Mode, nullStr(string(a.ApproveStyle)), a.Policy, a.Phase, a.Model, a.SessionID,
			nullStr(string(a.ResumedVia)), a.Cwd, a.WorktreePath, a.Prompt.Text, a.Prompt.Ref, a.Prompt.Size, stringsText(a.Argv), a.Status, a.StopReason, a.Error,
			a.Result.Text, a.Result.Ref, a.Result.Size, a.RawJSONRPC.Text, a.RawJSONRPC.Ref, a.RawJSONRPC.Size,
			tsOrNil(a.StartedAt), tsOrNil(a.EndedAt), a.ID)
		if err != nil {
			return fmt.Errorf("store: update agent_run: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResAgentRun, a.ID, OpUpdate)
		return nil
	})
}

// GetAgentRun returns one agent run with full inline content.
func (s *Store) GetAgentRun(ctx context.Context, id string) (*AgentRun, error) {
	a, err := scanAgentRun(s.rdb.QueryRowContext(ctx, "SELECT "+agentRunCols(false)+" FROM agent_runs WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// AgentRunFilter selects agent runs for ListAgentRuns.
type AgentRunFilter struct {
	NodeRunID string
	// AssistSessionID selects the turns of one assist session.
	AssistSessionID string
	FlowRunID       string
	Runtime         string
	Mode            AgentMode
	Statuses        []AgentStatus
	Time            TimeRange // on created_at
	Slim            bool      // omit inline prompt/result/raw text
	ListOpts
}

// ListAgentRuns lists agent runs, newest first.
func (s *Store) ListAgentRuns(ctx context.Context, f AgentRunFilter) (Page[AgentRun], error) {
	var w where
	w.eq("node_run_id", f.NodeRunID)
	w.eq("assist_session_id", f.AssistSessionID)
	w.eq("runtime", f.Runtime)
	w.eq("mode", string(f.Mode))
	w.in("status", strs(f.Statuses))
	w.timeRange("created_at", f.Time)
	if f.FlowRunID != "" {
		w.add("node_run_id IN (SELECT id FROM node_runs WHERE flow_run_id = ?)", f.FlowRunID)
	}
	return listPage(ctx, s.rdb, "SELECT "+agentRunCols(f.Slim)+" FROM agent_runs", &w, f.ListOpts, "id",
		scanAgentRun, func(a AgentRun) string { return a.ID })
}

// ClaimNextAgentRun atomically moves the oldest queued agent run to running
// (StartedAt = now) when fewer than maxRunning agent runs are active, and
// returns it, or nil when none is claimable or the cap is reached. Active =
// status running, plus awaiting_approval with approve style per_tool (that
// agent process stays alive while it waits). Agent runs awaiting a proposal
// approval hold no slot. maxRunning < 0 means unlimited.
//
// This is the agent-run-level counterpart of ClaimNextNodeRun's agent pool;
// use one or the other as the global max_agent_runs gate, not both.
func (s *Store) ClaimNextAgentRun(ctx context.Context, maxRunning int) (*AgentRun, error) {
	var claimed *AgentRun
	err := s.write(ctx, func(t *txn) error {
		if maxRunning >= 0 {
			var active int
			if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_runs WHERE status = 'running' OR (status = 'awaiting_approval' AND approve_style = 'per_tool')`).Scan(&active); err != nil {
				return fmt.Errorf("store: count active agent runs: %w", err)
			}
			if active >= maxRunning {
				return nil
			}
		}
		var id string
		err := t.QueryRowContext(ctx, `SELECT id FROM agent_runs WHERE status = 'queued' ORDER BY id LIMIT 1`).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: find queued agent run: %w", err)
		}
		if _, err := t.ExecContext(ctx, `UPDATE agent_runs SET status='running', started_at=? WHERE id=?`, ts(s.now()), id); err != nil {
			return fmt.Errorf("store: claim agent_run: %w", err)
		}
		a, err := scanAgentRun(t.QueryRowContext(ctx, "SELECT "+agentRunCols(false)+" FROM agent_runs WHERE id = ?", id))
		if err != nil {
			return err
		}
		claimed = &a
		t.note(ResAgentRun, id, OpUpdate)
		return nil
	})
	return claimed, err
}

// ---- agent events ----

func agentEventCols(slim bool) string {
	return "agent_run_id, seq, ts, kind, tool_call_id, " + contentCols("payload", slim)
}

func scanAgentEvent(r rowScanner) (AgentEvent, error) {
	var e AgentEvent
	var at string
	dests := []any{&e.AgentRunID, &e.Seq, &at, &e.Kind, &e.ToolCallID}
	dests = append(dests, scanDests(&e.Payload)...)
	if err := r.Scan(dests...); err != nil {
		return AgentEvent{}, scanErr(err)
	}
	var err error
	e.TS, err = parseTS(at)
	return e, err
}

// AppendAgentEvent adds one transcript entry. If ev.Seq is 0 it is assigned
// the run's next sequence number (1-based, gapless); a supplied Seq must not
// already exist. TS defaults to now.
func (s *Store) AppendAgentEvent(ctx context.Context, ev *AgentEvent) error {
	return s.AppendAgentEvents(ctx, []*AgentEvent{ev})
}

// AppendAgentEvents adds several entries in one transaction, preserving order.
func (s *Store) AppendAgentEvents(ctx context.Context, evs []*AgentEvent) error {
	for _, ev := range evs {
		if ev.AgentRunID == "" || ev.Kind == "" {
			return fmt.Errorf("%w: agent_run_id and kind required", ErrInvalid)
		}
	}
	return s.write(ctx, func(t *txn) error {
		next := map[string]int64{}
		for _, ev := range evs {
			if ev.TS.IsZero() {
				ev.TS = s.Now()
			}
			if ev.Seq <= 0 {
				n, ok := next[ev.AgentRunID]
				if !ok {
					if err := t.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM agent_events WHERE agent_run_id = ?`, ev.AgentRunID).Scan(&n); err != nil {
						return fmt.Errorf("store: next agent event seq: %w", err)
					}
				}
				ev.Seq = n
			}
			next[ev.AgentRunID] = ev.Seq + 1
			stored, err := t.prepContent(ev.Payload, nil)
			if err != nil {
				return err
			}
			ev.Payload = stored
			if _, err := t.ExecContext(ctx, `INSERT INTO agent_events (agent_run_id, seq, ts, kind, tool_call_id, payload_text, payload_ref, payload_size) VALUES (?,?,?,?,?,?,?,?)`,
				ev.AgentRunID, ev.Seq, ts(ev.TS), ev.Kind, ev.ToolCallID, stored.Text, stored.Ref, stored.Size); err != nil {
				return fmt.Errorf("store: insert agent_event: %w", err)
			}
			t.note(ResAgentEvent, fmt.Sprintf("%s/%d", ev.AgentRunID, ev.Seq), OpCreate)
		}
		return nil
	})
}

// AgentEventFilter selects transcript entries of one run, ordered by seq ascending.
type AgentEventFilter struct {
	AgentRunID string
	AfterSeq   int64    // exclusive; pass the last seq seen to continue
	Kinds      []string // restrict to these kinds
	ToolCallID string
	Limit      int  // default 50, max 500
	Slim       bool // omit inline payload text
}

// ListAgentEvents returns the entries after AfterSeq in seq order.
func (s *Store) ListAgentEvents(ctx context.Context, f AgentEventFilter) ([]AgentEvent, error) {
	if f.AgentRunID == "" {
		return nil, fmt.Errorf("%w: agent_run_id required", ErrInvalid)
	}
	var w where
	w.add("agent_run_id = ?", f.AgentRunID)
	w.add("seq > ?", f.AfterSeq)
	w.in("kind", f.Kinds)
	w.eq("tool_call_id", f.ToolCallID)
	query := sqlJoin("SELECT ", agentEventCols(f.Slim), " FROM agent_events", w.clause(), " ORDER BY seq LIMIT ?")
	rows, err := s.rdb.QueryContext(ctx, query, append(append([]any{}, w.args...), limitOf(ListOpts{Limit: f.Limit}))...)
	if err != nil {
		return nil, fmt.Errorf("store: list agent_events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AgentEvent{}
	for rows.Next() {
		e, err := scanAgentEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- permission requests ----

func permCols(slim bool) string {
	return "id, agent_run_id, tool_call_id, tool_kind, title, " + contentCols("raw_input", slim) +
		", options, COALESCE(decision, ''), COALESCE(decided_by, ''), matched_rule, reason, requested_at, decided_at"
}

func scanPerm(r rowScanner) (PermissionRequest, error) {
	var p PermissionRequest
	var opts, decision, by, requested string
	var decided sql.NullString
	dests := []any{&p.ID, &p.AgentRunID, &p.ToolCallID, &p.ToolKind, &p.Title}
	dests = append(dests, scanDests(&p.RawInput)...)
	dests = append(dests, &opts, &decision, &by, &p.MatchedRule, &p.Reason, &requested, &decided)
	if err := r.Scan(dests...); err != nil {
		return PermissionRequest{}, scanErr(err)
	}
	p.Options, p.Decision, p.DecidedBy = parseRaw(opts), PermDecision(decision), PermDecidedBy(by)
	var err error
	if p.RequestedAt, err = parseTS(requested); err != nil {
		return PermissionRequest{}, err
	}
	p.DecidedAt, err = parseTSPtr(decided)
	return p, err
}

func (p *PermissionRequest) validate() error {
	switch p.Decision {
	case "", PermAllow, PermReject, PermCancelled, PermExpired:
	default:
		return fmt.Errorf("%w: permission decision %q", ErrInvalid, p.Decision)
	}
	switch p.DecidedBy {
	case "", DecidedByPolicy, DecidedByUser, DecidedByTimeout:
	default:
		return fmt.Errorf("%w: permission decided_by %q", ErrInvalid, p.DecidedBy)
	}
	return nil
}

// CreatePermissionRequest inserts p, assigning ID and RequestedAt when unset.
// Policy-decided requests are created with Decision, DecidedBy and DecidedAt set.
func (s *Store) CreatePermissionRequest(ctx context.Context, p *PermissionRequest) error {
	if p.AgentRunID == "" {
		return fmt.Errorf("%w: agent_run_id required", ErrInvalid)
	}
	if err := p.validate(); err != nil {
		return err
	}
	if p.ID == "" {
		p.ID = s.NewID()
	}
	if p.RequestedAt.IsZero() {
		p.RequestedAt = s.Now()
	}
	opts, err := rawText(p.Options)
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.prep(ctx, "permission_requests", "", contentField{"raw_input", &p.RawInput}); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO permission_requests (id, agent_run_id, tool_call_id, tool_kind, title, raw_input_text, raw_input_ref, raw_input_size,
			options, decision, decided_by, matched_rule, reason, requested_at, decided_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.AgentRunID, p.ToolCallID, p.ToolKind, p.Title, p.RawInput.Text, p.RawInput.Ref, p.RawInput.Size,
			opts, nullStr(string(p.Decision)), nullStr(string(p.DecidedBy)), p.MatchedRule, p.Reason, ts(p.RequestedAt), tsOrNil(p.DecidedAt))
		if err != nil {
			return fmt.Errorf("store: insert permission_request: %w", err)
		}
		t.note(ResPermissionRequest, p.ID, OpCreate)
		return nil
	})
}

// UpdatePermissionRequest writes decision, decided_by, matched_rule, reason and decided_at.
// A request that already has a decision cannot be decided again (ErrConflict).
func (s *Store) UpdatePermissionRequest(ctx context.Context, p *PermissionRequest) error {
	if err := p.validate(); err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE permission_requests SET decision=?, decided_by=?, matched_rule=?, reason=?, decided_at=?
			WHERE id=? AND (decision IS NULL OR decision = ?)`,
			nullStr(string(p.Decision)), nullStr(string(p.DecidedBy)), p.MatchedRule, p.Reason, tsOrNil(p.DecidedAt), p.ID, nullStr(string(p.Decision)))
		if err != nil {
			return fmt.Errorf("store: update permission_request: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests WHERE id = ?`, p.ID).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		t.note(ResPermissionRequest, p.ID, OpUpdate)
		return nil
	})
}

// GetPermissionRequest returns one request with full raw input.
func (s *Store) GetPermissionRequest(ctx context.Context, id string) (*PermissionRequest, error) {
	p, err := scanPerm(s.rdb.QueryRowContext(ctx, "SELECT "+permCols(false)+" FROM permission_requests WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PermissionRequestFilter selects permission requests for ListPermissionRequests.
type PermissionRequestFilter struct {
	AgentRunID string
	Decision   PermDecision
	Undecided  bool
	Slim       bool
	ListOpts
}

// ListPermissionRequests lists requests, newest first (set Oldest for chronological order).
func (s *Store) ListPermissionRequests(ctx context.Context, f PermissionRequestFilter) (Page[PermissionRequest], error) {
	var w where
	w.eq("agent_run_id", f.AgentRunID)
	w.eq("decision", string(f.Decision))
	if f.Undecided {
		w.add("decision IS NULL")
	}
	return listPage(ctx, s.rdb, "SELECT "+permCols(f.Slim)+" FROM permission_requests", &w, f.ListOpts, "id",
		scanPerm, func(p PermissionRequest) string { return p.ID })
}

// ---- approvals ----

func approvalCols(slim bool) string {
	return "id, COALESCE(agent_run_id, ''), COALESCE(node_run_id, ''), type, COALESCE(permission_request_id, ''), request, " + contentCols("proposal", slim) +
		", status, comment, decided_by, created_at, expires_at, decided_at"
}

func scanApproval(r rowScanner) (Approval, error) {
	var a Approval
	var typ, status, created string
	var expires, decided sql.NullString
	dests := []any{&a.ID, &a.AgentRunID, &a.NodeRunID, &typ, &a.PermissionRequestID, &a.Request}
	dests = append(dests, scanDests(&a.Proposal)...)
	dests = append(dests, &status, &a.Comment, &a.DecidedBy, &created, &expires, &decided)
	if err := r.Scan(dests...); err != nil {
		return Approval{}, scanErr(err)
	}
	a.Type, a.Status = ApprovalType(typ), ApprovalState(status)
	var err error
	if a.CreatedAt, err = parseTS(created); err != nil {
		return Approval{}, err
	}
	if a.ExpiresAt, err = parseTSPtr(expires); err != nil {
		return Approval{}, err
	}
	a.DecidedAt, err = parseTSPtr(decided)
	return a, err
}

// CreateApproval inserts a, assigning ID, Status (pending) and CreatedAt.
// A tool_call approval must reference its permission request.
func (s *Store) CreateApproval(ctx context.Context, a *Approval) error {
	switch a.Type {
	case ApprovalRequest:
		if a.NodeRunID == "" || a.AgentRunID != "" || a.Request == "" {
			return fmt.Errorf("%w: request approval requires node_run_id and request, and no agent_run_id", ErrInvalid)
		}
	case ApprovalProposal:
	case ApprovalToolCall:
		if a.PermissionRequestID == "" {
			return fmt.Errorf("%w: tool_call approval requires permission_request_id", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: approval type %q", ErrInvalid, a.Type)
	}
	if a.Type != ApprovalRequest && a.AgentRunID == "" {
		return fmt.Errorf("%w: agent_run_id required", ErrInvalid)
	}
	if a.Status == "" {
		a.Status = ApprovalPending
	}
	if a.Status != ApprovalPending && a.Status != ApprovalApproved && a.Status != ApprovalDenied && a.Status != ApprovalExpired {
		return fmt.Errorf("%w: approval status %q", ErrInvalid, a.Status)
	}
	if a.ID == "" {
		a.ID = s.NewID()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.Now()
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.prep(ctx, "approvals", "", contentField{"proposal", &a.Proposal}); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO approvals (id, agent_run_id, node_run_id, type, permission_request_id, request, proposal_text, proposal_ref, proposal_size, status, comment, decided_by,
			created_at, expires_at, decided_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, nullStr(a.AgentRunID), nullStr(a.NodeRunID), a.Type, nullStr(a.PermissionRequestID), a.Request, a.Proposal.Text, a.Proposal.Ref, a.Proposal.Size, a.Status, a.Comment, a.DecidedBy,
			ts(a.CreatedAt), tsOrNil(a.ExpiresAt), tsOrNil(a.DecidedAt))
		if err != nil {
			return fmt.Errorf("store: insert approval: %w", err)
		}
		t.note(ResApproval, a.ID, OpCreate)
		return nil
	})
}

// GetApproval returns one approval with full proposal text.
func (s *Store) GetApproval(ctx context.Context, id string) (*Approval, error) {
	a, err := scanApproval(s.rdb.QueryRowContext(ctx, "SELECT "+approvalCols(false)+" FROM approvals WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ApprovalFilter selects approvals for ListApprovals.
type ApprovalFilter struct {
	Statuses   []ApprovalState
	Type       ApprovalType
	AgentRunID string
	NodeRunID  string // request approvals of one node run
	FlowRunID  string
	Time       TimeRange // on created_at
	Slim       bool      // omit inline proposal text
	ListOpts
}

// ListApprovals lists approvals, newest first (set Oldest for a FIFO queue).
func (s *Store) ListApprovals(ctx context.Context, f ApprovalFilter) (Page[Approval], error) {
	var w where
	w.eq("agent_run_id", f.AgentRunID)
	w.eq("node_run_id", f.NodeRunID)
	w.eq("type", string(f.Type))
	w.in("status", strs(f.Statuses))
	w.timeRange("created_at", f.Time)
	if f.FlowRunID != "" {
		w.add(`(agent_run_id IN (SELECT a.id FROM agent_runs a JOIN node_runs n ON n.id = a.node_run_id WHERE n.flow_run_id = ?)
			OR node_run_id IN (SELECT id FROM node_runs WHERE flow_run_id = ?))`, f.FlowRunID, f.FlowRunID)
	}
	return listPage(ctx, s.rdb, "SELECT "+approvalCols(f.Slim)+" FROM approvals", &w, f.ListOpts, "id",
		scanApproval, func(a Approval) string { return a.ID })
}

// ApprovalDecision is the outcome applied by DecideApproval.
type ApprovalDecision struct {
	Status    ApprovalState // approved, denied or expired
	Comment   string
	DecidedBy string // principal; "system" for expiry
	// Request, when set, replaces the approval's request JSON in the same update (a request approval's recorded edits).
	Request string
	// PermissionDecision, when set on a tool_call approval, is recorded on the
	// linked permission request in the same transaction (decided_by is "user",
	// or "timeout" for expired), with PermissionReason as the reason.
	PermissionDecision PermDecision
	PermissionReason   string
}

// DecideApproval moves a pending approval to its final status. It is a
// compare-and-set: when two callers race, one wins and the other gets
// ErrConflict, so an approval is never decided twice. ErrNotFound if absent.
func (s *Store) DecideApproval(ctx context.Context, id string, d ApprovalDecision) (*Approval, error) {
	if d.Status != ApprovalApproved && d.Status != ApprovalDenied && d.Status != ApprovalExpired {
		return nil, fmt.Errorf("%w: cannot decide an approval as %q", ErrInvalid, d.Status)
	}
	var out *Approval
	err := s.write(ctx, func(t *txn) error {
		now := s.now()
		res, err := t.ExecContext(ctx, `UPDATE approvals SET status=?, comment=?, decided_by=?, decided_at=?, request=CASE WHEN ? = '' THEN request ELSE ? END WHERE id=? AND status='pending'`,
			d.Status, d.Comment, d.DecidedBy, ts(now), d.Request, d.Request, id)
		if err != nil {
			return fmt.Errorf("store: decide approval: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var exists int
			if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE id = ?`, id).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		t.note(ResApproval, id, OpUpdate)
		a, err := scanApproval(t.QueryRowContext(ctx, "SELECT "+approvalCols(false)+" FROM approvals WHERE id = ?", id))
		if err != nil {
			return err
		}
		if d.PermissionDecision != "" && a.PermissionRequestID != "" {
			by := DecidedByUser
			if d.Status == ApprovalExpired {
				by = DecidedByTimeout
			}
			if _, err := t.ExecContext(ctx, `UPDATE permission_requests SET decision=?, decided_by=?, reason=?, decided_at=? WHERE id=? AND decision IS NULL`,
				d.PermissionDecision, by, d.PermissionReason, ts(now), a.PermissionRequestID); err != nil {
				return fmt.Errorf("store: decide permission_request: %w", err)
			}
			t.note(ResPermissionRequest, a.PermissionRequestID, OpUpdate)
		}
		out = &a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
