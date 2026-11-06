package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ---- flow versions ----

// PutFlowVersion snapshots a flow's YAML under (flowID, hash). It reports
// whether the version was new; existing versions are never modified.
func (s *Store) PutFlowVersion(ctx context.Context, flowID, hash, yaml string) (bool, error) {
	if flowID == "" || hash == "" {
		return false, fmt.Errorf("%w: flow_id and hash required", ErrInvalid)
	}
	var created bool
	err := s.write(ctx, func(t *txn) error {
		var err error
		created, err = t.putFlowVersion(ctx, flowID, hash, yaml)
		return err
	})
	return created, err
}

func (t *txn) putFlowVersion(ctx context.Context, flowID, hash, yaml string) (bool, error) {
	res, err := t.ExecContext(ctx, `INSERT INTO flow_versions (flow_id, hash, yaml, first_seen) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`,
		flowID, hash, yaml, ts(t.s.now()))
	if err != nil {
		return false, fmt.Errorf("store: insert flow_version: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 1 {
		t.note(ResFlowVersion, flowID+"/"+hash, OpCreate)
	}
	return n == 1, nil
}

// GetFlowVersion returns the snapshot for (flowID, hash).
func (s *Store) GetFlowVersion(ctx context.Context, flowID, hash string) (*FlowVersion, error) {
	var v FlowVersion
	var first string
	err := s.rdb.QueryRowContext(ctx, `SELECT flow_id, hash, yaml, first_seen FROM flow_versions WHERE flow_id = ? AND hash = ?`, flowID, hash).
		Scan(&v.FlowID, &v.Hash, &v.YAML, &first)
	if err != nil {
		return nil, scanErr(err)
	}
	if v.FirstSeen, err = parseTS(first); err != nil {
		return nil, err
	}
	return &v, nil
}

// ListFlowVersions returns every stored version of a flow, newest first.
func (s *Store) ListFlowVersions(ctx context.Context, flowID string) ([]FlowVersion, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT flow_id, hash, yaml, first_seen FROM flow_versions WHERE flow_id = ? ORDER BY first_seen DESC, hash`, flowID)
	if err != nil {
		return nil, fmt.Errorf("store: list flow_versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []FlowVersion{}
	for rows.Next() {
		var v FlowVersion
		var first string
		if err := rows.Scan(&v.FlowID, &v.Hash, &v.YAML, &first); err != nil {
			return nil, err
		}
		if v.FirstSeen, err = parseTS(first); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- flow runs ----

const flowRunCols = `id, flow_id, flow_hash, COALESCE(event_id, ''), kind, COALESCE(replay_of, ''), dry_run, status, error, created_at, started_at, ended_at, cancelled_by`

func scanFlowRun(r rowScanner) (FlowRun, error) {
	var fr FlowRun
	var kind, status, created string
	var dry int
	var started, ended sql.NullString
	if err := r.Scan(&fr.ID, &fr.FlowID, &fr.FlowHash, &fr.EventID, &kind, &fr.ReplayOf, &dry, &status, &fr.Error, &created, &started, &ended, &fr.CancelledBy); err != nil {
		return FlowRun{}, scanErr(err)
	}
	fr.Kind, fr.Status, fr.DryRun = FlowRunKind(kind), FlowRunStatus(status), dry != 0
	var err error
	if fr.CreatedAt, err = parseTS(created); err != nil {
		return FlowRun{}, err
	}
	if fr.StartedAt, err = parseTSPtr(started); err != nil {
		return FlowRun{}, err
	}
	if fr.EndedAt, err = parseTSPtr(ended); err != nil {
		return FlowRun{}, err
	}
	return fr, nil
}

// CreateFlowRun inserts fr, assigning ID, Kind (live), Status (queued) and
// CreatedAt (now) when unset. The (FlowID, FlowHash) version must exist unless
// fr.FlowYAML is set, in which case it is stored in the same transaction.
func (s *Store) CreateFlowRun(ctx context.Context, fr *FlowRun) error {
	if fr.FlowID == "" || fr.FlowHash == "" {
		return fmt.Errorf("%w: flow_id and flow_hash required", ErrInvalid)
	}
	if fr.Kind == "" {
		fr.Kind = KindLive
	}
	if fr.Status == "" {
		fr.Status = FlowRunQueued
	}
	if !fr.Kind.valid() || !fr.Status.valid() {
		return fmt.Errorf("%w: flow run kind %q / status %q", ErrInvalid, fr.Kind, fr.Status)
	}
	if fr.ID == "" {
		fr.ID = s.NewID()
	}
	if fr.CreatedAt.IsZero() {
		fr.CreatedAt = s.Now()
	}
	return s.write(ctx, func(t *txn) error {
		if fr.FlowYAML != "" {
			if _, err := t.putFlowVersion(ctx, fr.FlowID, fr.FlowHash, fr.FlowYAML); err != nil {
				return err
			}
		}
		_, err := t.ExecContext(ctx, `INSERT INTO flow_runs (id, flow_id, flow_hash, event_id, kind, replay_of, dry_run, status, error, created_at, started_at, ended_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			fr.ID, fr.FlowID, fr.FlowHash, nullStr(fr.EventID), fr.Kind, nullStr(fr.ReplayOf), b2i(fr.DryRun), fr.Status, fr.Error,
			ts(fr.CreatedAt), tsOrNil(fr.StartedAt), tsOrNil(fr.EndedAt))
		if err != nil {
			return fmt.Errorf("store: insert flow_run: %w", err)
		}
		t.note(ResFlowRun, fr.ID, OpCreate)
		return nil
	})
}

