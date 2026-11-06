package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

func TestEngineErrorsMapToProblemTypes(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()
	must(t, e.st.SetKillSwitch(ctx(), store.KillSwitch{Paused: true, Since: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)}))
	var fail error
	e.eng.decide = func(context.Context, string, core.ApprovalDecision) (*store.Approval, error) { return nil, fail }
	path := "/api/approvals/" + f.toolAppr.ID + "/approve"

	for _, tc := range []struct {
		name   string
		err    error
		status int
		typ    string
	}{
		{"unknown reference", fmt.Errorf("sensor ghost: %w", core.ErrUnknownRef), 404, "not-found"},
		{"store not found", fmt.Errorf("approval: %w", store.ErrNotFound), 404, "not-found"},
		{"paused", fmt.Errorf("start: %w", core.ErrPaused), 423, "paused"},
		{"invalid state", fmt.Errorf("approval is decided: %w", core.ErrInvalidState), 409, "invalid-state"},
		{"store conflict", store.ErrConflict, 409, "invalid-state"},
		{"validation", &core.ValidationError{Diagnostics: []spec.Diagnostic{{Severity: spec.SeverityError, Code: "cycle", Message: "a -> b -> a"}}}, 422, "validation-failed"},
		{"anything else", errors.New("database exploded: password=hunter2"), 500, "internal"},
	} {
		fail = tc.err
		r := e.do("POST", path, "{}")
		if r.Status != tc.status || r.problem() != tc.typ {
			t.Errorf("%s: %d %s, want %d %s", tc.name, r.Status, r.Body, tc.status, tc.typ)
		}
		if tc.typ == "internal" && strings.Contains(string(r.Body), "hunter2") {
			t.Errorf("internal errors must not echo their cause: %s", r.Body)
		}
	}
	// Details travel with the problem.
	fail = core.ErrPaused
	if got := e.do("POST", path, "{}").want(423).json()["paused_since"]; got != "2026-11-05T09:00:00Z" {
		t.Errorf("paused_since = %v", got)
	}
	fail = &core.ValidationError{Diagnostics: []spec.Diagnostic{{Severity: spec.SeverityError, Code: "cycle", Message: "loop", Path: "edges[1]"}}}
	if r := e.do("POST", path, "{}").want(422); !contains(diagCodes(r), "cycle") {
		t.Errorf("diagnostics missing: %s", r.Body)
	}
}

func TestApproveAndDenyPassThePrincipalAndArguments(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()
	var mu sync.Mutex
	var gotID string
	var got core.ApprovalDecision
	calls := 0
	e.eng.decide = func(_ context.Context, id string, d core.ApprovalDecision) (*store.Approval, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		gotID, got = id, d
		return e.st.GetApproval(ctx(), id)
	}

	e.do("POST", "/api/approvals/"+f.toolAppr.ID+"/approve", map[string]any{"comment": "go ahead"}).want(200)
	if gotID != f.toolAppr.ID || !got.Approve || got.Comment != "go ahead" || got.CancelRun || got.Principal != "localhost" {
		t.Errorf("approve passed %q %+v", gotID, got)
	}
	e.do("POST", "/api/approvals/"+f.toolAppr.ID+"/approve", nil).want(200) // body is optional
	if got.Comment != "" {
		t.Errorf("empty body kept a comment: %+v", got)
	}

	e.do("POST", "/api/approvals/"+f.toolAppr.ID+"/deny", map[string]any{"comment": "too risky", "cancel_run": true}).want(200)
	if got.Approve || !got.CancelRun || got.Comment != "too risky" || got.Principal != "localhost" {
		t.Errorf("deny passed %+v", got)
	}
	e.do("POST", "/api/approvals/"+f.propAppr.ID+"/deny", map[string]any{"comment": "not now"}).want(200)
	if got.CancelRun {
		t.Error("cancel_run must default to false")
	}

	// cancel_run is meaningless for a proposal: rejected before the engine hears of it.
	before := calls
	r := e.do("POST", "/api/approvals/"+f.propAppr.ID+"/deny", map[string]any{"cancel_run": true}).want(422)
	if r.problem() != "validation-failed" || calls != before {
		t.Errorf("cancel_run on a proposal: %s (engine calls %d -> %d)", r.Body, before, calls)
	}
	e.do("POST", "/api/approvals/01JC7Z3K9Q8V5T2M4N6P8R0S1T/deny", map[string]any{"cancel_run": true}).want(404)
	e.do("POST", "/api/approvals/"+f.toolAppr.ID+"/approve", map[string]any{"comment": "x", "extra": true}).want(400)
}

