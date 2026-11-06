package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// QueueReason is the reason recorded on a run removed from the queue.
const QueueReason = "removed from queue"

// ErrNotQueued is wrapped by CancelQueued when the run is not waiting in the
// queue (it started a node, finished, or waits for an approval).
var ErrNotQueued = errors.New("flow run is not waiting in the queue")

// QueueItem is one flow run waiting for a slot.
type QueueItem struct {
	FlowRun FlowRun
	// QueuedAt is when the wait began: the run's creation time for a queued
	// run, the oldest waiting node run's queue time for a running one.
	QueuedAt time.Time
	// WaitingNodeIDs are the node ids of the queued node runs (empty for a
	// run that has not been claimed yet).
	WaitingNodeIDs []string
	// WaitingType is the pool the oldest waiting node run needs; empty for a
	// run still waiting for a flow slot.
	WaitingType NodeType
}

// QueueState is the queue plus the slots currently in use.
type QueueState struct {
	// Items are ordered oldest wait first; within one kind of wait this is
	// the order the engine claims them in.
	Items []QueueItem
	// FlowRunning counts running live/replay flow runs per flow.
	FlowRunning map[string]int
	// NodeInUse counts the node runs holding a pool slot per node type.
	NodeInUse map[NodeType]int
}

// ListQueue returns the queue: queued flow runs (waiting for a flow slot) and
// running flow runs whose only unfinished node runs are queued (waiting for an
// agent or command slot). Test runs are excluded. flowID, when set, restricts
// the items to one flow.
func (s *Store) ListQueue(ctx context.Context, flowID string) (QueueState, error) {
	tx, err := s.rdb.BeginTx(ctx, nil)
	if err != nil {
		return QueueState{}, fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	t := &txn{Tx: tx, s: s}
	st := QueueState{}
	if st.FlowRunning, err = t.runningPerFlow(ctx); err != nil {
		return QueueState{}, err
	}
	if st.NodeInUse, err = t.nodeSlotsInUse(ctx); err != nil {
		return QueueState{}, err
	}
	q := "SELECT " + flowRunCols + " FROM flow_runs WHERE kind <> 'test' AND status IN ('queued','running')"
	var args []any
	if flowID != "" {
		q += " AND flow_id = ?"
		args = append(args, flowID)
	}
	runs, err := queryAll(ctx, tx, scanFlowRun, q+" ORDER BY id", args...)
	if err != nil {
		return QueueState{}, fmt.Errorf("store: list queue: %w", err)
	}
	for _, fr := range runs {
		if fr.Status == FlowRunQueued {
			st.Items = append(st.Items, QueueItem{FlowRun: fr, QueuedAt: fr.CreatedAt})
			continue
		}
		waiting, ok, err := queuedNodesOnly(ctx, tx, fr.ID)
		if err != nil {
			return QueueState{}, err
		}
		if !ok {
			continue
		}
		it := QueueItem{FlowRun: fr, QueuedAt: waiting[0].QueuedAt, WaitingType: waiting[0].NodeType}
		for _, n := range waiting {
			it.WaitingNodeIDs = append(it.WaitingNodeIDs, n.NodeID)
		}
		st.Items = append(st.Items, it)
	}
	sort.SliceStable(st.Items, func(i, j int) bool {
		a, b := st.Items[i], st.Items[j]
		if !a.QueuedAt.Equal(b.QueuedAt) {
			return a.QueuedAt.Before(b.QueuedAt)
		}
		return a.FlowRun.ID < b.FlowRun.ID
	})
	return st, nil
}

// queuedNodesOnly returns the queued node runs of a flow run (oldest first)
// and whether the run waits only on them: at least one is queued and no node
// run is running or awaiting approval.
func queuedNodesOnly(ctx context.Context, q dbq, flowRunID string) ([]NodeRun, bool, error) {
	nodes, err := queryAll(ctx, q, scanNodeRun, "SELECT "+nodeRunCols(true)+" FROM node_runs WHERE flow_run_id = ? AND status IN ('queued','running','awaiting_approval') ORDER BY id", flowRunID)
	if err != nil {
		return nil, false, fmt.Errorf("store: node runs of %s: %w", flowRunID, err)
	}
	if len(nodes) == 0 {
		return nil, false, nil
	}
	for _, n := range nodes {
		if n.Status != NodeQueued {
			return nil, false, nil
		}
	}
	return nodes, true, nil
}

// CancelQueued ends a flow run waiting in the queue: the flow run and its
// queued node runs become cancelled, recording by and QueueReason. It runs in
// the write transaction that also serialises the claimers, so a run is never
// both claimed and cancelled. A run that is not queued (see ListQueue) fails
// with an error wrapping ErrNotQueued; ErrNotFound if it does not exist.
func (s *Store) CancelQueued(ctx context.Context, flowRunID, by string) (*FlowRun, error) {
	var out *FlowRun
	err := s.write(ctx, func(t *txn) error {
		fr, err := getFlowRun(ctx, t, flowRunID)
		if err != nil {
			return err
		}
		if fr.Kind == KindTest {
			return fmt.Errorf("%w: test runs are not queued", ErrNotQueued)
		}
		now := s.Now()
		switch fr.Status {
		case FlowRunQueued:
		case FlowRunRunning:
			waiting, ok, err := queuedNodesOnly(ctx, t, fr.ID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: it is already running a node", ErrNotQueued)
			}
			for _, n := range waiting {
				if _, err := t.ExecContext(ctx, `UPDATE node_runs SET status='cancelled', decision='skipped', reason=?, ended_at=? WHERE id=? AND status='queued'`,
					QueueReason, ts(now), n.ID); err != nil {
					return fmt.Errorf("store: cancel node_run: %w", err)
				}
				t.note(ResNodeRun, n.ID, OpUpdate)
			}
		default:
			return fmt.Errorf("%w: its status is %s", ErrNotQueued, fr.Status)
		}
		if _, err := t.ExecContext(ctx, `UPDATE flow_runs SET status='cancelled', error=?, cancelled_by=?, started_at=COALESCE(started_at, ?), ended_at=? WHERE id=?`,
			QueueReason, by, ts(now), ts(now), fr.ID); err != nil {
			return fmt.Errorf("store: cancel flow_run: %w", err)
		}
		t.note(ResFlowRun, fr.ID, OpUpdate)
		if out, err = getFlowRun(ctx, t, fr.ID); err != nil {
			return err
		}
		return nil
	})
	return out, err
}