// UpdateFlowRun writes the mutable columns: status, error, started_at, ended_at.
func (s *Store) UpdateFlowRun(ctx context.Context, fr *FlowRun) error {
	if !fr.Status.valid() {
		return fmt.Errorf("%w: flow run status %q", ErrInvalid, fr.Status)
	}
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE flow_runs SET status=?, error=?, started_at=?, ended_at=? WHERE id=?`,
			fr.Status, fr.Error, tsOrNil(fr.StartedAt), tsOrNil(fr.EndedAt), fr.ID)
		if err != nil {
			return fmt.Errorf("store: update flow_run: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResFlowRun, fr.ID, OpUpdate)
		return nil
	})
}

// GetFlowRun returns one flow run.
func (s *Store) GetFlowRun(ctx context.Context, id string) (*FlowRun, error) {
	return getFlowRun(ctx, s.rdb, id)
}

func getFlowRun(ctx context.Context, q dbq, id string) (*FlowRun, error) {
	fr, err := scanFlowRun(q.QueryRowContext(ctx, "SELECT "+flowRunCols+" FROM flow_runs WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &fr, nil
}

// FlowRunFilter selects flow runs for ListFlowRuns.
type FlowRunFilter struct {
	FlowID   string
	SensorID string // via the run's event
	EventID  string
	ReplayOf string
	Statuses []FlowRunStatus
	// Kinds restricts run kinds. When empty, test runs are excluded so they
	// do not clutter timelines (REQUIREMENTS §8); list them with Kinds: {KindTest}.
	Kinds    []FlowRunKind
	Decision NodeDecision // runs with at least one node run carrying this decision
	Search   string       // case-insensitive substring of the event's inline payload or dedup key
	Time     TimeRange    // on created_at
	ListOpts
}

// ListFlowRuns lists flow runs, newest first.
func (s *Store) ListFlowRuns(ctx context.Context, f FlowRunFilter) (Page[FlowRun], error) {
	var w where
	w.eq("flow_id", f.FlowID)
	w.eq("event_id", f.EventID)
	w.eq("replay_of", f.ReplayOf)
	w.in("status", strs(f.Statuses))
	if len(f.Kinds) > 0 {
		w.in("kind", strs(f.Kinds))
	} else {
		w.add("kind <> 'test'")
	}
	w.timeRange("created_at", f.Time)
	if f.SensorID != "" {
		w.add("EXISTS (SELECT 1 FROM events e WHERE e.id = flow_runs.event_id AND e.sensor_id = ?)", f.SensorID)
	}
	if f.Decision != "" {
		w.add("EXISTS (SELECT 1 FROM node_runs n WHERE n.flow_run_id = flow_runs.id AND n.decision = ?)", f.Decision)
	}
	if f.Search != "" {
		w.add(`EXISTS (SELECT 1 FROM events e WHERE e.id = flow_runs.event_id AND
			(instr(lower(e.payload_text), lower(?)) > 0 OR instr(lower(e.dedup_key), lower(?)) > 0))`, f.Search, f.Search)
	}
	return listPage(ctx, s.rdb, "SELECT "+flowRunCols+" FROM flow_runs", &w, f.ListOpts, "id",
		scanFlowRun, func(r FlowRun) string { return r.ID })
}

// ClaimNextFlowRun atomically moves the oldest queued flow run whose flow is
// under its concurrency cap to running (StartedAt = now) and returns it, or
// returns nil when nothing is claimable. maxPerFlow gives each flow's
// max_concurrent: a negative value means unlimited, 0 means the flow's runs
// are never claimed. Only running live/replay runs count toward the cap
// (awaiting_approval runs do not hold a slot).
func (s *Store) ClaimNextFlowRun(ctx context.Context, maxPerFlow func(flowID string) int) (*FlowRun, error) {
	var claimed *FlowRun
	err := s.write(ctx, func(t *txn) error {
		running, err := t.runningPerFlow(ctx)
		if err != nil {
			return err
		}
		id, err := t.firstClaimable(ctx, running, maxPerFlow)
		if err != nil || id == "" {
			return err
		}
		now := s.Now()
		res, err := t.ExecContext(ctx, `UPDATE flow_runs SET status='running', started_at=? WHERE id=? AND status='queued'`, ts(now), id)
		if err != nil {
			return fmt.Errorf("store: claim flow_run: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		fr, err := getFlowRun(ctx, t, id)
		if err != nil {
			return err
		}
		claimed = fr
		t.note(ResFlowRun, id, OpUpdate)
		return nil
	})
	return claimed, err
}

func (t *txn) runningPerFlow(ctx context.Context) (map[string]int, error) {
	rows, err := t.QueryContext(ctx, `SELECT flow_id, COUNT(*) FROM flow_runs WHERE status = 'running' AND kind <> 'test' GROUP BY flow_id`)
	if err != nil {
		return nil, fmt.Errorf("store: count running flow runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (t *txn) firstClaimable(ctx context.Context, running map[string]int, maxPerFlow func(string) int) (string, error) {
	rows, err := t.QueryContext(ctx, `SELECT id, flow_id FROM flow_runs WHERE status = 'queued' ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("store: scan queued flow runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, flowID string
		if err := rows.Scan(&id, &flowID); err != nil {
			return "", err
		}
		limit := -1
		if maxPerFlow != nil {
			limit = maxPerFlow(flowID)
		}
		if limit < 0 || running[flowID] < limit {
			return id, nil
		}
	}
	return "", rows.Err()
}