func TestInjectEvent(t *testing.T) {
	e := newEnv(t)
	var sensor string
	var item json.RawMessage
	e.eng.inject = func(_ context.Context, sensorID string, raw json.RawMessage) (core.InjectResult, error) {
		sensor, item = sensorID, raw
		ev := &store.Event{SensorID: sensorID, DedupKey: "key-1", Payload: store.Inline(string(raw))}
		isNew, err := e.st.IngestEvent(ctx(), ev)
		if err != nil {
			return core.InjectResult{}, err
		}
		if !isNew {
			old, err := e.st.ListEvents(ctx(), store.EventFilter{SensorID: sensorID})
			if err != nil {
				return core.InjectResult{}, err
			}
			return core.InjectResult{Event: &old.Items[0], Duplicate: true, DedupKey: "key-1"}, nil
		}
		fr := e.flowRun("demo", ev.ID, store.KindLive, store.FlowRunQueued)
		return core.InjectResult{Event: ev, DedupKey: "key-1", FlowRunIDs: []string{fr.ID}}, nil
	}

	r := e.do("POST", "/api/events?sensor_id=demo-ticker", `{"id":7,"tags":["a"]}`).want(201)
	var out struct {
		Event struct {
			ID       string
			Injected bool
			Payload  map[string]any
		} `json:"event"`
		Deduplicated bool                      `json:"deduplicated"`
		DedupKey     string                    `json:"dedup_key"`
		FlowRuns     []struct{ Status string } `json:"flow_runs"`
	}
	r.decode(&out)
	if sensor != "demo-ticker" || string(item) != `{"id":7,"tags":["a"]}` {
		t.Errorf("engine got %q %s", sensor, item)
	}
	if out.Deduplicated || out.DedupKey != "key-1" || !out.Event.Injected || out.Event.Payload["id"] != float64(7) || len(out.FlowRuns) != 1 || out.FlowRuns[0].Status != "queued" {
		t.Errorf("result = %+v", out)
	}
	if loc := r.Header.Get("Location"); loc != "/api/events/"+out.Event.ID {
		t.Errorf("Location = %q", loc)
	}

	// The same key again: 200, nothing created, the existing event comes back.
	dup := e.do("POST", "/api/events?sensor_id=demo-ticker", `{"id":7,"tags":["a"]}`).want(200)
	var again struct {
		Event        struct{ ID string } `json:"event"`
		Deduplicated bool                `json:"deduplicated"`
		FlowRuns     []any               `json:"flow_runs"`
	}
	dup.decode(&again)
	if !again.Deduplicated || again.Event.ID != out.Event.ID || len(again.FlowRuns) != 0 {
		t.Errorf("duplicate = %+v", again)
	}

	// Any JSON value is an item.
	for _, body := range []string{`[1,2]`, `"text"`, `42`, `null`} {
		e.do("POST", "/api/events?sensor_id=demo-ticker", body)
		if string(item) != body {
			t.Errorf("item %s reached the engine as %s", body, item)
		}
	}
	for name, tc := range map[string]struct {
		path, body string
		status     int
	}{
		"no sensor":  {"/api/events", `{}`, 400},
		"bad sensor": {"/api/events?sensor_id=a%20b", `{}`, 400},
		"not json":   {"/api/events?sensor_id=demo-ticker", `{oops`, 400},
		"empty body": {"/api/events?sensor_id=demo-ticker", ``, 400},
		"too large":  {"/api/events?sensor_id=demo-ticker", `"` + strings.Repeat("x", 9<<20) + `"`, 413},
	} {
		if r := e.do("POST", tc.path, tc.body); r.Status != tc.status {
			t.Errorf("%s: %d %.200s", name, r.Status, r.Body)
		}
	}
	e.eng.inject = func(context.Context, string, json.RawMessage) (core.InjectResult, error) {
		return core.InjectResult{}, fmt.Errorf("sensor ghost: %w", core.ErrUnknownRef)
	}
	e.do("POST", "/api/events?sensor_id=ghost", `{}`).want(404)
}

