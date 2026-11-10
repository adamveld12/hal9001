package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"hal9001/internal/store/blob"
)

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// updateReturning runs an UPDATE ... RETURNING id statement and collects the ids.
func (t *txn) updateReturning(ctx context.Context, query string, args ...any) ([]string, error) {
	return queryAll(ctx, t, scanString, query, args...)
}

// InterruptResult counts the rows MarkInterrupted changed.
type InterruptResult struct {
	SensorRuns         int `json:"sensor_runs"`
	FlowRuns           int `json:"flow_runs"`
	NodeRuns           int `json:"node_runs"`
	AgentRuns          int `json:"agent_runs"`
	ExpiredApprovals   int `json:"expired_approvals"`
	ExpiredPermissions int `json:"expired_permissions"`
	AssistSessions     int `json:"assist_sessions"`
}

// MarkInterrupted reconciles persisted state after a daemon restart, in one
// transaction (REQUIREMENTS §7.2, §8):
//
//   - sensor runs, node runs, agent runs and flow runs in status running become interrupted;
//   - pending tool_call approvals and operator approvals (request approvals with no node run) become expired (the
//     agent process or MCP client waiting on them is gone), with the linked permission requests expired
//     (decided_by timeout), and the agent runs and node runs that were waiting on them become interrupted;
//   - a flow run in awaiting_approval with no node run left awaiting approval
//     becomes interrupted;
//   - queued rows and pending proposal approvals (and the runs waiting on
//     them) are left untouched: queues resume, proposals survive restarts.
//
// Call it once at startup before any worker starts.
func (s *Store) MarkInterrupted(ctx context.Context) (InterruptResult, error) {
	var res InterruptResult
	err := s.write(ctx, func(t *txn) error {
		now := ts(s.now())
		const msg = "daemon restarted"

		// 1. Tool-call approvals die with their agent process, operator approvals with the MCP call that waited.
		approvals, err := t.updateReturning(ctx, `UPDATE approvals SET status='expired', decided_by='system',
			comment = CASE WHEN comment = '' THEN ? ELSE comment END, decided_at=?
			WHERE status='pending' AND (type='tool_call' OR (type='request' AND node_run_id IS NULL)) RETURNING id`, msg, now)
		if err != nil {
			return fmt.Errorf("store: expire tool approvals: %w", err)
		}
		for _, id := range approvals {
			t.note(ResApproval, id, OpUpdate)
		}
		res.ExpiredApprovals = len(approvals)

		// 2. Agent runs that were live.
		agents, err := t.updateReturning(ctx, `UPDATE agent_runs SET status='interrupted', ended_at=?,
			error = CASE WHEN error = '' THEN ? ELSE error END
			WHERE status='running' OR (status='awaiting_approval' AND approve_style='per_tool') RETURNING id`, now, msg)
		if err != nil {
			return fmt.Errorf("store: interrupt agent runs: %w", err)
		}
		for _, id := range agents {
			t.note(ResAgentRun, id, OpUpdate)
		}
		res.AgentRuns = len(agents)

		// 3. Permission requests nobody can answer any more: those of interrupted
		// agent runs, and those linked to the approvals just expired.
		perms, err := t.updateReturning(ctx, `UPDATE permission_requests SET decision='expired', decided_by='timeout', reason=?, decided_at=?
			WHERE decision IS NULL AND (
			  agent_run_id IN (SELECT id FROM agent_runs WHERE status='interrupted' AND ended_at=?)
			  OR id IN (SELECT permission_request_id FROM approvals WHERE status='expired' AND decided_by='system' AND decided_at=?))
			RETURNING id`, msg, now, now, now)
		if err != nil {
			return fmt.Errorf("store: expire permission requests: %w", err)
		}
		for _, id := range perms {
			t.note(ResPermissionRequest, id, OpUpdate)
		}
		res.ExpiredPermissions = len(perms)

		// 4. Node runs.
		nodes, err := t.updateReturning(ctx, `UPDATE node_runs SET status='interrupted', ended_at=?,
			error = CASE WHEN error = '' THEN ? ELSE error END
			WHERE status='running'
			   OR (status='awaiting_approval' AND EXISTS (
			        SELECT 1 FROM agent_runs a WHERE a.node_run_id = node_runs.id AND a.approve_style='per_tool'))
			RETURNING id`, now, msg)
		if err != nil {
			return fmt.Errorf("store: interrupt node runs: %w", err)
		}
		for _, id := range nodes {
			t.note(ResNodeRun, id, OpUpdate)
		}
		res.NodeRuns = len(nodes)

		// 5. Flow runs: running ones, and waiting ones with nothing left to wait for.
		flows, err := t.updateReturning(ctx, `UPDATE flow_runs SET status='interrupted', ended_at=?,
			error = CASE WHEN error = '' THEN ? ELSE error END
			WHERE status='running'
			   OR (status='awaiting_approval' AND NOT EXISTS (
			        SELECT 1 FROM node_runs n WHERE n.flow_run_id = flow_runs.id AND n.status='awaiting_approval'))
			RETURNING id`, now, msg)
		if err != nil {
			return fmt.Errorf("store: interrupt flow runs: %w", err)
		}
		for _, id := range flows {
			t.note(ResFlowRun, id, OpUpdate)
		}
		res.FlowRuns = len(flows)

		// 6. Sensor polls.
		polls, err := t.updateReturning(ctx, `UPDATE sensor_runs SET status='interrupted', ended_at=?,
			error = CASE WHEN error = '' THEN ? ELSE error END WHERE status='running' RETURNING id`, now, msg)
		if err != nil {
			return fmt.Errorf("store: interrupt sensor runs: %w", err)
		}
		for _, id := range polls {
			t.note(ResSensorRun, id, OpUpdate)
		}
		res.SensorRuns = len(polls)

		// 7. Assist sessions whose turn was running: the agent process is gone. The conversation
		// stays usable, the next message resumes it.
		assists, err := t.updateReturning(ctx, `UPDATE assist_sessions SET status='failed', updated_at=?,
			error = CASE WHEN error = '' THEN ? ELSE error END
			WHERE status IN ('running','awaiting_approval') RETURNING id`, now, "the daemon restarted during the turn")
		if err != nil {
			return fmt.Errorf("store: interrupt assist sessions: %w", err)
		}
		for _, id := range assists {
			t.note(ResAssistSession, id, OpUpdate)
		}
		res.AssistSessions = len(assists)
		return nil
	})
	return res, err
}