// ---- node runs ----

func nodeRunCols(slim bool) string {
	return "id, flow_run_id, node_id, node_type, COALESCE(parent_node_run_id, ''), status, COALESCE(decision, ''), reason, ports, output_count, lang, script, argv, env_keys, " +
		contentCols("stdin", slim) + ", " + contentCols("stdout", slim) + ", " + contentCols("stderr", slim) +
		", exit_code, error, queued_at, started_at, ended_at, duration_ms"
}

func scanNodeRun(r rowScanner) (NodeRun, error) {
	var (
		n                     NodeRun
		typ, status, decision string
		ports, argv, envKeys  string
		exit, dur             sql.NullInt64
		queued                string
		started, ended        sql.NullString
	)
	dests := []any{&n.ID, &n.FlowRunID, &n.NodeID, &typ, &n.ParentNodeRunID, &status, &decision, &n.Reason, &ports, &n.OutputCount,
		&n.Lang, &n.Script, &argv, &envKeys}
	dests = append(dests, scanDests(&n.Stdin)...)
	dests = append(dests, scanDests(&n.Stdout)...)
	dests = append(dests, scanDests(&n.Stderr)...)
	dests = append(dests, &exit, &n.Error, &queued, &started, &ended, &dur)
	if err := r.Scan(dests...); err != nil {
		return NodeRun{}, scanErr(err)
	}
	n.NodeType, n.Status, n.Decision = NodeType(typ), NodeRunStatus(status), NodeDecision(decision)
	var err error
	if n.Ports, err = parseStrings(ports); err != nil {
		return NodeRun{}, err
	}
	if n.Argv, err = parseStrings(argv); err != nil {
		return NodeRun{}, err
	}
	if n.EnvKeys, err = parseStrings(envKeys); err != nil {
		return NodeRun{}, err
	}
	if exit.Valid {
		v := int(exit.Int64)
		n.ExitCode = &v
	}
	if dur.Valid {
		n.DurationMS = &dur.Int64
	}
	if n.QueuedAt, err = parseTS(queued); err != nil {
		return NodeRun{}, err
	}
	if n.StartedAt, err = parseTSPtr(started); err != nil {
		return NodeRun{}, err
	}
	if n.EndedAt, err = parseTSPtr(ended); err != nil {
		return NodeRun{}, err
	}
	return n, nil
}