func TestReplayAndPoll(t *testing.T) {
	e := newEnv(t)
	e.writeHome("flows/dry.yaml", "id: dry\nenabled: true\ndry_run: true\nsubscribe: [demo-ticker]\nnodes:\n  - {id: a, type: command, lang: bash, script: ls}\n")
	e.ws.Store(spec.Load(e.home))
	ev := e.event("demo-ticker", "k", `{}`)

	var gotEvent, gotFlow string
	var gotDry bool
	e.eng.replay = func(_ context.Context, eventID, flowID string, dryRun bool) (*store.FlowRun, error) {
		gotEvent, gotFlow, gotDry = eventID, flowID, dryRun
		return e.flowRun(flowID, eventID, store.KindReplay, store.FlowRunQueued), nil
	}
	r := e.do("POST", "/api/events/"+ev.ID+"/replay", map[string]any{"flow": "dry"}).want(202)
	if gotEvent != ev.ID || gotFlow != "dry" || !gotDry {
		t.Errorf("absent dry_run must keep the flow's own setting: %q %q %v", gotEvent, gotFlow, gotDry)
	}
	var run struct {
		ID, Kind string
	}
	r.decode(&run)
	if run.Kind != "replay" || r.Header.Get("Location") != "/api/flow-runs/"+run.ID {
		t.Errorf("replay response = %s, Location %q", r.Body, r.Header.Get("Location"))
	}
	e.do("POST", "/api/events/"+ev.ID+"/replay", map[string]any{"flow": "dry", "dry_run": false}).want(202)
	if gotDry {
		t.Error("explicit dry_run=false must override the flow")
	}
	e.do("POST", "/api/events/"+ev.ID+"/replay", map[string]any{"flow": "demo-flow-that-is-missing"}).want(404)
	e.do("POST", "/api/events/01JC7Z3K9Q8V5T2M4N6P8R0S1T/replay", map[string]any{"flow": "dry"}).want(404)
	e.do("POST", "/api/events/"+ev.ID+"/replay", map[string]any{"flow": "not a slug"}).want(422)
	e.do("POST", "/api/events/"+ev.ID+"/replay", map[string]any{}).want(422)
	e.do("POST", "/api/events/"+ev.ID+"/replay", map[string]any{"flow": "dry", "x": 1}).want(400)

	// Poll now.
	var gotPoll core.PollOptions
	e.eng.poll = func(_ context.Context, id string, opts core.PollOptions) (core.PollResult, error) {
		gotPoll = opts
		run := &store.SensorRun{SensorID: id, Origin: store.OriginManual}
		return core.PollResult{Run: run, NewEvents: 2, RerunEvents: 1, FlowRunIDs: []string{"01JC7Z3K9Q8V5T2M4N6P8R0S1A", "01JC7Z3K9Q8V5T2M4N6P8R0S1B", "01JC7Z3K9Q8V5T2M4N6P8R0S1C"}}, e.st.CreateSensorRun(ctx(), run)
	}
	r = e.do("POST", "/api/sensors/demo-ticker/poll", nil).want(202)
	var sr struct {
		ID, Trigger, Status string
		NewEvents           int      `json:"new_events"`
		RerunEvents         int      `json:"rerun_events"`
		FlowRunIDs          []string `json:"flow_run_ids"`
	}
	r.decode(&sr)
	if sr.Trigger != "manual" || sr.Status != "running" || r.Header.Get("Location") != "/api/sensor-runs/"+sr.ID {
		t.Errorf("poll = %s", r.Body)
	}
	if sr.NewEvents != 2 || sr.RerunEvents != 1 || len(sr.FlowRunIDs) != 3 || gotPoll.RerunSeen || len(gotPoll.Flows) != 0 {
		t.Errorf("an empty body must keep today's behavior and report the counts: %s, opts %+v", r.Body, gotPoll)
	}
	e.do("POST", "/api/sensors/demo-ticker/poll", map[string]any{"rerun_seen": true, "flows": []string{"dry"}}).want(202)
	if !gotPoll.RerunSeen || len(gotPoll.Flows) != 1 || gotPoll.Flows[0] != "dry" {
		t.Errorf("options = %+v", gotPoll)
	}
	e.do("POST", "/api/sensors/demo-ticker/poll", map[string]any{"flows": []string{"not a slug"}}).want(422)
	e.do("POST", "/api/sensors/demo-ticker/poll", map[string]any{"rerun": true}).want(400)
	e.do("POST", "/api/sensors/ghost/poll", nil).want(404)
	e.eng.poll = func(context.Context, string, core.PollOptions) (core.PollResult, error) {
		return core.PollResult{}, fmt.Errorf("a poll of this sensor is running: %w", core.ErrInvalidState)
	}
	e.do("POST", "/api/sensors/demo-ticker/poll", nil).want(409)
	e.eng.poll = func(context.Context, string, core.PollOptions) (core.PollResult, error) {
		return core.PollResult{}, core.ErrPaused
	}
	e.do("POST", "/api/sensors/demo-ticker/poll", nil).want(423)
}

