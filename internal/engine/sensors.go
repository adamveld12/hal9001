package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// sensorLoop is the scheduler goroutine of one sensor.
type sensorLoop struct {
	cancel context.CancelFunc
	sig    string
}

// syncSensors starts a scheduler for every scheduled sensor that an enabled
// flow subscribes to, and stops those that lost their last subscriber, were
// removed or changed. A manual-only sensor (no interval, no cron) never gets
// one. A poll that is running keeps running: polls are bound to the engine,
// not to their scheduler.
func (e *Engine) syncSensors() {
	if !e.started.Load() || e.stopping.Load() {
		return
	}
	snap := e.snapshot()
	e.loopMu.Lock()
	defer e.loopMu.Unlock()
	for id, l := range e.loops {
		s := snap.sensors[id]
		if s == nil || len(snap.subs[id]) == 0 || sensorSig(s) != l.sig {
			l.cancel()
			delete(e.loops, id)
		}
	}
	for id, s := range snap.sensors {
		if len(snap.subs[id]) == 0 || e.loops[id] != nil {
			continue
		}
		sched, err := s.Schedule()
		if err != nil {
			e.log.Warn("engine: sensor has no usable schedule", "sensor", id, "err", err)
			continue
		}
		if sched.Manual() {
			continue
		}
		lctx, cancel := context.WithCancel(e.ctx)
		if !e.spawn(func(context.Context) { e.sensorLoop(lctx, id, sched) }) {
			cancel()
			return
		}
		e.loops[id] = &sensorLoop{cancel: cancel, sig: sensorSig(s)}
	}
}

func (e *Engine) stopLoops() {
	e.loopMu.Lock()
	defer e.loopMu.Unlock()
	for id, l := range e.loops {
		l.cancel()
		delete(e.loops, id)
	}
}

// sensorLoop polls on schedule until ctx ends. A tick that finds the previous
// poll still running (or the kill switch engaged) is skipped.
func (e *Engine) sensorLoop(ctx context.Context, id string, sched spec.Schedule) {
	next := e.firstDue(ctx, id, sched)
	for {
		t := e.clock.NewTimer(next.Sub(e.scheduleNow()))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		if !e.isPaused() && !e.sensorPaused(ctx, id) {
			_, done, err := e.startPoll(id, store.OriginSchedule, core.PollOptions{})
			switch {
			case err == nil:
				select {
				case <-done:
				case <-ctx.Done():
					return
				}
			case !errors.Is(err, core.ErrInvalidState) && !errors.Is(err, store.ErrNotFound):
				e.log.Warn("engine: scheduled poll", "sensor", id, "err", err)
			}
		}
		if next = sched.Next(e.scheduleNow()); next.IsZero() {
			return
		}
	}
}

// sensorPaused reports whether the sensor is paused individually. A state that
// cannot be read counts as not paused: a poll is cheaper than a silent outage.
func (e *Engine) sensorPaused(ctx context.Context, id string) bool {
	st, err := e.st.GetSensorState(ctx, id)
	return err == nil && st.PausedAt != nil
}

// PauseSensor stops the scheduler polling the sensor until ResumeSensor. The
// state is persisted. "Poll now" and event injection keep working: they are
// explicit user actions. Pausing a paused sensor changes nothing.
func (e *Engine) PauseSensor(ctx context.Context, sensorID, principal string) (store.SensorState, error) {
	if e.snapshot().sensors[sensorID] == nil {
		return store.SensorState{}, fmt.Errorf("%w: sensor %q", store.ErrNotFound, sensorID)
	}
	return e.st.SetSensorPaused(ctx, sensorID, true, principal)
}

// ResumeSensor lets the scheduler poll the sensor again. Its scheduler starts
// over, so an overdue interval sensor polls at once, as after a restart.
func (e *Engine) ResumeSensor(ctx context.Context, sensorID string) (store.SensorState, error) {
	if e.snapshot().sensors[sensorID] == nil {
		return store.SensorState{}, fmt.Errorf("%w: sensor %q", store.ErrNotFound, sensorID)
	}
	st, err := e.st.SetSensorPaused(ctx, sensorID, false, "")
	if err != nil {
		return store.SensorState{}, err
	}
	e.loopMu.Lock()
	if l := e.loops[sensorID]; l != nil {
		l.cancel()
		delete(e.loops, sensorID)
	}
	e.loopMu.Unlock()
	e.syncSensors()
	return st, nil
}

// scheduleNow is the engine clock's time in the local zone: cron expressions
// are evaluated in the zone of the time they are given.
func (e *Engine) scheduleNow() time.Time { return e.clock.Now().Local() }