// CreateNodeRun inserts n, assigning ID, Status (queued) and QueuedAt (now)
// when unset. Large stdin/stdout/stderr spill to blobs.
func (s *Store) CreateNodeRun(ctx context.Context, n *NodeRun) error {
	if err := s.defaultNodeRun(n); err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error { return t.insertNodeRun(ctx, n) })
}

// CreateNodeRuns inserts every node run in one transaction, each as
// CreateNodeRun does: all of them exist afterwards or none does. Node runs
// that become runnable together (the entry nodes of a flow run) are queued
// this way, so a reader never sees the first without the others.
func (s *Store) CreateNodeRuns(ctx context.Context, ns []*NodeRun) error {
	for _, n := range ns {
		if err := s.defaultNodeRun(n); err != nil {
			return err
		}
	}
	return s.write(ctx, func(t *txn) error {
		for _, n := range ns {
			if err := t.insertNodeRun(ctx, n); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateNodeRun writes every column except id, flow_run_id, node_id,
// node_type, parent_node_run_id and queued_at. A NodeRun read from the store
// (even a slim one) can be modified and written back without losing content.
func (s *Store) UpdateNodeRun(ctx context.Context, n *NodeRun) error {
	if err := checkNodeRunState(n); err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error { return t.updateNodeRun(ctx, n) })
}

// FinishNodeRun is UpdateNodeRun for a node run that has finished, together
// with CreateNodeRuns(next) for the node runs that follow it, in one
// transaction. A reader therefore never sees a finished node run without the
// work it hands on: a flow run whose node runs are all finished has nothing
// still to queue. If any of next is rejected, n is left as it was.
func (s *Store) FinishNodeRun(ctx context.Context, n *NodeRun, next []*NodeRun) error {
	if err := checkNodeRunState(n); err != nil {
		return err
	}
	for _, c := range next {
		if err := s.defaultNodeRun(c); err != nil {
			return err
		}
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.updateNodeRun(ctx, n); err != nil {
			return err
		}
		for _, c := range next {
			if err := t.insertNodeRun(ctx, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// defaultNodeRun validates n for insertion and assigns the ID, Status (queued)
// and QueuedAt (now) that are unset.
func (s *Store) defaultNodeRun(n *NodeRun) error {
	if n.FlowRunID == "" || n.NodeID == "" {
		return fmt.Errorf("%w: flow_run_id and node_id required", ErrInvalid)
	}
	if n.Status == "" {
		n.Status = NodeQueued
	}
	if !n.NodeType.valid() || !n.Status.valid() || !n.Decision.valid() {
		return fmt.Errorf("%w: node run type %q / status %q / decision %q", ErrInvalid, n.NodeType, n.Status, n.Decision)
	}
	if n.ID == "" {
		n.ID = s.NewID()
	}
	if n.QueuedAt.IsZero() {
		n.QueuedAt = s.Now()
	}
	return nil
}

func checkNodeRunState(n *NodeRun) error {
	if !n.Status.valid() || !n.Decision.valid() {
		return fmt.Errorf("%w: node run status %q / decision %q", ErrInvalid, n.Status, n.Decision)
	}
	return nil
}

func (t *txn) insertNodeRun(ctx context.Context, n *NodeRun) error {
	if err := t.prep(ctx, "node_runs", "", contentField{"stdin", &n.Stdin}, contentField{"stdout", &n.Stdout}, contentField{"stderr", &n.Stderr}); err != nil {
		return err
	}
	_, err := t.ExecContext(ctx, `INSERT INTO node_runs (id, flow_run_id, node_id, node_type, parent_node_run_id, status, decision, reason, ports, output_count,
		lang, script, argv, env_keys, stdin_text, stdin_ref, stdin_size, stdout_text, stdout_ref, stdout_size, stderr_text, stderr_ref, stderr_size,
		exit_code, error, queued_at, started_at, ended_at, duration_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.FlowRunID, n.NodeID, n.NodeType, nullStr(n.ParentNodeRunID), n.Status, nullStr(string(n.Decision)), n.Reason,
		stringsText(n.Ports), n.OutputCount, n.Lang, n.Script, stringsText(n.Argv), stringsText(n.EnvKeys),
		n.Stdin.Text, n.Stdin.Ref, n.Stdin.Size, n.Stdout.Text, n.Stdout.Ref, n.Stdout.Size, n.Stderr.Text, n.Stderr.Ref, n.Stderr.Size,
		nullInt(n.ExitCode), n.Error, ts(n.QueuedAt), tsOrNil(n.StartedAt), tsOrNil(n.EndedAt), nullInt64(n.DurationMS))
	if err != nil {
		return fmt.Errorf("store: insert node_run: %w", err)
	}
	t.note(ResNodeRun, n.ID, OpCreate)
	return nil
}

func (t *txn) updateNodeRun(ctx context.Context, n *NodeRun) error {
	if err := t.prep(ctx, "node_runs", n.ID, contentField{"stdin", &n.Stdin}, contentField{"stdout", &n.Stdout}, contentField{"stderr", &n.Stderr}); err != nil {
		return err
	}
	res, err := t.ExecContext(ctx, `UPDATE node_runs SET status=?, decision=?, reason=?, ports=?, output_count=?, lang=?, script=?, argv=?, env_keys=?,
		stdin_text=?, stdin_ref=?, stdin_size=?, stdout_text=?, stdout_ref=?, stdout_size=?, stderr_text=?, stderr_ref=?, stderr_size=?,
		exit_code=?, error=?, started_at=?, ended_at=?, duration_ms=? WHERE id=?`,
		n.Status, nullStr(string(n.Decision)), n.Reason, stringsText(n.Ports), n.OutputCount, n.Lang, n.Script, stringsText(n.Argv), stringsText(n.EnvKeys),
		n.Stdin.Text, n.Stdin.Ref, n.Stdin.Size, n.Stdout.Text, n.Stdout.Ref, n.Stdout.Size, n.Stderr.Text, n.Stderr.Ref, n.Stderr.Size,
		nullInt(n.ExitCode), n.Error, tsOrNil(n.StartedAt), tsOrNil(n.EndedAt), nullInt64(n.DurationMS), n.ID)
	if err != nil {
		return fmt.Errorf("store: update node_run: %w", err)
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	t.note(ResNodeRun, n.ID, OpUpdate)
	return nil
}

// GetNodeRun returns one node run with full inline content.
func (s *Store) GetNodeRun(ctx context.Context, id string) (*NodeRun, error) {
	n, err := scanNodeRun(s.rdb.QueryRowContext(ctx, "SELECT "+nodeRunCols(false)+" FROM node_runs WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// NodeRunFilter selects node runs for ListNodeRuns.
type NodeRunFilter struct {
	FlowRunID string
	FlowID    string
	NodeID    string
	NodeType  NodeType
	Statuses  []NodeRunStatus
	Decision  NodeDecision
	Time      TimeRange // on queued_at
	Slim      bool      // omit inline stdin/stdout/stderr text
	ListOpts
}

// ListNodeRuns lists node runs, newest first (set Oldest for execution order).
func (s *Store) ListNodeRuns(ctx context.Context, f NodeRunFilter) (Page[NodeRun], error) {
	var w where
	w.eq("flow_run_id", f.FlowRunID)
	w.eq("node_id", f.NodeID)
	w.eq("node_type", string(f.NodeType))
	w.eq("decision", string(f.Decision))
	w.in("status", strs(f.Statuses))
	w.timeRange("queued_at", f.Time)
	if f.FlowID != "" {
		w.add("flow_run_id IN (SELECT id FROM flow_runs WHERE flow_id = ?)", f.FlowID)
	}
	return listPage(ctx, s.rdb, "SELECT "+nodeRunCols(f.Slim)+" FROM node_runs", &w, f.ListOpts, "id",
		scanNodeRun, func(n NodeRun) string { return n.ID })
}

// Unlimited marks a pool without a cap in NodeLimits.
const Unlimited = -1

// NodeLimits caps concurrently active node runs per node type. A type absent
// from the map, or mapped to 0, is never claimed; Unlimited (-1) removes the
// cap. Typical use: {NodeCommand: 8, NodeAgent: maxAgentRuns, NodeInbox: Unlimited}.
type NodeLimits map[NodeType]int

// ClaimNextNodeRun atomically moves the oldest queued node run whose type pool
// has a free slot to running (StartedAt = now) and returns it, or nil when
// none is claimable.
//
// Slots in use: node runs in status running, plus, for agents, node runs
// awaiting a per-tool approval, because that agent process stays alive
// (REQUIREMENTS §7.2). Agent nodes waiting on a proposal approval hold no slot;
// after approval, requeue the node run (status queued) and claim it again.
func (s *Store) ClaimNextNodeRun(ctx context.Context, limits NodeLimits) (*NodeRun, error) {
	var claimed *NodeRun
	err := s.write(ctx, func(t *txn) error {
		inUse, err := t.nodeSlotsInUse(ctx)
		if err != nil {
			return err
		}
		var types []string
		for typ, limit := range limits {
			if limit < 0 || inUse[typ] < limit {
				types = append(types, string(typ))
			}
		}
		if len(types) == 0 {
			return nil
		}
		var id string
		q := "SELECT id FROM node_runs WHERE status = 'queued' AND node_type IN (" + placeholders(len(types)) + ") ORDER BY id LIMIT 1"
		err = t.QueryRowContext(ctx, q, anySlice(types)...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("store: find claimable node_run: %w", err)
		}
		if _, err := t.ExecContext(ctx, `UPDATE node_runs SET status='running', started_at=? WHERE id=? AND status='queued'`, ts(s.now()), id); err != nil {
			return fmt.Errorf("store: claim node_run: %w", err)
		}
		n, err := scanNodeRun(t.QueryRowContext(ctx, "SELECT "+nodeRunCols(false)+" FROM node_runs WHERE id = ?", id))
		if err != nil {
			return err
		}
		claimed = &n
		t.note(ResNodeRun, id, OpUpdate)
		return nil
	})
	return claimed, err
}

func (t *txn) nodeSlotsInUse(ctx context.Context) (map[NodeType]int, error) {
	rows, err := t.QueryContext(ctx, `SELECT node_type, COUNT(*) FROM node_runs n
		WHERE n.status = 'running'
		   OR (n.status = 'awaiting_approval' AND n.node_type = 'agent' AND EXISTS (
		        SELECT 1 FROM agent_runs a WHERE a.node_run_id = n.id AND a.approve_style = 'per_tool' AND a.status = 'awaiting_approval'))
		GROUP BY node_type`)
	if err != nil {
		return nil, fmt.Errorf("store: count active node runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[NodeType]int{}
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			return nil, err
		}
		out[NodeType(typ)] = n
	}
	return out, rows.Err()
}

// ---- trees ----

// NodeRunTree is a node run with the node runs it produced (children are node
// runs whose parent_node_run_id is this run). Content is slim.
type NodeRunTree struct {
	NodeRun
	Children []*NodeRunTree `json:"children"`
}

// FlowRunTree is a flow run with its node runs arranged by parent. Nodes holds
// the roots (entry nodes) in execution order.
type FlowRunTree struct {
	FlowRun
	Nodes []*NodeRunTree `json:"nodes"`
}

// EventFanOut is an event with every non-test flow run it started.
type EventFanOut struct {
	Event    Event         `json:"event"`
	FlowRuns []FlowRunTree `json:"flow_runs"`
}

// GetFlowRunTree returns the flow run and its node run tree (slim content).
func (s *Store) GetFlowRunTree(ctx context.Context, id string) (*FlowRunTree, error) {
	fr, err := getFlowRun(ctx, s.rdb, id)
	if err != nil {
		return nil, err
	}
	tree, err := buildFlowRunTree(ctx, s.rdb, *fr)
	if err != nil {
		return nil, err
	}
	return &tree, nil
}

// GetEventFanOut returns the event (full payload) and, per flow run it
// started (live and replay kinds, oldest first), the node run tree showing each
// branch's decision path.
func (s *Store) GetEventFanOut(ctx context.Context, eventID string) (*EventFanOut, error) {
	ev, err := getEvent(ctx, s.rdb, eventID, false)
	if err != nil {
		return nil, err
	}
	runs, err := queryAll(ctx, s.rdb, scanFlowRun, "SELECT "+flowRunCols+" FROM flow_runs WHERE event_id = ? AND kind <> 'test' ORDER BY id", eventID)
	if err != nil {
		return nil, fmt.Errorf("store: fan-out: %w", err)
	}
	out := &EventFanOut{Event: *ev, FlowRuns: []FlowRunTree{}}
	for _, fr := range runs {
		tree, err := buildFlowRunTree(ctx, s.rdb, fr)
		if err != nil {
			return nil, err
		}
		out.FlowRuns = append(out.FlowRuns, tree)
	}
	return out, nil
}

func buildFlowRunTree(ctx context.Context, q dbq, fr FlowRun) (FlowRunTree, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+nodeRunCols(true)+" FROM node_runs WHERE flow_run_id = ? ORDER BY id", fr.ID)
	if err != nil {
		return FlowRunTree{}, fmt.Errorf("store: node runs of %s: %w", fr.ID, err)
	}
	defer func() { _ = rows.Close() }()
	byID := map[string]*NodeRunTree{}
	var ordered []*NodeRunTree
	for rows.Next() {
		n, err := scanNodeRun(rows)
		if err != nil {
			return FlowRunTree{}, err
		}
		nt := &NodeRunTree{NodeRun: n, Children: []*NodeRunTree{}}
		byID[n.ID] = nt
		ordered = append(ordered, nt)
	}
	if err := rows.Err(); err != nil {
		return FlowRunTree{}, err
	}
	tree := FlowRunTree{FlowRun: fr, Nodes: []*NodeRunTree{}}
	for _, nt := range ordered {
		if parent, ok := byID[nt.ParentNodeRunID]; ok && nt.ParentNodeRunID != "" {
			parent.Children = append(parent.Children, nt)
		} else {
			tree.Nodes = append(tree.Nodes, nt)
		}
	}
	return tree, nil
}