func TestRunFlowAndResetSensor(t *testing.T) {
	e := newEnv(t)
	e.eng.runFlow = func(_ context.Context, id string, opts core.RunFlowOptions) (core.RunFlowResult, error) {
		if opts.RerunSeen {
			return core.RunFlowResult{}, &core.ValidationError{Diagnostics: []spec.Diagnostic{{Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: "subscribe", Message: "subscribes to nothing"}}}
		}
		run := &store.SensorRun{SensorID: "demo-ticker", Origin: store.OriginManual}
		return core.RunFlowResult{FlowID: id, FlowRunIDs: []string{"01JC7Z3K9Q8V5T2M4N6P8R0S1X"}, Sensors: []core.FlowSensorResult{
			{SensorID: "demo-ticker", Poll: &core.PollResult{Run: run, NewEvents: 1, FlowRunIDs: []string{"01JC7Z3K9Q8V5T2M4N6P8R0S1X"}}},
			{SensorID: "gone", Error: "no such sensor"},
		}}, e.st.CreateSensorRun(ctx(), run)
	}
	r := e.do("POST", "/api/flows/demo/run", nil).want(202)
	var out struct {
		FlowID  string `json:"flow_id"`
		Sensors []struct {
			SensorID   string               `json:"sensor_id"`
			Run        *struct{ ID string } `json:"run"`
			FlowRunIDs []string             `json:"flow_run_ids"`
			Error      *string              `json:"error"`
		}
		FlowRunIDs []string `json:"flow_run_ids"`
	}
	r.decode(&out)
	if out.FlowID != "demo" || len(out.Sensors) != 2 || out.Sensors[0].Run == nil || out.Sensors[0].Error != nil ||
		out.Sensors[1].Run != nil || out.Sensors[1].Error == nil || len(out.Sensors[1].FlowRunIDs) != 0 || len(out.FlowRunIDs) != 1 {
		t.Errorf("run flow = %s", r.Body)
	}
	e.do("POST", "/api/flows/demo/run", map[string]any{"rerun_seen": true}).want(422)
	e.do("POST", "/api/flows/ghost/run", nil).want(404)
	e.eng.runFlow = func(context.Context, string, core.RunFlowOptions) (core.RunFlowResult, error) {
		return core.RunFlowResult{}, core.ErrPaused
	}
	e.do("POST", "/api/flows/demo/run", nil).want(423)

	var gotReset core.ResetOptions
	e.eng.resetSensor = func(_ context.Context, id, principal string, opts core.ResetOptions) (core.ResetResult, error) {
		gotReset = opts
		now := time.Now()
		return core.ResetResult{State: store.SensorState{SensorID: id, ResetAt: &now, ResetBy: principal}, KeysCleared: 4}, nil
	}
	r = e.do("POST", "/api/sensors/demo-ticker/reset", nil).want(200)
	var rs struct {
		KeysCleared int64     `json:"keys_cleared"`
		ResetBy     *string   `json:"reset_by"`
		Run         *struct{} `json:"run"`
	}
	r.decode(&rs)
	if rs.KeysCleared != 4 || rs.ResetBy == nil || rs.Run != nil || gotReset.Run {
		t.Errorf("reset = %s, opts %+v", r.Body, gotReset)
	}
	e.do("POST", "/api/sensors/demo-ticker/reset", map[string]any{"run": true, "rerun_flows": []string{"demo"}}).want(200)
	if !gotReset.Run || len(gotReset.Flows) != 1 {
		t.Errorf("reset options = %+v", gotReset)
	}
	e.do("POST", "/api/sensors/demo-ticker/reset", map[string]any{"rerun_flows": []string{"demo"}}).want(422)
	e.do("POST", "/api/sensors/ghost/reset", nil).want(404)
	e.eng.resetSensor = func(context.Context, string, string, core.ResetOptions) (core.ResetResult, error) {
		return core.ResetResult{}, core.ErrInvalidState
	}
	e.do("POST", "/api/sensors/demo-ticker/reset", nil).want(409)
}

