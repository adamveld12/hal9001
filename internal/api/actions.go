package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Handlers in this file execute or change run state: they validate the
// request, pass it to core.Engine together with the authenticated principal,
// and map the engine's sentinel errors to problem types (see writeError).

// ---- events ----

type injectResultView struct {
	Event        eventView          `json:"event"`
	Deduplicated bool               `json:"deduplicated"`
	DedupKey     string             `json:"dedup_key"`
	FlowRuns     []flowRunBriefView `json:"flow_runs"`
}

func (s *Server) injectEvent(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	sensorID := q.slug("sensor_id")
	if sensorID == "" && !q.v.Has("sensor_id") {
		q.fail("sensor_id is required")
	}
	if !q.ok(w) {
		return
	}
	body, ok := readBody(w, r, injectLimit)
	if !ok {
		return
	}
	if !json.Valid(body) {
		badRequest(w, "the request body must be one JSON value (the item)")
		return
	}
	res, err := s.opts.Engine.InjectEvent(r.Context(), sensorID, json.RawMessage(body))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := injectResultView{Deduplicated: res.Duplicate, DedupKey: res.DedupKey, FlowRuns: []flowRunBriefView{}}
	if res.Event != nil {
		out.Event = s.eventView(res.Event)
		if out.DedupKey == "" {
			out.DedupKey = res.Event.DedupKey
		}
	} else {
		// A duplicate whose event was pruned: only the dedup key survived.
		out.Event = eventView{
			ID: "00000000000000000000000000", SensorID: sensorID, DedupKey: out.DedupKey, Payload: body,
			CreatedAt: time.Unix(0, 0).UTC(), Injected: true,
		}
	}
	for _, id := range res.FlowRunIDs {
		fr, err := s.opts.Store.GetFlowRun(r.Context(), id)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.FlowRuns = append(out.FlowRuns, flowRunBrief(*fr))
	}
	if res.Duplicate {
		writeJSON(w, http.StatusOK, out)
		return
	}
	w.Header().Set("Location", "/api/events/"+out.Event.ID)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) replayEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	var req struct {
		Flow   string `json:"flow"`
		DryRun *bool  `json:"dry_run"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if !spec.ValidID(req.Flow) {
		fieldProblem(w, "flow", "flow must be a flow id")
		return
	}
	if _, err := s.opts.Store.GetEvent(r.Context(), eventID); err != nil {
		s.writeError(w, r, err)
		return
	}
	// The engine takes a plain bool; "absent" means the flow's own setting.
	dry := false
	if req.DryRun != nil {
		dry = *req.DryRun
	} else {
		f := s.opts.Workspace().Flow(req.Flow)
		if f == nil {
			notFound(w, "no flow %q", req.Flow)
			return
		}
		if f.Value != nil {
			dry = f.Value.DryRun
		}
	}
	fr, err := s.opts.Engine.Replay(r.Context(), eventID, req.Flow, dry)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/flow-runs/"+fr.ID)
	writeJSON(w, http.StatusAccepted, flowRunFull(*fr))
}

// ---- sensors ----

// pollRequest is the optional body of POST /api/sensors/{id}/poll.
type pollRequest struct {
	RerunSeen bool     `json:"rerun_seen"`
	Flows     []string `json:"flows"`
}

// pollResultView is the SensorRun of a poll plus what the poll started.
type pollResultView struct {
	sensorRunView
	NewEvents   int      `json:"new_events"`
	RerunEvents int      `json:"rerun_events"`
	FlowRunIDs  []string `json:"flow_run_ids"`
}

func (s *Server) pollResultView(res core.PollResult) pollResultView {
	return pollResultView{
		sensorRunView: s.sensorRunView(*res.Run, false),
		NewEvents:     res.NewEvents, RerunEvents: res.RerunEvents, FlowRunIDs: nonNilStrings(res.FlowRunIDs),
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// checkFlowIDs answers 422 unless every id can be a flow id.
func checkFlowIDs(w http.ResponseWriter, ids []string) bool {
	for _, id := range ids {
		if !spec.ValidID(id) {
			fieldProblem(w, "flows", "flows must be flow ids")
			return false
		}
	}
	return true
}

func (s *Server) pollSensor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.opts.Workspace().Sensor(id) == nil {
		notFound(w, "no sensor %q", id)
		return
	}
	var req pollRequest
	if !decodeJSON(w, r, &req, true) || !checkFlowIDs(w, req.Flows) {
		return
	}
	res, err := s.opts.Engine.PollSensor(r.Context(), id, core.PollOptions{RerunSeen: req.RerunSeen, Flows: req.Flows})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/sensor-runs/"+res.Run.ID)
	writeJSON(w, http.StatusAccepted, s.pollResultView(res))
}

// resetResultView answers POST /api/sensors/{id}/reset. Run has the shape of a
// poll result when the reset ran the sensor, else null.
type resetResultView struct {
	SensorID    string          `json:"sensor_id"`
	ResetAt     *time.Time      `json:"reset_at"`
	ResetBy     *string         `json:"reset_by"`
	KeysCleared int64           `json:"keys_cleared"`
	Run         *pollResultView `json:"run"`
}

func (s *Server) resetSensor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.sensorExists(w, id) {
		return
	}
	var req struct {
		Run        bool     `json:"run"`
		RerunFlows []string `json:"rerun_flows"`
	}
	if !decodeJSON(w, r, &req, true) || !checkFlowIDs(w, req.RerunFlows) {
		return
	}
	if len(req.RerunFlows) > 0 && !req.Run {
		fieldProblem(w, "rerun_flows", "rerun_flows only applies together with run: true")
		return
	}
	res, err := s.opts.Engine.ResetSensor(r.Context(), id, principal(r), core.ResetOptions{Run: req.Run, Flows: req.RerunFlows})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := resetResultView{
		SensorID: id, ResetAt: utcPtr(res.State.ResetAt), ResetBy: &res.State.ResetBy, KeysCleared: res.KeysCleared,
	}
	if res.Poll != nil {
		v := s.pollResultView(*res.Poll)
		out.Run = &v
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- flows ----

type runFlowSensorView struct {
	SensorID    string         `json:"sensor_id"`
	Run         *sensorRunView `json:"run"`
	NewEvents   int            `json:"new_events"`
	RerunEvents int            `json:"rerun_events"`
	FlowRunIDs  []string       `json:"flow_run_ids"`
	Error       *string        `json:"error"`
}

type runFlowResultView struct {
	FlowID     string              `json:"flow_id"`
	Sensors    []runFlowSensorView `json:"sensors"`
	FlowRunIDs []string            `json:"flow_run_ids"`
}

func (s *Server) runFlow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f := s.opts.Workspace().Flow(id)
	if f == nil {
		notFound(w, "no flow %q", id)
		return
	}
	if !f.Valid() {
		conflictProblem(w, typeInvalidState, "the flow has validation errors; fix it before running it", nil)
		return
	}
	var req struct {
		RerunSeen bool `json:"rerun_seen"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	res, err := s.opts.Engine.RunFlow(r.Context(), id, core.RunFlowOptions{RerunSeen: req.RerunSeen})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := runFlowResultView{FlowID: res.FlowID, Sensors: make([]runFlowSensorView, 0, len(res.Sensors)), FlowRunIDs: nonNilStrings(res.FlowRunIDs)}
	for _, sr := range res.Sensors {
		v := runFlowSensorView{SensorID: sr.SensorID, FlowRunIDs: []string{}, Error: nonEmpty(sr.Error)}
		if sr.Poll != nil {
			run := s.sensorRunView(*sr.Poll.Run, false)
			v.Run, v.NewEvents, v.RerunEvents, v.FlowRunIDs = &run, sr.Poll.NewEvents, sr.Poll.RerunEvents, nonNilStrings(sr.Poll.FlowRunIDs)
		}
		out.Sensors = append(out.Sensors, v)
	}
	writeJSON(w, http.StatusAccepted, out)
}

