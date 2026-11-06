package engine

import (
	"context"
	"fmt"
	"sync"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// RunFlow is "Run now" for a flow: it polls every sensor the flow subscribes to,
// in parallel, each as a normal manual sensor run, with this flow as the only
// target (other flows subscribed to those sensors get nothing). New items
// become events and live runs of this flow; with opts.RerunSeen, items whose
// key was already seen are replayed into it. It works for a flow with
// enabled: false: running it is an explicit user action.
//
// A sensor that cannot be polled (missing or invalid, or a poll of it is
// already running) is reported in its result; the others still run.
// store.ErrNotFound for a flow the engine does not know (missing, or with
// errors: it only runs valid files), a *core.ValidationError for one that
// subscribes to nothing, core.ErrPaused while the kill switch is engaged.
func (e *Engine) RunFlow(ctx context.Context, flowID string, opts core.RunFlowOptions) (core.RunFlowResult, error) {
	if e.isPaused() {
		return core.RunFlowResult{}, fmt.Errorf("%w: flow runs are rejected while the kill switch is engaged", core.ErrPaused)
	}
	fd := e.snapshot().flows[flowID]
	if fd == nil {
		return core.RunFlowResult{}, fmt.Errorf("%w: flow %q", store.ErrNotFound, flowID)
	}
	sensors := uniqueStrings(fd.flow.Subscribe)
	if len(sensors) == 0 {
		return core.RunFlowResult{}, &core.ValidationError{Diagnostics: []spec.Diagnostic{{
			Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "subscribe",
			Message: fmt.Sprintf("flow %q subscribes to no sensor, so there is nothing to run it on", flowID),
		}}}
	}
	results := make([]core.FlowSensorResult, len(sensors))
	var wg sync.WaitGroup
	for i, sensorID := range sensors {
		wg.Go(func() {
			results[i].SensorID = sensorID
			res, err := e.PollSensor(ctx, sensorID, core.PollOptions{RerunSeen: opts.RerunSeen, Flows: []string{flowID}})
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			results[i].Poll = &res
		})
	}
	wg.Wait()
	out := core.RunFlowResult{FlowID: flowID, Sensors: results, FlowRunIDs: []string{}}
	for _, r := range results {
		if r.Poll != nil {
			out.FlowRunIDs = append(out.FlowRunIDs, r.Poll.FlowRunIDs...)
		}
	}
	return out, nil
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
