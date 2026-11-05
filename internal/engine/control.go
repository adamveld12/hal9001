package engine

import (
	"context"
	"fmt"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/store"
)

// Pause engages the kill switch: sensors stop polling and no new node run
// starts. With killRunning, running nodes are also cancelled (agents with
// agent.ErrKilled, commands by killing their process group) and end as
// killed. The state is persisted, so it survives a restart.
func (e *Engine) Pause(ctx context.Context, killRunning bool, principal string) (store.KillSwitch, error) {
	e.ctl.Lock()
	ks := store.KillSwitch{Paused: true, KillRunning: killRunning, Since: time.Now().UTC(), By: principal}
	if e.ks.Paused {
		ks.Since = e.ks.Since // already paused: keep when it began
	}
	e.ctl.Unlock()
	if err := e.st.SetKillSwitch(ctx, ks); err != nil {
		return store.KillSwitch{}, fmt.Errorf("engine: persist kill switch: %w", err)
	}
	e.ctl.Lock()
	e.ks = ks
	e.paused.Store(true)
	e.ctl.Unlock()
	if killRunning {
		e.cancelActive(agent.ErrKilled)
	}
	return ks, nil
}

// Resume releases the kill switch: sensors poll again and queued work starts.
func (e *Engine) Resume(ctx context.Context, principal string) (store.KillSwitch, error) {
	ks := store.KillSwitch{Paused: false, Since: time.Now().UTC(), By: principal}
	if err := e.st.SetKillSwitch(ctx, ks); err != nil {
		return store.KillSwitch{}, fmt.Errorf("engine: persist kill switch: %w", err)
	}
	e.ctl.Lock()
	e.ks = ks
	e.paused.Store(false)
	e.ctl.Unlock()
	e.syncSensors()
	e.wake()
	return ks, nil
}