// pauseSensor and resumeSensor are idempotent and answer with the updated
// SensorSummary. A paused sensor is not polled on schedule; Poll now and event
// injection still work.
func (s *Server) pauseSensor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.sensorExists(w, id) {
		return
	}
	if _, err := s.opts.Engine.PauseSensor(r.Context(), id, principal(r)); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeSensorSummary(w, r, id)
}

func (s *Server) resumeSensor(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.sensorExists(w, id) {
		return
	}
	if _, err := s.opts.Engine.ResumeSensor(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeSensorSummary(w, r, id)
}

func (s *Server) sensorExists(w http.ResponseWriter, id string) bool {
	if s.opts.Workspace().Sensor(id) == nil {
		notFound(w, "no sensor %q", id)
		return false
	}
	return true
}

func (s *Server) writeSensorSummary(w http.ResponseWriter, r *http.Request, id string) {
	ks, err := s.opts.Store.GetKillSwitch(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f := s.opts.Workspace().Sensor(id)
	if f == nil {
		notFound(w, "no sensor %q", id)
		return
	}
	v, err := s.sensorSummary(r.Context(), f, ks.Paused)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- test node ----

const defaultTestWait = 30 * time.Second

type testNodeInput struct {
	Source      string          `json:"source"`
	EventID     string          `json:"event_id"`
	NodeRunID   string          `json:"node_run_id"`
	OutputIndex *int            `json:"output_index"`
	JSON        json.RawMessage `json:"json"`
}

// resolveTestInput turns the request's input union into the stdin payload and
// the event it relates to. It answers any failure itself.
func (s *Server) resolveTestInput(w http.ResponseWriter, r *http.Request, in testNodeInput) (payload json.RawMessage, eventID string, ok bool) {
	st := s.opts.Store
	switch in.Source {
	case "event":
		if !ulidRE.MatchString(in.EventID) {
			badRequest(w, "input.event_id must be an event id")
			return nil, "", false
		}
		ev, err := st.GetEvent(r.Context(), in.EventID)
		if err != nil {
			s.writeError(w, r, err)
			return nil, "", false
		}
		return s.contentJSON(ev.Payload), ev.ID, true
	case "node_run":
		if !ulidRE.MatchString(in.NodeRunID) {
			badRequest(w, "input.node_run_id must be a node run id")
			return nil, "", false
		}
		nr, err := st.GetNodeRun(r.Context(), in.NodeRunID)
		if err != nil {
			s.writeError(w, r, err)
			return nil, "", false
		}
		out, err := st.ReadContent(nr.Stdout)
		if err != nil {
			s.writeError(w, r, err)
			return nil, "", false
		}
		values := splitJSONValues(out)
		idx := 0
		if in.OutputIndex != nil {
			idx = *in.OutputIndex
		}
		if idx < 0 || idx >= len(values) {
			fieldProblem(w, "input.output_index", "that node run has no output at this index")
			return nil, "", false
		}
		if fr, err := st.GetFlowRun(r.Context(), nr.FlowRunID); err == nil {
			eventID = fr.EventID
		}
		return values[idx], eventID, true
	case "json":
		if in.JSON == nil {
			badRequest(w, "input.json is required when input.source is json")
			return nil, "", false
		}
		return in.JSON, "", true
	}
	badRequest(w, "input.source must be one of event, node_run, json")
	return nil, "", false
}

// splitJSONValues parses a node's stdout: one JSON value or JSONL.
func splitJSONValues(out []byte) []json.RawMessage {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil
	}
	if json.Valid(out) {
		return []json.RawMessage{out}
	}
	var values []json.RawMessage
	for _, line := range bytes.Split(out, []byte("\n")) {
		if line = bytes.TrimSpace(line); len(line) > 0 && json.Valid(line) {
			values = append(values, slices.Clone(line))
		}
	}
	return values
}

type testNodeResultView struct {
	FlowRun flowRunView `json:"flow_run"`
	NodeRun nodeRunView `json:"node_run"`
}

func (s *Server) testFlowNode(w http.ResponseWriter, r *http.Request) {
	flowID := r.PathValue("id")
	q := newQuery(r)
	wait := q.wait(defaultTestWait)
	if !q.ok(w) {
		return
	}
	var req struct {
		NodeID           string              `json:"node_id"`
		Node             *spec.Node          `json:"node"`
		Input            testNodeInput       `json:"input"`
		DryRun           bool                `json:"dry_run"`
		SimulateApproval string              `json:"simulate_approval"`
		SimulateEmit     *core.SimulatedEmit `json:"simulate_emit"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if !spec.ValidID(req.NodeID) {
		fieldProblem(w, "node_id", "node_id must be a node id")
		return
	}
	raw, _, err := s.readFile(spec.KindFlow, flowID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	saved, ds := spec.ParseFlow(flowID, raw)
	if saved == nil {
		validationProblem(w, "the flow does not parse", ds)
		return
	}
	if req.SimulateApproval != "" && req.SimulateApproval != "approved" && req.SimulateApproval != "denied" {
		fieldProblem(w, "simulate_approval", "simulate_approval must be approved or denied")
		return
	}
	if req.SimulateEmit != nil && !checkSimulatedEmit(w, "simulate_emit", req.SimulateEmit) {
		return
	}
	engineReq := core.TestNodeRequest{FlowID: flowID, NodeID: req.NodeID, DryRun: req.DryRun, SimulateApproval: req.SimulateApproval, SimulateEmit: req.SimulateEmit}
	if req.Node != nil {
		if req.Node.ID != req.NodeID {
			fieldProblem(w, "node.id", "node.id must equal node_id")
			return
		}
		draft := saved.Clone()
		if i := slices.IndexFunc(draft.Nodes, func(n spec.Node) bool { return n.ID == req.NodeID }); i >= 0 {
			draft.Nodes[i] = *req.Node
		} else {
			draft.Nodes = append(draft.Nodes, *req.Node)
		}
		draft.ApplyDefaults()
		engineReq.Flow = &draft
	} else if saved.NodeByID(req.NodeID) == nil {
		notFound(w, "flow %q has no node %q", flowID, req.NodeID)
		return
	}
	payload, eventID, ok := s.resolveTestInput(w, r, req.Input)
	if !ok {
		return
	}
	engineReq.Input, engineReq.EventID = payload, eventID

	st := s.opts.Store
	newest, err := st.ListFlowRuns(r.Context(), store.FlowRunFilter{FlowID: flowID, Kinds: []store.FlowRunKind{store.KindTest}, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	after := ""
	if len(newest.Items) > 0 {
		after = newest.Items[0].ID
	}

	type outcome struct {
		fr  *store.FlowRun
		nr  *store.NodeRun
		err error
	}
	done := make(chan outcome, 1)
	// The run continues after this request ends or `wait` elapses.
	go func() {
		fr, nr, err := s.opts.Engine.TestNode(context.WithoutCancel(r.Context()), engineReq)
		done <- outcome{fr, nr, err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var res outcome
	status := http.StatusOK
	select {
	case res = <-done:
		if res.err != nil {
			s.writeError(w, r, res.err)
			return
		}
	case <-timer.C:
		status = http.StatusAccepted
	case <-r.Context().Done():
		return
	}

	var fr *store.FlowRun
	var nr *store.NodeRun
	if res.fr != nil && res.nr != nil {
		fr, err = st.GetFlowRun(r.Context(), res.fr.ID)
		if err == nil {
			nr, err = st.GetNodeRun(r.Context(), res.nr.ID)
		}
	} else {
		fr, nr, err = s.findTestRun(r.Context(), flowID, req.NodeID, after)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	writeJSON(w, status, testNodeResultView{FlowRun: flowRunFull(*fr), NodeRun: rv.nodeRunView(*nr, false)})
}

// findTestRun locates the test run the engine started for a request that is
// still running: the oldest test run of the flow created after the newest one
// that existed before the request. It waits briefly for the engine to create it.
func (s *Server) findTestRun(ctx context.Context, flowID, nodeID, after string) (*store.FlowRun, *store.NodeRun, error) {
	st := s.opts.Store
	var fr *store.FlowRun
	var nr *store.NodeRun
	find := func() (bool, error) {
		runs, err := st.ListFlowRuns(ctx, store.FlowRunFilter{
			FlowID: flowID, Kinds: []store.FlowRunKind{store.KindTest}, ListOpts: store.ListOpts{Cursor: after, Limit: 1, Oldest: true},
		})
		if err != nil || len(runs.Items) == 0 {
			return false, err
		}
		nodes, err := st.ListNodeRuns(ctx, store.NodeRunFilter{FlowRunID: runs.Items[0].ID, NodeID: nodeID, ListOpts: store.ListOpts{Limit: 1, Oldest: true}})
		if err != nil || len(nodes.Items) == 0 {
			return false, err
		}
		fr, nr = &runs.Items[0], &nodes.Items[0]
		return true, nil
	}
	err := s.waitMatch(ctx, 5*time.Second, func(c store.Change) bool {
		return c.Resource == store.ResFlowRun || c.Resource == store.ResNodeRun
	}, find)
	if err != nil {
		return nil, nil, err
	}
	if fr == nil {
		return nil, nil, errors.New("the test run has not been recorded yet")
	}
	return fr, nr, nil
}

// ---- approvals ----

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request, d core.ApprovalDecision) {
	id := r.PathValue("id")
	d.Principal = principal(r)
	a, err := s.opts.Engine.DecideApproval(r.Context(), id, d)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if a == nil {
		if a, err = s.opts.Store.GetApproval(r.Context(), id); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	v, err := s.resolver(r.Context()).approvalView(*a)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) approveApproval(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Comment string                     `json:"comment"`
		Edits   map[string]json.RawMessage `json:"edits"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if len(req.Edits) > 0 {
		a, err := s.opts.Store.GetApproval(r.Context(), r.PathValue("id"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if a.Type != store.ApprovalRequest {
			fieldProblem(w, "edits", "edits apply to request approvals only")
			return
		}
		var rq store.RequestApproval
		if err := json.Unmarshal([]byte(a.Request), &rq); err != nil {
			s.writeError(w, r, err)
			return
		}
		for path := range req.Edits {
			if !slices.ContainsFunc(rq.Editable, func(e store.RequestEditable) bool { return e.Key == path }) {
				fieldProblem(w, "edits."+path, fmt.Sprintf("%q is not an editable path of this approval", path))
				return
			}
		}
	}
	s.decideApproval(w, r, core.ApprovalDecision{Approve: true, Comment: req.Comment, Edits: req.Edits})
}

func (s *Server) denyApproval(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Comment   string `json:"comment"`
		CancelRun bool   `json:"cancel_run"`
		Edits     any    `json:"edits"` // accepted and ignored
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if req.CancelRun {
		a, err := s.opts.Store.GetApproval(r.Context(), r.PathValue("id"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if a.Type != store.ApprovalToolCall {
			fieldProblem(w, "cancel_run", "cancel_run applies to tool_call approvals only")
			return
		}
	}
	s.decideApproval(w, r, core.ApprovalDecision{Comment: req.Comment, CancelRun: req.CancelRun})
}

// ---- kill switch ----

func killSwitchOf(ks store.KillSwitch, reason string) killSwitchView {
	v := killSwitchView{Paused: ks.Paused}
	if ks.Paused {
		v.KillRunning, v.Since, v.By, v.Reason = ks.KillRunning, utcPtr(&ks.Since), nonEmpty(ks.By), nonEmpty(reason)
	}
	return v
}

func (s *Server) pauseKillSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KillRunning bool   `json:"kill_running"`
		Reason      string `json:"reason"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	st := s.opts.Store
	// core.Engine.Pause has no reason parameter, so the API keeps it. It is
	// stored first so the killswitch.changed event the engine's write triggers
	// already carries it.
	if req.Reason != "" {
		b, _ := json.Marshal(req.Reason)
		if err := st.SetSetting(r.Context(), killSwitchReasonKey, b); err != nil {
			s.writeError(w, r, err)
			return
		}
	} else if err := st.DeleteSetting(r.Context(), killSwitchReasonKey); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, err)
		return
	}
	ks, err := s.opts.Engine.Pause(r.Context(), req.KillRunning, principal(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, killSwitchOf(ks, req.Reason))
}

func (s *Server) resumeKillSwitch(w http.ResponseWriter, r *http.Request) {
	ks, err := s.opts.Engine.Resume(r.Context(), principal(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.opts.Store.DeleteSetting(r.Context(), killSwitchReasonKey); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log().Warn("api: cannot clear the kill switch reason", "err", err)
	}
	writeJSON(w, http.StatusOK, killSwitchOf(ks, ""))
}

// ---- runtimes ----

type probeResultView struct {
	Runtime           string          `json:"runtime"`
	OK                bool            `json:"ok"`
	CheckedAt         time.Time       `json:"checked_at"`
	DurationMS        int64           `json:"duration_ms"`
	Error             *string         `json:"error"`
	AgentInfo         json.RawMessage `json:"agent_info"`
	Capabilities      json.RawMessage `json:"capabilities,omitempty"`
	LoadSession       bool            `json:"load_session"`
	AvailableModes    []probeModeView `json:"available_modes"`
	AskModeAvailable  bool            `json:"ask_mode_available"`
	FullModeAvailable bool            `json:"full_mode_available"`
}

type probeModeView struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type runtimeView struct {
	Name      string            `json:"name"`
	Command   []string          `json:"command"`
	Modes     map[string]string `json:"modes"`
	EnvKeys   []string          `json:"env_keys"`
	LastProbe *probeResultView  `json:"last_probe"`
}

func (s *Server) listRuntimes(w http.ResponseWriter, _ *http.Request) {
	cfg := s.opts.Workspace().Config.Value
	names := make([]string, 0)
	if cfg != nil {
		for name := range cfg.Runtimes {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := struct {
		Items []runtimeView `json:"items"`
	}{Items: []runtimeView{}}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	for _, name := range names {
		rt := cfg.Runtimes[name]
		v := runtimeView{Name: name, Command: nonNil(rt.Command), Modes: map[string]string{}, EnvKeys: []string{}}
		if rt.Modes.Ask != "" {
			v.Modes["ask"] = rt.Modes.Ask
		}
		if rt.Modes.Full != "" {
			v.Modes["full"] = rt.Modes.Full
		}
		for k := range rt.Env {
			v.EnvKeys = append(v.EnvKeys, k)
		}
		sort.Strings(v.EnvKeys)
		if p, ok := s.probes[name]; ok {
			v.LastProbe = &p
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) probeRuntime(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.opts.Workspace().Config.Value
	if cfg == nil {
		notFound(w, "no runtime %q", name)
		return
	}
	rt, ok := cfg.Runtimes[name]
	if !ok {
		notFound(w, "no runtime %q", name)
		return
	}
	res, err := s.opts.Engine.ProbeRuntime(r.Context(), name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	v := probeResultView{
		Runtime: name, OK: res.OK, CheckedAt: time.Now().UTC(), DurationMS: res.Duration.Milliseconds(),
		Error: nonEmpty(res.Error), AgentInfo: rawOrNull(res.AgentInfo), AvailableModes: []probeModeView{},
	}
	if len(bytes.TrimSpace(res.Capabilities)) > 0 {
		v.Capabilities = res.Capabilities
		var caps struct {
			LoadSession bool `json:"loadSession"`
		}
		_ = json.Unmarshal(res.Capabilities, &caps)
		v.LoadSession = caps.LoadSession
	}
	for _, m := range res.Modes {
		v.AvailableModes = append(v.AvailableModes, probeModeView{ID: m, Name: m})
	}
	v.AskModeAvailable = rt.Modes.Ask != "" && slices.Contains(res.Modes, rt.Modes.Ask)
	v.FullModeAvailable = rt.Modes.Full != "" && slices.Contains(res.Modes, rt.Modes.Full)
	if !res.OK && v.Error == nil {
		msg := "probe failed"
		v.Error = &msg
	}
	s.probeMu.Lock()
	s.probes[name] = v
	s.probeMu.Unlock()
	writeJSON(w, http.StatusOK, v)
}
