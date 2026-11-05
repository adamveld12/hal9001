package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// contentField names one content triple of a row so prepContent can be applied
// to several columns at once.
type contentField struct {
	name string
	c    *Content
}

// prep normalises each field in place to its stored form (spilling large text
// to blobs). id is empty on create; on update it lets unhydrated inline
// content be preserved from the current row.
func (t *txn) prep(ctx context.Context, table, id string, fields ...contentField) error {
	for _, f := range fields {
		var load func() (Content, error)
		if id != "" {
			load = t.loadContent(ctx, table, f.name, "id", id)
		}
		stored, err := t.prepContent(*f.c, load)
		if err != nil {
			return err
		}
		*f.c = stored
	}
	return nil
}

func sensorRunCols(slim bool) string {
	return "id, sensor_id, origin, status, baseline, started_at, ended_at, duration_ms, exit_code, error, item_count, new_count, " +
		contentCols("stdout", slim) + ", " + contentCols("stderr", slim)
}

func scanSensorRun(r rowScanner) (SensorRun, error) {
	var (
		sr             SensorRun
		started        string
		ended          sql.NullString
		dur, exit      sql.NullInt64
		baseline       int
		origin, status string
	)
	dests := []any{&sr.ID, &sr.SensorID, &origin, &status, &baseline, &started, &ended, &dur, &exit,
		&sr.Error, &sr.ItemCount, &sr.NewCount}
	dests = append(dests, scanDests(&sr.Stdout)...)
	dests = append(dests, scanDests(&sr.Stderr)...)
	if err := r.Scan(dests...); err != nil {
		return SensorRun{}, scanErr(err)
	}
	var err error
	sr.Origin, sr.Status, sr.Baseline = SensorOrigin(origin), SensorStatus(status), baseline != 0
	if sr.StartedAt, err = parseTS(started); err != nil {
		return SensorRun{}, err
	}
	if sr.EndedAt, err = parseTSPtr(ended); err != nil {
		return SensorRun{}, err
	}
	if dur.Valid {
		sr.DurationMS = &dur.Int64
	}
	if exit.Valid {
		v := int(exit.Int64)
		sr.ExitCode = &v
	}
	return sr, nil
}

// CreateSensorRun inserts r, assigning ID, Origin (schedule), Status (running)
// and StartedAt (now) when unset.
func (s *Store) CreateSensorRun(ctx context.Context, r *SensorRun) error {
	if r.SensorID == "" {
		return fmt.Errorf("%w: sensor_id required", ErrInvalid)
	}
	if r.ID == "" {
		r.ID = s.NewID()
	}
	if r.Origin == "" {
		r.Origin = OriginSchedule
	}
	if r.Status == "" {
		r.Status = SensorRunning
	}
	if !r.Status.valid() {
		return fmt.Errorf("%w: sensor run status %q", ErrInvalid, r.Status)
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = s.Now()
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.prep(ctx, "sensor_runs", "", contentField{"stdout", &r.Stdout}, contentField{"stderr", &r.Stderr}); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO sensor_runs (id, sensor_id, origin, status, baseline, started_at, ended_at, duration_ms, exit_code, error, item_count, new_count,
			stdout_text, stdout_ref, stdout_size, stderr_text, stderr_ref, stderr_size)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.ID, r.SensorID, r.Origin, r.Status, b2i(r.Baseline), ts(r.StartedAt), tsOrNil(r.EndedAt), nullInt64(r.DurationMS), nullInt(r.ExitCode),
			r.Error, r.ItemCount, r.NewCount,
			r.Stdout.Text, r.Stdout.Ref, r.Stdout.Size, r.Stderr.Text, r.Stderr.Ref, r.Stderr.Size)
		if err != nil {
			return fmt.Errorf("store: insert sensor_run: %w", err)
		}
		t.note(ResSensorRun, r.ID, OpCreate)
		return nil
	})
}

