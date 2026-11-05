package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"hal9001/internal/store"
)

const (
	pruneFirstDelay = time.Minute
	pruneInterval   = 24 * time.Hour
)

// retentionLoop prunes once a day, starting shortly after Start.
func (e *Engine) retentionLoop(ctx context.Context) {
	t := time.NewTimer(pruneFirstDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if res, err := e.Prune(ctx); err != nil {
			e.log.Error("engine: retention prune", "err", err)
		} else {
			e.log.Info("engine: retention prune", "flow_runs", res.FlowRuns, "events", res.Events, "blobs", len(res.OrphanBlobs), "worktrees", len(res.Worktrees))
		}
		t.Reset(pruneInterval)
	}
}

// Prune applies retention now (§9): it deletes old runs, events and sensor
// runs from the store (except anything tied to a starred inbox item or a
// pending approval), removes the blobs that became unreferenced, and removes
// the worktrees of the deleted agent runs.
func (e *Engine) Prune(ctx context.Context) (store.PruneResult, error) {
	return e.pruneAt(ctx, time.Now().UTC())
}

func (e *Engine) pruneAt(ctx context.Context, now time.Time) (store.PruneResult, error) {
	cfg := e.snapshot().cfg
	res, err := e.st.Prune(ctx, now, cfg.RetentionDays, cfg.DedupRetentionDays)
	if err != nil {
		return store.PruneResult{}, fmt.Errorf("engine: prune store: %w", err)
	}
	if _, err := e.st.RemoveOrphanBlobs(ctx, res.OrphanBlobs); err != nil {
		return res, fmt.Errorf("engine: remove orphan blobs: %w", err)
	}
	for _, path := range res.Worktrees {
		if filepath.Dir(path) != e.wt.Dir {
			continue // not ours to delete
		}
		if err := e.wt.Remove(ctx, filepath.Base(path)); err != nil {
			// No longer registered with its repository (the repo is gone): drop the directory.
			if rerr := os.RemoveAll(path); rerr != nil {
				e.log.Warn("engine: remove worktree", "path", path, "err", rerr)
			}
		}
	}
	return res, nil
}