// firstDue is when a (re)started scheduler first polls: an interval sensor
// polls at once if it is overdue, a cron sensor waits for its next match.
func (e *Engine) firstDue(ctx context.Context, id string, sched spec.Schedule) time.Time {
	now := e.scheduleNow()
	if sched.Cron != nil {
		return sched.Next(now)
	}
	if st, err := e.st.GetSensorState(ctx, id); err == nil && st.LastRunAt != nil {
		if due := sched.Next(*st.LastRunAt); due.After(now) {
			return due
		}
	}
	return now
}

// PollSensor runs one poll now (works for any valid sensor, subscribed or
// not) and returns the recorded run once it has finished. If ctx ends first
// the run keeps going and its current, unfinished record is returned, without
// counts. ErrPaused while the kill switch is engaged, ErrInvalidState when a
// poll of this sensor is already running.
//
// A manual poll never baselines: on a sensor's very first successful poll its
// items become events even with first_run: baseline. opts picks the target
// flows and whether already-seen items are re-run (see core.PollOptions); a
// *core.ValidationError reports a flow that does not subscribe to the sensor,
// store.ErrNotFound an unknown one.
func (e *Engine) PollSensor(ctx context.Context, sensorID string, opts core.PollOptions) (core.PollResult, error) {
	if e.isPaused() {
		return core.PollResult{}, fmt.Errorf("%w: sensor polls are rejected while the kill switch is engaged", core.ErrPaused)
	}
	run, done, err := e.startPoll(sensorID, store.OriginManual, opts)
	if err != nil {
		return core.PollResult{}, err
	}
	select {
	case final := <-done:
		return final, nil
	case <-ctx.Done():
		return core.PollResult{Run: run}, nil
	}
}

// pollPlan is what one poll does with its items, and what it did.
type pollPlan struct {
	manual    bool     // an explicit user action: no first_run baseline
	rerunSeen bool     // replay items whose key was already seen
	targets   []string // flows that receive new events and re-runs

	newEvents, rerunEvents int
	flowRunIDs             []string
}

func (p *pollPlan) result(run *store.SensorRun) core.PollResult {
	return core.PollResult{Run: run, NewEvents: p.newEvents, RerunEvents: p.rerunEvents, FlowRunIDs: p.flowRunIDs}
}

// pollTargets resolves the flows a poll feeds: the enabled flows subscribed to
// the sensor, or, when flows is given, exactly those flows (each must exist
// and subscribe to the sensor; it may be disabled).
func pollTargets(snap *snapshot, sensorID string, flows []string) ([]string, error) {
	if len(flows) == 0 {
		return snap.subs[sensorID], nil
	}
	var targets []string
	for _, id := range flows {
		fd := snap.flows[id]
		if fd == nil {
			return nil, fmt.Errorf("%w: flow %q", store.ErrNotFound, id)
		}
		if !slices.Contains(fd.flow.Subscribe, sensorID) {
			return nil, &core.ValidationError{Diagnostics: []spec.Diagnostic{{
				Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "flows",
				Message: fmt.Sprintf("flow %q does not subscribe to sensor %q", id, sensorID),
			}}}
		}
		if !slices.Contains(targets, id) {
			targets = append(targets, id)
		}
	}
	return targets, nil
}

// startPoll records a running SensorRun and polls in the background. done
// delivers the finished record. It fails with ErrInvalidState while another
// poll of the sensor is in flight.
func (e *Engine) startPoll(sensorID string, origin store.SensorOrigin, opts core.PollOptions) (*store.SensorRun, <-chan core.PollResult, error) {
	snap := e.snapshot()
	sensor := snap.sensors[sensorID]
	if sensor == nil {
		return nil, nil, fmt.Errorf("%w: sensor %q", store.ErrNotFound, sensorID)
	}
	targets, err := pollTargets(snap, sensorID, opts.Flows)
	if err != nil {
		return nil, nil, err
	}
	plan := &pollPlan{manual: origin == store.OriginManual, rerunSeen: opts.RerunSeen, targets: targets}
	e.pollMu.Lock()
	if e.polling[sensorID] {
		e.pollMu.Unlock()
		return nil, nil, fmt.Errorf("%w: a poll of sensor %q is already running", core.ErrInvalidState, sensorID)
	}
	e.polling[sensorID] = true
	e.pollMu.Unlock()
	release := sync.OnceFunc(func() {
		e.pollMu.Lock()
		delete(e.polling, sensorID)
		e.pollMu.Unlock()
	})

	run := &store.SensorRun{SensorID: sensorID, Origin: origin, Status: store.SensorRunning}
	if err := e.st.CreateSensorRun(e.dbCtx, run); err != nil {
		release()
		return nil, nil, err
	}
	done := make(chan core.PollResult, 1)
	started := e.spawn(func(ctx context.Context) {
		defer release()
		pctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		untrack := e.track("poll:"+run.ID, cancel)
		defer untrack()
		final := e.doPoll(pctx, run, sensor, snap, plan)
		// Free the sensor before reporting: whoever receives the result may poll again at once (the scheduler
		// re-arms and the next tick fires, an API client posts another poll) and must not find this poll running.
		release()
		done <- plan.result(final)
	})
	if !started {
		release()
		e.failSensorRun(run, errors.New("engine is stopping"))
		return nil, nil, errors.New("engine: stopping")
	}
	return run, done, nil
}

