package engine

import (
	"context"
	"errors"
	"fmt"

	"hal9001/internal/core"
	"hal9001/internal/store"
)

// RemoveQueued ends a flow run that is waiting in the queue as cancelled and
// records principal as who removed it. A run that already runs a node, or is
// not queued, fails with core.ErrInvalidState. The store claims and cancels
// in one serialised transaction, and the flow run lock keeps a concurrent
// reconcile from writing a stale status over the cancellation.
func (e *Engine) RemoveQueued(ctx context.Context, flowRunID, principal string) (*store.FlowRun, error) {
	unlock := e.locks.lock("flowrun:" + flowRunID)
	defer unlock()
	fr, err := e.st.CancelQueued(ctx, flowRunID, principal)
	switch {
	case errors.Is(err, store.ErrNotQueued):
		return nil, fmt.Errorf("%w: flow run %s cannot be removed from the queue: %s; use the kill switch or wait for it to finish",
			core.ErrInvalidState, flowRunID, trimSentinel(err))
	case err != nil:
		return nil, fmt.Errorf("engine: remove queued flow run: %w", err)
	}
	return fr, nil
}

// ClearQueue removes every run in the queue (of one flow when flowID is set)
// and returns the ids it cancelled. Runs that started a node meanwhile are
// left alone.
func (e *Engine) ClearQueue(ctx context.Context, flowID, principal string) ([]string, error) {
	q, err := e.st.ListQueue(ctx, flowID)
	if err != nil {
		return nil, fmt.Errorf("engine: list queue: %w", err)
	}
	ids := []string{}
	for _, it := range q.Items {
		_, err := e.RemoveQueued(ctx, it.FlowRun.ID, principal)
		switch {
		case err == nil:
			ids = append(ids, it.FlowRun.ID)
		case errors.Is(err, core.ErrInvalidState), errors.Is(err, store.ErrNotFound):
		default:
			return ids, err
		}
	}
	return ids, nil
}

// trimSentinel returns the message of err without the ErrNotQueued prefix.
func trimSentinel(err error) string {
	msg := err.Error()
	prefix := store.ErrNotQueued.Error() + ": "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return msg[len(prefix):]
	}
	return msg
}