// Counts are the live totals shown in the UI header.
type Counts struct {
	FlowRunsQueued   int `json:"flow_runs_queued"`
	FlowRunsRunning  int `json:"flow_runs_running"`
	FlowRunsAwaiting int `json:"flow_runs_awaiting_approval"`
	NodeRunsQueued   int `json:"node_runs_queued"`
	NodeRunsRunning  int `json:"node_runs_running"`
	AgentRunsQueued  int `json:"agent_runs_queued"`
	AgentRunsRunning int `json:"agent_runs_running"`
	PendingApprovals int `json:"pending_approvals"`
	UnreadInbox      int `json:"unread_inbox"`
}

// Counts returns the header counters. Test runs are excluded from flow run counts;
// UnreadInbox excludes archived and dry-run items. AgentRunsQueued counts agent
// runs waiting for a slot: queued agent_runs rows plus agent node runs still
// queued behind max_agent_runs (their agent run row is created when they start).
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.rdb.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM flow_runs WHERE status='queued' AND kind <> 'test'),
		(SELECT COUNT(*) FROM flow_runs WHERE status='running' AND kind <> 'test'),
		(SELECT COUNT(*) FROM flow_runs WHERE status='awaiting_approval' AND kind <> 'test'),
		(SELECT COUNT(*) FROM node_runs WHERE status='queued'),
		(SELECT COUNT(*) FROM node_runs WHERE status='running'),
		(SELECT COUNT(*) FROM agent_runs WHERE status='queued')
		 + (SELECT COUNT(*) FROM node_runs n WHERE n.status='queued' AND n.node_type='agent'
		    AND NOT EXISTS (SELECT 1 FROM agent_runs a WHERE a.node_run_id=n.id AND a.status='queued')),
		(SELECT COUNT(*) FROM agent_runs WHERE status='running'),
		(SELECT COUNT(*) FROM approvals WHERE status='pending'),
		(SELECT COUNT(*) FROM inbox_items WHERE is_read=0 AND archived=0 AND dry_run=0)`).
		Scan(&c.FlowRunsQueued, &c.FlowRunsRunning, &c.FlowRunsAwaiting, &c.NodeRunsQueued, &c.NodeRunsRunning,
			&c.AgentRunsQueued, &c.AgentRunsRunning, &c.PendingApprovals, &c.UnreadInbox)
	if err != nil {
		return Counts{}, fmt.Errorf("store: counts: %w", err)
	}
	return c, nil
}

// blobColumns lists every column that can hold a blob ref. A test checks this
// against the schema so a new *_ref column cannot be forgotten (which would
// let GC delete live blobs).
var blobColumns = []struct{ table, col string }{
	{"sensor_runs", "stdout_ref"}, {"sensor_runs", "stderr_ref"},
	{"events", "payload_ref"},
	{"node_runs", "stdin_ref"}, {"node_runs", "stdout_ref"}, {"node_runs", "stderr_ref"},
	{"agent_runs", "prompt_ref"}, {"agent_runs", "result_ref"}, {"agent_runs", "raw_jsonrpc_ref"},
	{"agent_events", "payload_ref"},
	{"permission_requests", "raw_input_ref"},
	{"approvals", "proposal_ref"},
}

// unreferenced returns the subset of refs that no row references. It runs in
// the write transaction so the answer cannot be invalidated by a concurrent
// spill before the caller acts on it.
func (t *txn) unreferenced(ctx context.Context, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if _, err := t.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS gc_refs (ref TEXT PRIMARY KEY) WITHOUT ROWID`); err != nil {
		return nil, fmt.Errorf("store: create gc_refs: %w", err)
	}
	if _, err := t.ExecContext(ctx, `DELETE FROM gc_refs`); err != nil {
		return nil, err
	}
	for _, r := range refs {
		if _, err := t.ExecContext(ctx, `INSERT OR IGNORE INTO gc_refs (ref) VALUES (?)`, r); err != nil {
			return nil, fmt.Errorf("store: stage gc ref: %w", err)
		}
	}
	for _, bc := range blobColumns {
		// table and column names come from the constant blobColumns list.
		q := "DELETE FROM gc_refs WHERE ref IN (SELECT " + bc.col + " FROM " + bc.table + " WHERE " + bc.col + " <> '')"
		if _, err := t.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("store: check refs in %s.%s: %w", bc.table, bc.col, err)
		}
	}
	out, err := queryAll(ctx, t, scanString, `SELECT ref FROM gc_refs ORDER BY ref`)
	if err != nil {
		return nil, err
	}
	if _, err := t.ExecContext(ctx, `DELETE FROM gc_refs`); err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveOrphanBlobs deletes the given blobs from disk, skipping any that a row