func (e *Engine) failSensorRun(run *store.SensorRun, cause error) {
	now := time.Now().UTC()
	run.Status, run.Error, run.EndedAt = store.SensorFailed, cause.Error(), &now
	if err := e.st.UpdateSensorRun(e.dbCtx, run); err != nil {
		e.log.Warn("engine: record sensor run failure", "run", run.ID, "err", err)
	}
}

type polledItem struct {
	raw []byte
	key string
}

// doPoll executes the sensor command and ingests its items (§5). The plan says
// where new events go, whether seen items are re-run, and collects what the
// poll started.
func (e *Engine) doPoll(ctx context.Context, run *store.SensorRun, sensor *spec.Sensor, snap *snapshot, plan *pollPlan) *store.SensorRun {
	state, err := e.st.GetSensorState(e.dbCtx, sensor.ID)
	if err != nil {
		return e.finishPoll(run, snap, nil, fmt.Errorf("load sensor state: %w", err))
	}
	var last time.Time
	if state.LastSuccessAt != nil {
		last = *state.LastSuccessAt
	}
	env, cleanup, err := runner.BuildEnv(runner.EnvOptions{
		Path:          e.opt.BasePATH,
		EnvFile:       snap.pollEnv(sensor.ID),
		RunID:         run.ID,
		Node:          sensor.ID,
		StateDir:      filepath.Join(e.data, "state", "_sensors", sensor.ID),
		Sensor:        true,
		LastSuccessAt: last,
	})
	if err != nil {
		return e.finishPoll(run, snap, nil, err)
	}
	defer cleanup()
	res, runErr := runner.Run(ctx, runner.Spec{
		Lang:    runner.Lang(sensor.Lang),
		Script:  sensor.Script,
		Command: sensor.Command,
		Cwd:     sensor.Cwd,
		Env:     env,
		BaseEnv: map[string]string{},
		Timeout: sensor.Timeout.Std(),
	})
	if runErr != nil {
		return e.finishPoll(run, snap, &res, fmt.Errorf("run sensor: %w", runErr))
	}
	res = snap.red.Result(res)
	switch {
	case res.TimedOut:
		return e.finishPoll(run, snap, &res, fmt.Errorf("timeout after %s", sensor.Timeout.Std()))
	case res.Killed:
		return e.finishPoll(run, snap, &res, context.Cause(ctx))
	case res.ExitCode != 0:
		return e.finishPoll(run, snap, &res, fmt.Errorf("exit status %d", res.ExitCode))
	case res.StdoutTruncated:
		return e.finishPoll(run, snap, &res, fmt.Errorf("stdout exceeded the output cap (%d bytes)", res.StdoutBytes))
	}
	raw, err := runner.ParseSensorItems(res.Stdout)
	if err != nil {
		return e.finishPoll(run, snap, &res, err)
	}
	items, err := keyItems(sensor, raw)
	if err != nil {
		return e.finishPoll(run, snap, &res, err)
	}
	run.ItemCount = len(items)
	// An explicit poll fires what it finds, even on the sensor's very first
	// run: baselining is for the scheduler's unattended first poll.
	baseline := state.LastSuccessAt == nil && sensor.FirstRun == spec.FirstRunBaseline && !plan.manual
	run.Baseline = baseline
	handled := make(map[string]bool, len(items))
	for _, it := range items {
		if ctx.Err() != nil {
			return e.finishPoll(run, snap, &res, context.Cause(ctx))
		}
		if baseline {
			if _, err := e.st.ClaimEvent(e.dbCtx, sensor.ID, it.key); err != nil {
				return e.finishPoll(run, snap, &res, fmt.Errorf("baseline item: %w", err))
			}
			continue
		}
		if handled[it.key] {
			continue // the same key twice in one poll is one item
		}
		handled[it.key] = true
		created, _, runIDs, err := e.ingest(snap, run.ID, sensor.ID, it, plan.targets)
		if err != nil {
			return e.finishPoll(run, snap, &res, err)
		}
		switch {
		case created:
			run.NewCount++
			plan.newEvents++
			plan.flowRunIDs = append(plan.flowRunIDs, runIDs...)
		case plan.rerunSeen:
			runIDs, err := e.rerunSeen(snap, run.ID, sensor.ID, it, plan.targets)
			if err != nil {
				return e.finishPoll(run, snap, &res, err)
			}
			if len(runIDs) > 0 {
				plan.rerunEvents++
				plan.flowRunIDs = append(plan.flowRunIDs, runIDs...)
			}
		}
	}
	return e.finishPoll(run, snap, &res, nil)
}