// UpdateSensorRun replaces every mutable column of the run with r's values.
func (s *Store) UpdateSensorRun(ctx context.Context, r *SensorRun) error {
	if !r.Status.valid() {
		return fmt.Errorf("%w: sensor run status %q", ErrInvalid, r.Status)
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.prep(ctx, "sensor_runs", r.ID, contentField{"stdout", &r.Stdout}, contentField{"stderr", &r.Stderr}); err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `UPDATE sensor_runs SET status=?, baseline=?, ended_at=?, duration_ms=?, exit_code=?, error=?, item_count=?, new_count=?,
			stdout_text=?, stdout_ref=?, stdout_size=?, stderr_text=?, stderr_ref=?, stderr_size=? WHERE id=?`,
			r.Status, b2i(r.Baseline), tsOrNil(r.EndedAt), nullInt64(r.DurationMS), nullInt(r.ExitCode), r.Error, r.ItemCount, r.NewCount,
			r.Stdout.Text, r.Stdout.Ref, r.Stdout.Size, r.Stderr.Text, r.Stderr.Ref, r.Stderr.Size, r.ID)
		if err != nil {
			return fmt.Errorf("store: update sensor_run: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResSensorRun, r.ID, OpUpdate)
		return nil
	})
}

// GetSensorRun returns one run with full inline output.
func (s *Store) GetSensorRun(ctx context.Context, id string) (*SensorRun, error) {
	sr, err := scanSensorRun(s.rdb.QueryRowContext(ctx, "SELECT "+sensorRunCols(false)+" FROM sensor_runs WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &sr, nil
}

// SensorRunFilter selects sensor runs for ListSensorRuns.
type SensorRunFilter struct {
	SensorID string
	Status   SensorStatus
	Time     TimeRange // on started_at
	Slim     bool      // omit inline stdout/stderr text
	ListOpts
}

// ListSensorRuns lists runs, newest first.
func (s *Store) ListSensorRuns(ctx context.Context, f SensorRunFilter) (Page[SensorRun], error) {
	var w where
	w.eq("sensor_id", f.SensorID)
	w.eq("status", string(f.Status))
	w.timeRange("started_at", f.Time)
	return listPage(ctx, s.rdb, "SELECT "+sensorRunCols(f.Slim)+" FROM sensor_runs", &w, f.ListOpts, "id",
		scanSensorRun, func(r SensorRun) string { return r.ID })
}

// GetSensorState returns the sensor's bookkeeping row. A sensor that has never
// run yields a zero state (nil LastSuccessAt), not an error.
func (s *Store) GetSensorState(ctx context.Context, sensorID string) (SensorState, error) {
	st, err := scanSensorState(s.rdb.QueryRowContext(ctx, sensorStateSQL+" WHERE sensor_id = ?", sensorID))
	if errors.Is(err, ErrNotFound) {
		return SensorState{SensorID: sensorID}, nil
	}
	return st, err
}

// ListSensorStates returns the state of every sensor that has run, by sensor id.
func (s *Store) ListSensorStates(ctx context.Context) ([]SensorState, error) {
	rows, err := s.rdb.QueryContext(ctx, sensorStateSQL+" ORDER BY sensor_id")
	if err != nil {
		return nil, fmt.Errorf("store: list sensor_state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []SensorState{}
	for rows.Next() {
		st, err := scanSensorState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

const sensorStateSQL = `SELECT sensor_id, last_run_id, last_run_at, last_success_at, last_error, consecutive_failures, updated_at FROM sensor_state`

func scanSensorState(r rowScanner) (SensorState, error) {
	var st SensorState
	var lastRun, lastOK sql.NullString
	var updated string
	if err := r.Scan(&st.SensorID, &st.LastRunID, &lastRun, &lastOK, &st.LastError, &st.ConsecutiveFailures, &updated); err != nil {
		return SensorState{}, scanErr(err)
	}
	var err error
	if st.LastRunAt, err = parseTSPtr(lastRun); err != nil {
		return SensorState{}, err
	}
	if st.LastSuccessAt, err = parseTSPtr(lastOK); err != nil {
		return SensorState{}, err
	}
	if st.UpdatedAt, err = parseTS(updated); err != nil {
		return SensorState{}, err
	}
	return st, nil
}

// RecordSensorOutcome updates the sensor's state after a poll finishes at the
// given time. Success sets LastSuccessAt and clears the failure streak;
// failure leaves LastSuccessAt untouched (REQUIREMENTS §5) and counts up.
func (s *Store) RecordSensorOutcome(ctx context.Context, sensorID, runID string, at time.Time, ok bool, errMsg string) error {
	if sensorID == "" {
		return fmt.Errorf("%w: sensor_id required", ErrInvalid)
	}
	return s.write(ctx, func(t *txn) error {
		now := ts(s.now())
		var err error
		if ok {
			_, err = t.ExecContext(ctx, `INSERT INTO sensor_state (sensor_id, last_run_id, last_run_at, last_success_at, last_error, consecutive_failures, updated_at)
				VALUES (?, ?, ?, ?, '', 0, ?)
				ON CONFLICT (sensor_id) DO UPDATE SET last_run_id=excluded.last_run_id, last_run_at=excluded.last_run_at,
				  last_success_at=excluded.last_success_at, last_error='', consecutive_failures=0, updated_at=excluded.updated_at`,
				sensorID, runID, ts(at), ts(at), now)
		} else {
			_, err = t.ExecContext(ctx, `INSERT INTO sensor_state (sensor_id, last_run_id, last_run_at, last_error, consecutive_failures, updated_at)
				VALUES (?, ?, ?, ?, 1, ?)
				ON CONFLICT (sensor_id) DO UPDATE SET last_run_id=excluded.last_run_id, last_run_at=excluded.last_run_at,
				  last_error=excluded.last_error, consecutive_failures=sensor_state.consecutive_failures+1, updated_at=excluded.updated_at`,
				sensorID, runID, ts(at), errMsg, now)
		}
		if err != nil {
			return fmt.Errorf("store: record sensor outcome: %w", err)
		}
		t.note(ResSensorState, sensorID, OpUpdate)
		return nil
	})
}

// claimKey inserts the dedup key and reports whether this call was the first.
func (t *txn) claimKey(ctx context.Context, sensorID, key string, at time.Time) (bool, error) {
	res, err := t.ExecContext(ctx, `INSERT INTO seen_keys (sensor_id, dedup_key, first_seen) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
		sensorID, key, ts(at))
	if err != nil {
		return false, fmt.Errorf("store: claim key: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClaimEvent records (sensorID, key) as seen and reports whether it was new.
// It is atomic: of any number of concurrent callers with the same pair,
// exactly one gets true. Use it to baseline items without creating events; use
// IngestEvent to dedup and create the event in one step.
func (s *Store) ClaimEvent(ctx context.Context, sensorID, key string) (bool, error) {
	if sensorID == "" {
		return false, fmt.Errorf("%w: sensor_id required", ErrInvalid)
	}
	var isNew bool
	err := s.write(ctx, func(t *txn) error {
		var err error
		isNew, err = t.claimKey(ctx, sensorID, key, s.now())
		return err
	})
	return isNew, err
}

// IngestEvent atomically claims ev's dedup key and, if the key is new, inserts
// the event. It returns false (and inserts nothing) when the key was already
// seen, including keys whose events have been pruned.
func (s *Store) IngestEvent(ctx context.Context, ev *Event) (bool, error) {
	if ev.SensorID == "" {
		return false, fmt.Errorf("%w: sensor_id required", ErrInvalid)
	}
	var created bool
	err := s.write(ctx, func(t *txn) error {
		now := s.now()
		var err error
		if created, err = t.claimKey(ctx, ev.SensorID, ev.DedupKey, now); err != nil || !created {
			return err
		}
		if ev.ID == "" {
			ev.ID = s.NewID()
		}
		if ev.CreatedAt.IsZero() {
			ev.CreatedAt = now.UTC()
		}
		if err := t.prep(ctx, "events", "", contentField{"payload", &ev.Payload}); err != nil {
			return err
		}
		_, err = t.ExecContext(ctx, `INSERT INTO events (id, sensor_id, sensor_run_id, dedup_key, payload_text, payload_ref, payload_size, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			ev.ID, ev.SensorID, nullStr(ev.SensorRunID), ev.DedupKey, ev.Payload.Text, ev.Payload.Ref, ev.Payload.Size, ts(ev.CreatedAt))
		if err != nil {
			return fmt.Errorf("store: insert event: %w", err)
		}
		t.note(ResEvent, ev.ID, OpCreate)
		return nil
	})
	if err != nil {
		created = false
	}
	return created, err
}

func eventCols(slim bool) string {
	return "id, sensor_id, COALESCE(sensor_run_id, ''), dedup_key, " + contentCols("payload", slim) + ", created_at"
}

func scanEvent(r rowScanner) (Event, error) {
	var e Event
	var created string
	dests := []any{&e.ID, &e.SensorID, &e.SensorRunID, &e.DedupKey}
	dests = append(dests, scanDests(&e.Payload)...)
	dests = append(dests, &created)
	if err := r.Scan(dests...); err != nil {
		return Event{}, scanErr(err)
	}
	var err error
	e.CreatedAt, err = parseTS(created)
	return e, err
}

// GetEvent returns one event with full inline payload.
func (s *Store) GetEvent(ctx context.Context, id string) (*Event, error) {
	return getEvent(ctx, s.rdb, id, false)
}

func getEvent(ctx context.Context, q dbq, id string, slim bool) (*Event, error) {
	e, err := scanEvent(q.QueryRowContext(ctx, "SELECT "+eventCols(slim)+" FROM events WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// EventFilter selects events for ListEvents.
//
// FlowID, RunStatuses, RunKinds and RunDecision restrict to events that have
// at least one flow run matching all of the given run conditions at once.
type EventFilter struct {
	SensorID    string
	FlowID      string
	RunStatuses []FlowRunStatus
	RunKinds    []FlowRunKind
	RunDecision NodeDecision // run has a node run with this decision
	Search      string       // case-insensitive substring of the inline payload or dedup key
	Time        TimeRange
	Slim        bool // omit inline payload text
	ListOpts
}

// ListEvents lists events, newest first. Search covers inline payloads (up to
// 64 KiB) and the dedup key; a payload spilled to a blob is not searched.
func (s *Store) ListEvents(ctx context.Context, f EventFilter) (Page[Event], error) {
	var w where
	w.eq("sensor_id", f.SensorID)
	w.timeRange("created_at", f.Time)
	if f.FlowID != "" || len(f.RunStatuses) > 0 || len(f.RunKinds) > 0 || f.RunDecision != "" {
		var rw where
		rw.add("fr.event_id = events.id")
		rw.eq("fr.flow_id", f.FlowID)
		rw.in("fr.status", strs(f.RunStatuses))
		rw.in("fr.kind", strs(f.RunKinds))
		if f.RunDecision != "" {
			rw.add("EXISTS (SELECT 1 FROM node_runs n WHERE n.flow_run_id = fr.id AND n.decision = ?)", f.RunDecision)
		}
		w.add("EXISTS (SELECT 1 FROM flow_runs fr"+rw.clause()+")", rw.args...)
	}
	if f.Search != "" {
		w.add("(instr(lower(payload_text), lower(?)) > 0 OR instr(lower(dedup_key), lower(?)) > 0)", f.Search, f.Search)
	}
	return listPage(ctx, s.rdb, "SELECT "+eventCols(f.Slim)+" FROM events", &w, f.ListOpts, "id",
		scanEvent, func(e Event) string { return e.ID })
}