func TestTestNode(t *testing.T) {
	e := newEnv(t)
	e.writeHome("flows/probe.yaml", demoFlowYAML)
	e.writeHome("flows/probe.yaml", strings.Replace(demoFlowYAML, "id: demo", "id: probe", 1))
	e.ws.Store(spec.Load(e.home))
	ev := e.event("demo-ticker", "k", `{"title":"hello"}`)
	upstreamFlow := e.flowRun("probe", ev.ID, store.KindLive, store.FlowRunSucceeded)
	upstream := e.nodeRun(upstreamFlow.ID, "filter", store.NodeDone, func(n *store.NodeRun) {
		n.Stdout = store.Inline("{\"n\":0}\n{\"n\":1}\n")
	})

	var req core.TestNodeRequest
	release := make(chan struct{})
	var started sync.WaitGroup
	e.eng.testNode = func(_ context.Context, r core.TestNodeRequest) (*store.FlowRun, *store.NodeRun, error) {
		req = r
		fr := e.flowRun(r.FlowID, r.EventID, store.KindTest, store.FlowRunRunning)
		nr := e.nodeRun(fr.ID, r.NodeID, store.NodeRunning, nil)
		started.Done()
		<-release
		fr.Status, nr.Status, nr.Decision = store.FlowRunSucceeded, store.NodeDone, store.DecisionEmitted
		nr.Stdout = store.Inline(`{"ok":true}`)
		if err := e.st.UpdateNodeRun(ctx(), nr); err != nil {
			return nil, nil, err
		}
		return fr, nr, e.st.UpdateFlowRun(ctx(), fr)
	}
	run := func(body any, query string) *resp {
		started.Add(1)
		return e.do("POST", "/api/flows/probe/test-node"+query, body)
	}
	close(release) // finish immediately unless a test re-arms it

	// Event input.
	r := run(map[string]any{"node_id": "filter", "input": map[string]any{"source": "event", "event_id": ev.ID}, "dry_run": true}, "").want(200)
	if req.FlowID != "probe" || req.NodeID != "filter" || req.Flow != nil || req.EventID != ev.ID || !req.DryRun || string(req.Input) != `{"title":"hello"}` {
		t.Errorf("event input: %+v", req)
	}
	var res struct {
		FlowRun struct{ Kind, Status string } `json:"flow_run"`
		NodeRun struct {
			Status string
			Stdout struct{ Text string }
		} `json:"node_run"`
	}
	r.decode(&res)
	if res.FlowRun.Kind != "test" || res.FlowRun.Status != "succeeded" || res.NodeRun.Stdout.Text != `{"ok":true}` {
		t.Errorf("result = %s", r.Body)
	}

	// An earlier node run's output, picked by index, and pasted JSON.
	run(map[string]any{"node_id": "filter", "input": map[string]any{"source": "node_run", "node_run_id": upstream.ID, "output_index": 1}}, "").want(200)
	if string(req.Input) != `{"n":1}` || req.EventID != ev.ID {
		t.Errorf("node_run input: %s event %q", req.Input, req.EventID)
	}
	run(map[string]any{"node_id": "filter", "input": map[string]any{"source": "json", "json": []int{1, 2}}}, "").want(200)
	if string(req.Input) != `[1,2]` || req.EventID != "" {
		t.Errorf("json input: %s", req.Input)
	}

	// An unsaved draft replaces the saved node in the flow the engine runs.
	draft := map[string]any{"id": "filter", "type": "command", "lang": "bash", "script": "echo draft"}
	run(map[string]any{"node_id": "filter", "node": draft, "input": map[string]any{"source": "json", "json": 1}}, "").want(200)
	if req.Flow == nil || len(req.Flow.Nodes) != 3 || req.Flow.Nodes[0].Script != "echo draft" || req.Flow.Nodes[0].Lang != "bash" {
		t.Errorf("draft flow = %+v", req.Flow)
	}
	if strings.Contains(e.readHome("flows/probe.yaml"), "echo draft") {
		t.Error("a draft must not be saved")
	}

	// Rejected before the engine runs.
	for name, tc := range map[string]struct {
		body   any
		status int
	}{
		"unknown node":     {map[string]any{"node_id": "ghost", "input": map[string]any{"source": "json", "json": 1}}, 404},
		"draft id differs": {map[string]any{"node_id": "filter", "node": map[string]any{"id": "other", "type": "inbox", "title": "t"}, "input": map[string]any{"source": "json", "json": 1}}, 422},
		"bad index":        {map[string]any{"node_id": "filter", "input": map[string]any{"source": "node_run", "node_run_id": upstream.ID, "output_index": 5}}, 422},
		"unknown event":    {map[string]any{"node_id": "filter", "input": map[string]any{"source": "event", "event_id": "01JC7Z3K9Q8V5T2M4N6P8R0S1T"}}, 404},
		"no source":        {map[string]any{"node_id": "filter", "input": map[string]any{}}, 400},
		"json missing":     {map[string]any{"node_id": "filter", "input": map[string]any{"source": "json"}}, 400},
		"no input":         {map[string]any{"node_id": "filter"}, 400},
	} {
		started.Add(1)
		started.Done()
		if r := e.do("POST", "/api/flows/probe/test-node", tc.body); r.Status != tc.status {
			t.Errorf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if r := e.do("POST", "/api/flows/ghost/test-node", map[string]any{"node_id": "a", "input": map[string]any{"source": "json", "json": 1}}); r.Status != 404 {
		t.Errorf("unknown flow: %d", r.Status)
	}
	e.eng.testNode = func(context.Context, core.TestNodeRequest) (*store.FlowRun, *store.NodeRun, error) {
		return nil, nil, core.ErrPaused
	}
	e.do("POST", "/api/flows/probe/test-node", map[string]any{"node_id": "filter", "input": map[string]any{"source": "json", "json": 1}}).want(423)
}

func TestTestNodeStillRunningReturns202WithTheRecordedRun(t *testing.T) {
	e := newEnv(t)
	e.writeHome("flows/probe.yaml", strings.Replace(demoFlowYAML, "id: demo", "id: probe", 1))
	e.ws.Store(spec.Load(e.home))
	release := make(chan struct{})
	done := make(chan struct{})
	e.eng.testNode = func(_ context.Context, r core.TestNodeRequest) (*store.FlowRun, *store.NodeRun, error) {
		defer close(done)
		fr := e.flowRun(r.FlowID, "", store.KindTest, store.FlowRunRunning)
		nr := e.nodeRun(fr.ID, r.NodeID, store.NodeRunning, nil)
		<-release
		nr.Status, nr.Decision = store.NodeDone, store.DecisionEmitted
		return fr, nr, e.st.UpdateNodeRun(ctx(), nr)
	}
	body := map[string]any{"node_id": "filter", "input": map[string]any{"source": "json", "json": 1}}
	start := time.Now()
	r := e.do("POST", "/api/flows/probe/test-node?wait=200ms", body).want(202)
	if d := time.Since(start); d < 150*time.Millisecond || d > 5*time.Second {
		t.Errorf("answered after %v", d)
	}
	var res struct {
		FlowRun struct{ ID, Kind, Status string } `json:"flow_run"`
		NodeRun struct {
			ID, Status string
			NodeID     string `json:"node_id"`
		} `json:"node_run"`
	}
	r.decode(&res)
	if res.FlowRun.Kind != "test" || res.FlowRun.Status != "running" || res.NodeRun.Status != "running" || res.NodeRun.NodeID != "filter" {
		t.Errorf("202 body = %s", r.Body)
	}
	// The run was not cancelled when the request ended.
	select {
	case <-done:
		t.Fatal("the engine call was cancelled with the response")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
	e.get("/api/node-runs/" + res.NodeRun.ID + "?wait=5s").want(200)
}

func TestPauseAndResumeKeepTheReasonAndPassArguments(t *testing.T) {
	e := newEnv(t)
	var gotKill bool
	var gotPrincipal string
	e.eng.pause = func(_ context.Context, kill bool, principal string) (store.KillSwitch, error) {
		gotKill, gotPrincipal = kill, principal
		ks := store.KillSwitch{Paused: true, KillRunning: kill, By: principal, Since: time.Now()}
		return ks, e.st.SetKillSwitch(ctx(), ks)
	}
	e.eng.resume = func(_ context.Context, principal string) (store.KillSwitch, error) {
		gotPrincipal = principal
		return store.KillSwitch{}, e.st.SetKillSwitch(ctx(), store.KillSwitch{})
	}

	r := e.do("POST", "/api/killswitch/pause", map[string]any{"kill_running": true, "reason": "deploying"}).want(200)
	got := r.json()
	if !gotKill || gotPrincipal != "localhost" || got["paused"] != true || got["kill_running"] != true || got["by"] != "localhost" || got["reason"] != "deploying" {
		t.Errorf("pause: kill=%v principal=%q body=%s", gotKill, gotPrincipal, r.Body)
	}
	if ks := e.get("/api/killswitch").want(200).json(); ks["reason"] != "deploying" {
		t.Errorf("GET killswitch = %v", ks)
	}
	// Pausing without a body is allowed and forgets the old reason.
	if got := e.do("POST", "/api/killswitch/pause", nil).want(200).json(); gotKill || got["reason"] != nil || got["kill_running"] != false {
		t.Errorf("bare pause: kill=%v body=%v", gotKill, got)
	}
	e.do("POST", "/api/killswitch/pause", map[string]any{"reason": "again"}).want(200)
	r = e.do("POST", "/api/killswitch/resume", nil).want(200)
	if got := r.json(); got["paused"] != false || got["reason"] != nil || got["since"] != nil {
		t.Errorf("resume = %s", r.Body)
	}
	if ks := e.get("/api/killswitch").want(200).json(); ks["paused"] != false || ks["reason"] != nil {
		t.Errorf("after resume = %v", ks)
	}
	e.do("POST", "/api/killswitch/pause", map[string]any{"kill_running": "yes"}).want(400)
}

func TestRuntimesAndProbe(t *testing.T) {
	e := newEnv(t)
	e.writeHome("config.yaml", configWithSecrets)
	e.ws.Store(spec.Load(e.home))

	var list struct {
		Items []struct {
			Name      string            `json:"name"`
			Command   []string          `json:"command"`
			Modes     map[string]string `json:"modes"`
			EnvKeys   []string          `json:"env_keys"`
			LastProbe any               `json:"last_probe"`
		} `json:"items"`
	}
	r := e.get("/api/runtimes").want(200)
	r.decode(&list)
	if len(list.Items) != 1 || list.Items[0].Name != "claude" || list.Items[0].Modes["ask"] != "default" || len(list.Items[0].EnvKeys) != 1 ||
		list.Items[0].EnvKeys[0] != "ANTHROPIC_API_KEY" || list.Items[0].LastProbe != nil {
		t.Errorf("runtimes = %s", r.Body)
	}
	if strings.Contains(string(r.Body), "sk-live-runtime-secret") {
		t.Error("runtime env values leaked")
	}

	var probed string
	e.eng.probe = func(_ context.Context, name string) (core.ProbeResult, error) {
		probed = name
		return core.ProbeResult{
			OK: true, AgentInfo: json.RawMessage(`{"name":"claude-agent-acp","version":"1"}`),
			Capabilities: json.RawMessage(`{"loadSession":true}`), Modes: []string{"default", "plan"}, Duration: 1500 * time.Millisecond,
		}, nil
	}
	got := e.do("POST", "/api/runtimes/claude/probe", nil).want(200).json()
	if probed != "claude" || got["ok"] != true || got["load_session"] != true || got["duration_ms"] != float64(1500) ||
		got["ask_mode_available"] != true || got["full_mode_available"] != false {
		t.Errorf("probe = %v", got)
	}
	e.get("/api/runtimes").want(200).decode(&list)
	if list.Items[0].LastProbe == nil {
		t.Error("the last probe is not remembered")
	}

	// A failing runtime is still a 200 with the reason.
	e.eng.probe = func(context.Context, string) (core.ProbeResult, error) {
		return core.ProbeResult{OK: false, Error: "spawn claude-agent-acp: not found"}, nil
	}
	if got := e.do("POST", "/api/runtimes/claude/probe", nil).want(200).json(); got["ok"] != false || got["error"] != "spawn claude-agent-acp: not found" {
		t.Errorf("failed probe = %v", got)
	}
	probed = ""
	e.do("POST", "/api/runtimes/ghost/probe", nil).want(404)
	if probed != "" {
		t.Error("an unknown runtime reached the engine")
	}
}

func TestBodylessWritesNeedNoBodyAndBadJSONIsRejected(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{`{`, `[]`, `"x"`, `{"kill_running": true} {"x":1}`} {
		req := request(http.MethodPost, "/api/killswitch/pause", []byte(body))
		if r := serve(t, e.srv, req); r.Status != 400 {
			t.Errorf("body %q -> %d %s", body, r.Status, r.Body)
		}
	}
	_ = bytes.MinRead
}