// still references, and returns the refs actually removed. Use it on
// PruneResult.OrphanBlobs.
func (s *Store) RemoveOrphanBlobs(ctx context.Context, refs []string) ([]string, error) {
	var removed []string
	err := s.write(ctx, func(t *txn) error {
		orphans, err := t.unreferenced(ctx, refs)
		if err != nil {
			return err
		}
		for _, r := range orphans {
			if err := s.blobs.Delete(r); err != nil {
				return err
			}
			removed = append(removed, r)
		}
		return nil
	})
	return removed, err
}

// GCBlobs deletes blobs on disk that no row references and that were last
// written more than minAge ago (by wall clock, like the file times). The grace period protects a blob stored with
// PutBlob moments before the row that will reference it is created. It returns
// the removed refs.
func (s *Store) GCBlobs(ctx context.Context, minAge time.Duration) ([]string, error) {
	cutoff := time.Now().Add(-minAge)
	var candidates []string
	if err := s.blobs.Walk(func(i blob.Info) error {
		if i.Modified.Before(cutoff) {
			candidates = append(candidates, i.Ref)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: walk blobs: %w", err)
	}
	return s.RemoveOrphanBlobs(ctx, candidates)
}

// PruneResult reports what Prune deleted and what the caller must now clean up.
type PruneResult struct {
	FlowRuns       int `json:"flow_runs"` // node runs, agent runs, events, permission requests and approvals go with them
	Events         int `json:"events"`
	SensorRuns     int `json:"sensor_runs"`
	InboxItems     int `json:"inbox_items"`
	AuthEvents     int `json:"auth_events"`
	Sessions       int `json:"sessions"`
	SeenKeys       int `json:"seen_keys"`
	FlowVersions   int `json:"flow_versions"`
	AssistSessions int `json:"assist_sessions"` // agent runs, transcripts and approvals of their turns go with them
	// OrphanBlobs are refs that were referenced only by deleted rows. Pass them
	// to RemoveOrphanBlobs.
	OrphanBlobs []string `json:"orphan_blobs"`
	// Worktrees are worktree paths of deleted agent runs; remove them from disk.
	Worktrees []string `json:"worktrees"`
	// AssistSessionIDs are the deleted assist sessions; remove their draft directories.
	AssistSessionIDs []string `json:"assist_session_ids"`
}

// Prune applies retention (REQUIREMENTS §9). now is the reference time.
//
// Rows older than retentionDays are deleted, except anything linked to a
// starred inbox item or a pending approval:
//
//   - A flow run is deleted (with its node runs, agent runs, agent events,
//     permission requests and approvals) when it is terminal, created before
//     the cutoff, has no starred inbox item, and none of its approvals is pending.
//   - An event is deleted when it predates the cutoff and no remaining flow
//     run uses it; a sensor run likewise when no remaining event references it.
//   - Inbox items that are not starred and predate the cutoff are deleted;
//     starred items are kept forever.
//   - Auth events, and sessions that expired or were revoked before the
//     cutoff, are deleted.
//   - Dedup keys are deleted only after dedupRetentionDays, so items whose
//     events were pruned do not re-fire.
//   - Flow versions are deleted when unreferenced, older than the cutoff, and
//     not their flow's newest version.
//
// Prune does not touch the filesystem; see PruneResult.
func (s *Store) Prune(ctx context.Context, now time.Time, retentionDays, dedupRetentionDays int) (PruneResult, error) {
	if retentionDays <= 0 || dedupRetentionDays <= 0 {
		return PruneResult{}, fmt.Errorf("%w: retention days must be positive", ErrInvalid)
	}
	cutoff := ts(now.AddDate(0, 0, -retentionDays))
	dedupCutoff := ts(now.AddDate(0, 0, -dedupRetentionDays))
	var res PruneResult
	err := s.write(ctx, func(t *txn) error {
		if err := t.stagePrune(ctx, cutoff); err != nil {
			return err
		}
		refs, worktrees, err := t.pruneCandidates(ctx)
		if err != nil {
			return err
		}
		res.Worktrees = worktrees
		if res.AssistSessionIDs, err = queryAll(ctx, t, scanString, `SELECT id FROM prune_assist ORDER BY id`); err != nil {
			return fmt.Errorf("store: prune assist sessions: %w", err)
		}

		del := func(dst *int, resource, query string, args ...any) error {
			r, err := t.ExecContext(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("store: prune %s: %w", resource, err)
			}
			n, _ := r.RowsAffected()
			*dst = int(n)
			if n > 0 {
				t.note(resource, "*", OpDelete)
			}
			return nil
		}
		steps := []struct {
			dst      *int
			resource string
			query    string
			args     []any
		}{
			{&res.FlowRuns, ResFlowRun, `DELETE FROM flow_runs WHERE id IN (SELECT id FROM prune_runs)`, nil},
			{&res.Events, ResEvent, `DELETE FROM events WHERE id IN (SELECT id FROM prune_events)`, nil},
			{&res.SensorRuns, ResSensorRun, `DELETE FROM sensor_runs WHERE id IN (SELECT id FROM prune_sensor_runs)`, nil},
			{&res.InboxItems, ResInboxItem, `DELETE FROM inbox_items WHERE starred = 0 AND created_at < ?`, []any{cutoff}},
			{&res.AuthEvents, ResAuthEvent, `DELETE FROM auth_events WHERE ts < ?`, []any{cutoff}},
			{&res.Sessions, ResSession, `DELETE FROM sessions WHERE expires_at < ? OR revoked_at < ?`, []any{cutoff, cutoff}},
			{&res.SeenKeys, "seen_key", `DELETE FROM seen_keys WHERE first_seen < ?`, []any{dedupCutoff}},
			{&res.AssistSessions, ResAssistSession, `DELETE FROM assist_sessions WHERE id IN (SELECT id FROM prune_assist)`, nil},
			{&res.FlowVersions, ResFlowVersion, `DELETE FROM flow_versions WHERE first_seen < ?
				AND NOT EXISTS (SELECT 1 FROM flow_runs fr WHERE fr.flow_id = flow_versions.flow_id AND fr.flow_hash = flow_versions.hash)
				AND hash <> (SELECT v.hash FROM flow_versions v WHERE v.flow_id = flow_versions.flow_id ORDER BY v.first_seen DESC, v.hash DESC LIMIT 1)`, []any{cutoff}},
		}
		for _, st := range steps {
			if err := del(st.dst, st.resource, st.query, st.args...); err != nil {
				return err
			}
		}
		// Operator approvals (request approvals with no node run) belong to no run, so the cascade never reaches them.
		var operatorApprovals int
		if err := del(&operatorApprovals, ResApproval, `DELETE FROM approvals WHERE type = 'request' AND node_run_id IS NULL AND status <> 'pending' AND created_at < ?`, cutoff); err != nil {
			return err
		}
		// The notification log is pruned silently: its rows are not shown in lists clients keep in sync.
		if _, err := t.ExecContext(ctx, `DELETE FROM notifications WHERE created_at < ?`, cutoff); err != nil {
			return fmt.Errorf("store: prune notifications: %w", err)
		}
		if res.OrphanBlobs, err = t.unreferenced(ctx, refs); err != nil {
			return err
		}
		for _, tbl := range []string{"prune_runs", "prune_events", "prune_sensor_runs", "prune_assist"} {
			if _, err := t.ExecContext(ctx, "DROP TABLE "+tbl); err != nil {
				return fmt.Errorf("store: drop %s: %w", tbl, err)
			}
		}
		return nil
	})
	if err != nil {
		return PruneResult{}, err
	}
	return res, nil
}

// stagePrune fills temp tables with the ids Prune will delete.
func (t *txn) stagePrune(ctx context.Context, cutoff string) error {
	stmts := []struct {
		q    string
		args []any
	}{
		{`DROP TABLE IF EXISTS prune_runs`, nil},
		{`DROP TABLE IF EXISTS prune_events`, nil},
		{`DROP TABLE IF EXISTS prune_sensor_runs`, nil},
		{`DROP TABLE IF EXISTS prune_assist`, nil},
		{`CREATE TEMP TABLE prune_runs (id TEXT PRIMARY KEY) WITHOUT ROWID`, nil},
		{`CREATE TEMP TABLE prune_events (id TEXT PRIMARY KEY) WITHOUT ROWID`, nil},
		{`CREATE TEMP TABLE prune_sensor_runs (id TEXT PRIMARY KEY) WITHOUT ROWID`, nil},
		{`CREATE TEMP TABLE prune_assist (id TEXT PRIMARY KEY) WITHOUT ROWID`, nil},
		{`INSERT INTO prune_runs SELECT fr.id FROM flow_runs fr
			WHERE fr.created_at < ? AND fr.status IN ('succeeded','failed','killed','interrupted','cancelled')
			  AND NOT EXISTS (SELECT 1 FROM inbox_items i WHERE i.starred = 1 AND i.flow_run_id = fr.id)
			  AND NOT EXISTS (SELECT 1 FROM approvals a
			        JOIN agent_runs ar ON ar.id = a.agent_run_id
			        JOIN node_runs nr ON nr.id = ar.node_run_id
			        WHERE nr.flow_run_id = fr.id AND a.status = 'pending')`, []any{cutoff}},
		{`INSERT INTO prune_events SELECT e.id FROM events e
			WHERE e.created_at < ?
			  AND NOT EXISTS (SELECT 1 FROM flow_runs fr WHERE fr.event_id = e.id AND fr.id NOT IN (SELECT id FROM prune_runs))`, []any{cutoff}},
		{`INSERT INTO prune_sensor_runs SELECT sr.id FROM sensor_runs sr
			WHERE sr.started_at < ? AND sr.status <> 'running'
			  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.sensor_run_id = sr.id AND e.id NOT IN (SELECT id FROM prune_events))`, []any{cutoff}},
		{`INSERT INTO prune_assist SELECT s.id FROM assist_sessions s
			WHERE s.updated_at < ? AND s.status IN ('idle','failed','closed')
			  AND NOT EXISTS (SELECT 1 FROM approvals a JOIN agent_runs ar ON ar.id = a.agent_run_id
			        WHERE ar.assist_session_id = s.id AND a.status = 'pending')`, []any{cutoff}},
	}
	for _, s := range stmts {
		if _, err := t.ExecContext(ctx, s.q, s.args...); err != nil {
			return fmt.Errorf("store: stage prune: %w", err)
		}
	}
	return nil
}

const (
	scopeNodes = `flow_run_id IN (SELECT id FROM prune_runs)`
	scopeRuns  = `node_run_id IN (SELECT id FROM node_runs WHERE ` + scopeNodes + `)`
	scopeAgent = `agent_run_id IN (SELECT id FROM agent_runs WHERE ` + scopeRuns + `)`

	// Assist turns are agent runs of a session being pruned.
	scopeAssistRuns  = `assist_session_id IN (SELECT id FROM prune_assist)`
	scopeAssistAgent = `agent_run_id IN (SELECT id FROM agent_runs WHERE ` + scopeAssistRuns + `)`
)

// pruneCandidates gathers blob refs and worktree paths held by the rows staged
// for deletion, before they are deleted.
func (t *txn) pruneCandidates(ctx context.Context) (refs, worktrees []string, err error) {
	scopes := map[string]string{
		"node_runs":           scopeNodes,
		"agent_runs":          `(` + scopeRuns + ` OR ` + scopeAssistRuns + `)`,
		"agent_events":        `(` + scopeAgent + ` OR ` + scopeAssistAgent + `)`,
		"permission_requests": `(` + scopeAgent + ` OR ` + scopeAssistAgent + `)`,
		"approvals":           `(` + scopeAgent + ` OR ` + scopeAssistAgent + `)`,
		"events":              `id IN (SELECT id FROM prune_events)`,
		"sensor_runs":         `id IN (SELECT id FROM prune_sensor_runs)`,
	}
	seen := map[string]bool{}
	for _, bc := range blobColumns {
		// table, column and scope are compile-time constants.
		q := "SELECT " + bc.col + " FROM " + bc.table + " WHERE " + bc.col + " <> '' AND " + scopes[bc.table]
		found, err := queryAll(ctx, t, scanString, q)
		if err != nil {
			return nil, nil, fmt.Errorf("store: prune candidates %s.%s: %w", bc.table, bc.col, err)
		}
		for _, r := range found {
			if !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
		}
	}
	sort.Strings(refs)

	worktrees, err = queryAll(ctx, t, scanString, sqlJoin(`SELECT DISTINCT worktree_path FROM agent_runs WHERE worktree_path <> '' AND `, scopeRuns, ` ORDER BY worktree_path`))
	if err != nil {
		return nil, nil, fmt.Errorf("store: prune worktrees: %w", err)
	}
	return refs, worktrees, nil
}