// finishPoll records the outcome of a poll. Failures do not advance
// last_success_at; a poll cut short by shutdown is not recorded at all.
func (e *Engine) finishPoll(run *store.SensorRun, snap *snapshot, res *runner.Result, cause error) *store.SensorRun {
	now := time.Now().UTC()
	run.EndedAt = &now
	d := now.Sub(run.StartedAt).Milliseconds()
	run.DurationMS = &d
	if res != nil {
		run.Stdout, run.Stderr = store.Inline(string(res.Stdout)), store.Inline(string(res.Stderr))
		code := res.ExitCode
		run.ExitCode = &code
	}
	interrupted := cause != nil && errors.Is(cause, agent.ErrInterrupted)
	switch {
	case cause == nil:
		run.Status = store.SensorSucceeded
	case interrupted:
		run.Status, run.Error = store.SensorInterrupted, "daemon stopped"
	default:
		run.Status, run.Error = store.SensorFailed, snap.red.String(cause.Error())
		if errors.Is(cause, agent.ErrKilled) {
			run.Error = "killed by the kill switch"
		}
	}
	if err := e.st.UpdateSensorRun(e.dbCtx, run); err != nil {
		e.log.Error("engine: record sensor run", "run", run.ID, "err", err)
	}
	if !interrupted {
		if err := e.st.RecordSensorOutcome(e.dbCtx, run.SensorID, run.ID, now, cause == nil, run.Error); err != nil {
			e.log.Error("engine: record sensor outcome", "sensor", run.SensorID, "err", err)
		}
	}
	return run
}

// keyItems computes the dedup key of every item. One item whose key cannot be
// rendered fails the poll: a key template that does not fit the output is a
// sensor bug, and nothing is ingested from a poll like that.
func keyItems(sensor *spec.Sensor, raw []json.RawMessage) ([]polledItem, error) {
	items := make([]polledItem, 0, len(raw))
	for i, r := range raw {
		key, err := dedupKey(sensor, r)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i+1, err)
		}
		items = append(items, polledItem{raw: r, key: key})
	}
	return items, nil
}

func dedupKey(sensor *spec.Sensor, item []byte) (string, error) {
	v, err := decodeJSON(item)
	if err != nil {
		return "", fmt.Errorf("item is not valid JSON: %w", err)
	}
	key, err := spec.RenderNamed("key", sensor.Key, v)
	if err != nil {
		return "", fmt.Errorf("dedup key: %w", err)
	}
	return key, nil
}

// decodeJSON decodes b keeping numbers as their literal text, so ids such as
// 1234567890 render unchanged in templates.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// ingest dedups one item into an event and fans it out to the target flows.
// created reports whether the key was new.
func (e *Engine) ingest(snap *snapshot, sensorRunID, sensorID string, it polledItem, targets []string) (created bool, ev *store.Event, flowRunIDs []string, err error) {
	ev = &store.Event{SensorID: sensorID, SensorRunID: sensorRunID, DedupKey: it.key, Payload: store.Inline(string(it.raw))}
	created, err = e.st.IngestEvent(e.dbCtx, ev)
	if err != nil {
		return false, nil, nil, fmt.Errorf("ingest item: %w", err)
	}
	if !created {
		return false, nil, nil, nil
	}
	return true, ev, e.fanOut(snap, ev, targets), nil
}

