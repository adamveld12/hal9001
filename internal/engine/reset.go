package engine

import (
	"context"
	"fmt"

	"hal9001/internal/core"
	"hal9001/internal/store"
)

// ResetSensor clears everything a sensor remembers between polls: its dedup
// keys and last_success_at (see store.ResetSensor), and records who did it. Past
// events, runs and inbox items stay. The next poll behaves like the first poll
// of a new sensor: HAL_LAST_SUCCESS_AT is unset, every item is new, and a
// scheduled poll applies first_run (baseline or fire) again. It works while the
// sensor is paused.
//
// With opts.Run it then runs the sensor now as a manual poll (never baseline),
// restricted to opts.Flows when given, and returns that poll's result.
//
// store.ErrNotFound for an unknown sensor; core.ErrInvalidState while a poll of
// the sensor is running (nothing is changed); core.ErrPaused when opts.Run is
// set under the kill switch (nothing is changed); the run's flow validation
// errors as for PollSensor (also before anything is changed).
func (e *Engine) ResetSensor(ctx context.Context, sensorID, principal string, opts core.ResetOptions) (core.ResetResult, error) {
	snap := e.snapshot()
	if snap.sensors[sensorID] == nil {
		return core.ResetResult{}, fmt.Errorf("%w: sensor %q", store.ErrNotFound, sensorID)
	}
	if opts.Run {
		if e.isPaused() {
			return core.ResetResult{}, fmt.Errorf("%w: sensor polls are rejected while the kill switch is engaged", core.ErrPaused)
		}
		if _, err := pollTargets(snap, sensorID, opts.Flows); err != nil {
			return core.ResetResult{}, err
		}
	}
	// Hold the poll slot so no poll starts or is running while the state is cleared.
	e.pollMu.Lock()
	if e.polling[sensorID] {
		e.pollMu.Unlock()
		return core.ResetResult{}, fmt.Errorf("%w: a poll of sensor %q is running; try again when it has finished", core.ErrInvalidState, sensorID)
	}
	e.polling[sensorID] = true
	e.pollMu.Unlock()
	state, cleared, err := e.st.ResetSensor(e.dbCtx, sensorID, principal)
	e.pollMu.Lock()
	delete(e.polling, sensorID)
	e.pollMu.Unlock()
	if err != nil {
		return core.ResetResult{}, err
	}
	out := core.ResetResult{State: state, KeysCleared: cleared}
	if opts.Run {
		res, err := e.PollSensor(ctx, sensorID, core.PollOptions{Flows: opts.Flows})
		if err != nil {
			return out, err
		}
		out.Poll = &res
	}
	return out, nil
}
