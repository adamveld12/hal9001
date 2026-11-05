package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CountEvents returns how many events a sensor created at or after since.
func (s *Store) CountEvents(ctx context.Context, sensorID string, since time.Time) (int, error) {
	var n int
	err := s.rdb.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE sensor_id = ? AND created_at >= ?`, sensorID, ts(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count events: %w", err)
	}
	return n, nil
}

// FlowRunStats summarises the live and replay runs of one flow.
//
// Runs, Failed and Finished cover runs created at or after the window start.
// Running, Queued and AwaitingApproval are current totals regardless of age.
// LastRun* describe the newest run of any age.
type FlowRunStats struct {
	Runs             int
	Finished         int // succeeded, failed, killed or interrupted
	Failed           int // failed, killed or interrupted
	Running          int
	Queued           int
	AwaitingApproval int
	LastRunID        string
	LastRunAt        *time.Time
	LastRunStatus    FlowRunStatus
}

// eachRow runs query on the reader pool and calls scan for every row.
func (s *Store) eachRow(ctx context.Context, what, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := s.rdb.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// FlowRunStats returns per-flow run statistics for live and replay runs, keyed
// by flow id. Flows without runs are absent.
func (s *Store) FlowRunStats(ctx context.Context, since time.Time) (map[string]FlowRunStats, error) {
	out := map[string]FlowRunStats{}
	err := s.eachRow(ctx, "flow run stats", `SELECT flow_id, status, COUNT(*) FROM flow_runs
		WHERE kind <> 'test' AND created_at >= ? GROUP BY flow_id, status`, []any{ts(since)}, func(r *sql.Rows) error {
		var flow, status string
		var n int
		if err := r.Scan(&flow, &status, &n); err != nil {
			return err
		}
		st := out[flow]
		st.Runs += n
		switch FlowRunStatus(status) {
		case FlowRunSucceeded:
			st.Finished += n
		case FlowRunFailed, FlowRunKilled, FlowRunInterrupted:
			st.Finished += n
			st.Failed += n
		}
		out[flow] = st
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = s.eachRow(ctx, "flow run stats", `SELECT flow_id, status, COUNT(*) FROM flow_runs
		WHERE kind <> 'test' AND status IN ('running','queued','awaiting_approval') GROUP BY flow_id, status`, nil, func(r *sql.Rows) error {
		var flow, status string
		var n int
		if err := r.Scan(&flow, &status, &n); err != nil {
			return err
		}
		st := out[flow]
		switch FlowRunStatus(status) {
		case FlowRunRunning:
			st.Running = n
		case FlowRunQueued:
			st.Queued = n
		case FlowRunAwaitingApproval:
			st.AwaitingApproval = n
		}
		out[flow] = st
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = s.eachRow(ctx, "flow run stats", `SELECT flow_id, id, status, created_at FROM flow_runs fr
		WHERE kind <> 'test' AND id = (SELECT MAX(id) FROM flow_runs WHERE flow_id = fr.flow_id AND kind <> 'test')`, nil, func(r *sql.Rows) error {
		var flow, id, status, created string
		if err := r.Scan(&flow, &id, &status, &created); err != nil {
			return err
		}
		at, err := parseTS(created)
		if err != nil {
			return err
		}
		st := out[flow]
		st.LastRunID, st.LastRunAt, st.LastRunStatus = id, &at, FlowRunStatus(status)
		out[flow] = st
		return nil
	})
	return out, err
}

// FlowVersionRunCounts returns, per flow version hash, how many flow runs
// (any kind) used it.
func (s *Store) FlowVersionRunCounts(ctx context.Context, flowID string) (map[string]int, error) {
	out := map[string]int{}
	err := s.eachRow(ctx, "flow version run counts", `SELECT flow_hash, COUNT(*) FROM flow_runs WHERE flow_id = ? GROUP BY flow_hash`,
		[]any{flowID}, func(r *sql.Rows) error {
			var h string
			var n int
			if err := r.Scan(&h, &n); err != nil {
				return err
			}
			out[h] = n
			return nil
		})
	return out, err
}