// rerunSeen starts a kind=replay run of an item whose key was already seen
// into each target flow, using the current flow version, and returns the run
// ids. It replays the key's most recent stored event. When that event has been
// pruned, the item is stored as a new event of this (manual) sensor run, which
// is how such an event is marked as manual in the trace; the dedup state does
// not change either way. Nothing is stored when there is no target flow.
func (e *Engine) rerunSeen(snap *snapshot, sensorRunID, sensorID string, it polledItem, targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	ev, err := e.st.GetEventByKey(e.dbCtx, sensorID, it.key)
	if errors.Is(err, store.ErrNotFound) {
		ev = &store.Event{SensorID: sensorID, SensorRunID: sensorRunID, DedupKey: it.key, Payload: store.Inline(string(it.raw))}
		err = e.st.AddEvent(e.dbCtx, ev)
	}
	if err != nil {
		return nil, fmt.Errorf("re-run item: %w", err)
	}
	ids := make([]string, 0, len(targets))
	for _, flowID := range targets {
		fr, err := e.replayRun(e.dbCtx, snap, ev, flowID, false)
		if err != nil {
			return nil, fmt.Errorf("re-run item into flow %q: %w", flowID, err)
		}
		ids = append(ids, fr.ID)
	}
	return ids, nil
}

// fanOut creates one queued live FlowRun per target flow for the event (§8),
// each pinned to the flow version it will run.
func (e *Engine) fanOut(snap *snapshot, ev *store.Event, targets []string) []string {
	var ids []string
	for _, flowID := range targets {
		fd := snap.flows[flowID]
		fr := &store.FlowRun{
			FlowID:   flowID,
			FlowHash: fd.hash,
			FlowYAML: string(fd.raw),
			EventID:  ev.ID,
			Kind:     store.KindLive,
			DryRun:   fd.flow.DryRun,
		}
		if err := e.st.CreateFlowRun(e.dbCtx, fr); err != nil {
			e.log.Error("engine: create flow run", "flow", flowID, "event", ev.ID, "err", err)
			continue
		}
		ids = append(ids, fr.ID)
	}
	e.wake()
	return ids
}

// InjectEvent runs item through the sensor's key template, dedup and fan-out
// exactly like a polled item (origin inject). It is accepted while paused;
// the resulting runs stay queued until the kill switch is released.
func (e *Engine) InjectEvent(_ context.Context, sensorID string, item json.RawMessage) (core.InjectResult, error) {
	snap := e.snapshot()
	sensor := snap.sensors[sensorID]
	if sensor == nil {
		return core.InjectResult{}, fmt.Errorf("%w: sensor %q", store.ErrNotFound, sensorID)
	}
	item = bytes.TrimSpace(item)
	if len(item) == 0 || !json.Valid(item) {
		return core.InjectResult{}, fmt.Errorf("%w: item is not valid JSON", store.ErrInvalid)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, item); err != nil {
		return core.InjectResult{}, fmt.Errorf("%w: item is not valid JSON", store.ErrInvalid)
	}
	key, err := dedupKey(sensor, compact.Bytes())
	if err != nil {
		return core.InjectResult{}, fmt.Errorf("%w: %w", store.ErrInvalid, err)
	}
	start := time.Now().UTC()
	run := &store.SensorRun{
		SensorID: sensorID, Origin: store.OriginInject, Status: store.SensorRunning, StartedAt: start,
		ItemCount: 1, Stdout: store.Inline(snap.red.String(compact.String())),
	}
	if err := e.st.CreateSensorRun(e.dbCtx, run); err != nil {
		return core.InjectResult{}, err
	}
	created, ev, flowRunIDs, err := e.ingest(snap, "", sensorID, polledItem{raw: compact.Bytes(), key: key}, snap.subs[sensorID])
	end := time.Now().UTC()
	run.EndedAt = &end
	d := end.Sub(start).Milliseconds()
	run.DurationMS = &d
	run.Status = store.SensorSucceeded
	if err != nil {
		run.Status, run.Error = store.SensorFailed, err.Error()
	}
	if created {
		run.NewCount = 1
	}
	if uerr := e.st.UpdateSensorRun(e.dbCtx, run); uerr != nil {
		e.log.Warn("engine: record inject run", "run", run.ID, "err", uerr)
	}
	if err != nil {
		return core.InjectResult{}, err
	}
	if !created {
		return core.InjectResult{Duplicate: true, DedupKey: key, Event: e.eventByKey(sensorID, key)}, nil
	}
	return core.InjectResult{Event: ev, DedupKey: key, FlowRunIDs: flowRunIDs}, nil
}

// eventByKey finds the stored event of a dedup key, or nil if it was pruned.
func (e *Engine) eventByKey(sensorID, key string) *store.Event {
	ev, err := e.st.GetEventByKey(e.dbCtx, sensorID, key)
	if err != nil {
		return nil
	}
	return ev
}

// eventPayload loads an event's payload bytes.
func (e *Engine) eventPayload(ev *store.Event) ([]byte, error) {
	b, err := e.st.ReadContent(ev.Payload)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(string(b))), nil
}
