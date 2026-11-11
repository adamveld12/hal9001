package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/store/blob"
)

var (
	flowRunStatuses = []string{"queued", "running", "awaiting_approval", "succeeded", "failed", "killed", "interrupted", "cancelled"}
	flowRunKinds    = []string{"live", "replay", "test", "continuation"}
	nodeDecisions   = []string{"emitted", "dropped", "error", "skipped"}
	agentStatuses   = []string{"queued", "running", "awaiting_approval", "succeeded", "failed", "denied", "killed", "interrupted", "timeout"}
	sensorStatuses  = []string{"running", "succeeded", "failed", "timeout", "killed", "interrupted"}
	approvalStates  = []string{"pending", "approved", "denied", "expired"}
	approvalTypes   = []string{"proposal", "tool_call", "request"}
	priorities      = []string{"low", "normal", "high", "urgent"}
)

// settledFlowRun reports whether `wait` can stop: the run finished or is
// waiting for a person.
func settledFlowRun(fr *store.FlowRun) bool {
	return fr.Status.Terminal() || fr.Status == store.FlowRunAwaitingApproval
}

func settledNodeRun(nr *store.NodeRun) bool {
	switch nr.Status {
	case store.NodeQueued, store.NodeRunning:
		return false
	}
	return true
}

// ---- blobs ----

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if !hashRE.MatchString(hash) {
		badRequest(w, "hash must be 64 lowercase hex characters")
		return
	}
	data, err := s.opts.Store.Blobs().Get(hash)
	switch {
	case errors.Is(err, blob.ErrNotFound), errors.Is(err, blob.ErrInvalidRef):
		notFound(w, "no blob %s", hash)
		return
	case err != nil:
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data) //nolint:gosec // served as application/octet-stream with nosniff, never rendered
}

// ---- kill switch ----

func (s *Server) getKillSwitch(w http.ResponseWriter, r *http.Request) {
	v, err := s.killSwitchView(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- sensor runs ----

type pageView[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func (s *Server) sensorKnown(r *http.Request, id string) (bool, error) {
	if s.opts.Workspace().Sensor(id) != nil {
		return true, nil
	}
	page, err := s.opts.Store.ListSensorRuns(r.Context(), store.SensorRunFilter{SensorID: id, Slim: true, ListOpts: store.ListOpts{Limit: 1}})
	return len(page.Items) > 0, err
}

func (s *Server) listSensorRuns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := newQuery(r)
	opts := q.page()
	status := q.enum("status", sensorStatuses...)
	if !q.ok(w) {
		return
	}
	known, err := s.sensorKnown(r, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !known {
		notFound(w, "no sensor %q", id)
		return
	}
	// The store has four statuses; timeout and killed are failed runs with
	// that error class, so those filters (and failed, which excludes them)
	// are applied here by scanning pages.
	f := store.SensorRunFilter{SensorID: id, ListOpts: opts}
	var keep func(store.SensorRun) bool
	switch status {
	case "running", "succeeded", "interrupted":
		f.Status = store.SensorStatus(status)
	case "failed", "timeout", "killed":
		f.Status = store.SensorFailed
		keep = func(run store.SensorRun) bool { return sensorRunStatus(run) == status }
	}
	runs, cursor, err := collectSensorRuns(r, s.opts.Store, f, keep)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := pageView[sensorRunView]{Items: make([]sensorRunView, 0, len(runs)), NextCursor: nextCursor(cursor)}
	for _, run := range runs {
		out.Items = append(out.Items, s.sensorRunView(run, true))
	}
	writeJSON(w, http.StatusOK, out)
}

// collectSensorRuns returns one page of runs that satisfy keep (nil keeps all).
func collectSensorRuns(r *http.Request, st *store.Store, f store.SensorRunFilter, keep func(store.SensorRun) bool) ([]store.SensorRun, string, error) {
	limit := f.Limit
	if keep == nil {
		page, err := st.ListSensorRuns(r.Context(), f)
		return page.Items, page.NextCursor, err
	}
	f.Limit = maxLimit
	var out []store.SensorRun
	for {
		page, err := st.ListSensorRuns(r.Context(), f)
		if err != nil {
			return nil, "", err
		}
		for _, run := range page.Items {
			if !keep(run) {
				continue
			}
			if len(out) == limit {
				return out, out[limit-1].ID, nil
			}
			out = append(out, run)
		}
		if page.NextCursor == "" {
			return out, "", nil
		}
		f.Cursor = page.NextCursor
	}
}

func (s *Server) getSensorRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := newQuery(r)
	wait := q.wait(0)
	if !q.ok(w) {
		return
	}
	st := s.opts.Store
	run, err := st.GetSensorRun(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if wait > 0 && run.Status == store.SensorRunning {
		err = s.waitFor(r.Context(), wait, store.ResSensorRun, id, func() (bool, error) {
			cur, err := st.GetSensorRun(r.Context(), id)
			if err != nil {
				return false, err
			}
			run = cur
			return cur.Status != store.SensorRunning, nil
		})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.sensorRunView(*run, false))
}

// ---- events ----

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	opts := q.page()
	sensorID := q.slug("sensor_id")
	flowID := q.slug("flow_id")
	status := q.enum("status", flowRunStatuses...)
	decision := q.enum("decision", nodeDecisions...)
	kind := q.enum("kind", flowRunKinds...)
	search := q.str("q")
	if q.v.Has("q") && search == "" {
		q.fail("q must not be empty")
	}
	tr := q.timeRange()
	if !q.ok(w) {
		return
	}
	kinds := []store.FlowRunKind{store.KindLive, store.KindReplay, store.KindContinuation}
	if kind != "" {
		kinds = []store.FlowRunKind{store.FlowRunKind(kind)}
	}
	var statuses []store.FlowRunStatus
	if status != "" {
		statuses = []store.FlowRunStatus{store.FlowRunStatus(status)}
	}
	f := store.EventFilter{SensorID: sensorID, Search: search, Time: tr, ListOpts: opts}
	if flowID != "" || status != "" || decision != "" || kind != "" {
		f.FlowID, f.RunStatuses, f.RunKinds, f.RunDecision = flowID, statuses, kinds, store.NodeDecision(decision)
	}
	st := s.opts.Store
	page, err := st.ListEvents(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := pageView[eventSummaryView]{Items: make([]eventSummaryView, 0, len(page.Items)), NextCursor: nextCursor(page.NextCursor)}
	for _, ev := range page.Items {
		runs, err := st.ListFlowRuns(r.Context(), store.FlowRunFilter{
			EventID: ev.ID, FlowID: flowID, Statuses: statuses, Kinds: kinds, Decision: store.NodeDecision(decision),
			ListOpts: store.ListOpts{Limit: maxLimit, Oldest: true},
		})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Items = append(out.Items, s.eventSummary(ev, runs.Items))
	}
	writeJSON(w, http.StatusOK, out)
}

type nodeRunTreeView struct {
	NodeRun  nodeRunView       `json:"node_run"`
	Children []nodeRunTreeView `json:"children"`
}

type fanOutBranchView struct {
	FlowRun  flowRunView       `json:"flow_run"`
	NodeRuns []nodeRunTreeView `json:"node_runs"`
}

type eventDetailView struct {
	Event     eventView          `json:"event"`
	SensorRun *sensorRunView     `json:"sensor_run"`
	FanOut    []fanOutBranchView `json:"fan_out"`
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	st := s.opts.Store
	fan, err := st.GetEventFanOut(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	out := eventDetailView{Event: s.eventView(&fan.Event), FanOut: make([]fanOutBranchView, 0, len(fan.FlowRuns))}
	if fan.Event.SensorRunID != "" {
		run, err := st.GetSensorRun(r.Context(), fan.Event.SensorRunID)
		switch {
		case err == nil:
			v := s.sensorRunView(*run, true)
			out.SensorRun = &v
		case !errors.Is(err, store.ErrNotFound):
			s.writeError(w, r, err)
			return
		}
	}
	for _, tree := range fan.FlowRuns {
		branch := fanOutBranchView{FlowRun: flowRunFull(tree.FlowRun), NodeRuns: []nodeRunTreeView{}}
		for _, n := range tree.Nodes {
			v, err := s.nodeTreeView(r, rv, n)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			branch.NodeRuns = append(branch.NodeRuns, v)
		}
		out.FanOut = append(out.FanOut, branch)
	}
	writeJSON(w, http.StatusOK, out)
}

// nodeTreeView converts a slim store tree to previews, reading each node run
// in full so its 4 KiB text previews can be cut.
func (s *Server) nodeTreeView(r *http.Request, rv *resolver, n *store.NodeRunTree) (nodeRunTreeView, error) {
	full, err := s.opts.Store.GetNodeRun(r.Context(), n.ID)
	if err != nil {
		return nodeRunTreeView{}, err
	}
	out := nodeRunTreeView{NodeRun: rv.nodeRunView(*full, true), Children: []nodeRunTreeView{}}
	for _, c := range n.Children {
		cv, err := s.nodeTreeView(r, rv, c)
		if err != nil {
			return nodeRunTreeView{}, err
		}
		out.Children = append(out.Children, cv)
	}
	return out, nil
}

// ---- flow runs and node runs ----

func (s *Server) listFlowRuns(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	opts := q.page()
	flowID := q.slug("flow_id")
	eventID := q.ulid("event_id")
	status := q.enum("status", flowRunStatuses...)
	kind := q.enum("kind", flowRunKinds...)
	replayOf := q.ulid("replay_of")
	continuedFrom := q.ulid("continuation_of")
	conversation := q.ulid("conversation")
	tr := q.timeRange()
	if !q.ok(w) {
		return
	}
	f := store.FlowRunFilter{
		FlowID: flowID, EventID: eventID, ReplayOf: replayOf, ContinuationOf: continuedFrom, Conversation: conversation, Time: tr, ListOpts: opts,
	}
	if status != "" {
		f.Statuses = []store.FlowRunStatus{store.FlowRunStatus(status)}
	}
	if kind != "" {
		f.Kinds = []store.FlowRunKind{store.FlowRunKind(kind)}
	}
	page, err := s.opts.Store.ListFlowRuns(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := pageView[flowRunView]{Items: make([]flowRunView, 0, len(page.Items)), NextCursor: nextCursor(page.NextCursor)}
	for _, fr := range page.Items {
		out.Items = append(out.Items, flowRunFull(fr))
	}
	writeJSON(w, http.StatusOK, out)
}

type flowRunDetailView struct {
	FlowRun  flowRunView   `json:"flow_run"`
	NodeRuns []nodeRunView `json:"node_runs"`
}

// flowRunDetail loads a flow run with every node run, in creation order.
func (s *Server) flowRunDetail(r *http.Request, fr store.FlowRun) (flowRunDetailView, error) {
	rv := s.resolver(r.Context())
	out := flowRunDetailView{FlowRun: flowRunFull(fr), NodeRuns: []nodeRunView{}}
	f := store.NodeRunFilter{FlowRunID: fr.ID, ListOpts: store.ListOpts{Limit: 500, Oldest: true}}
	for {
		page, err := s.opts.Store.ListNodeRuns(r.Context(), f)
		if err != nil {
			return out, err
		}
		for _, nr := range page.Items {
			out.NodeRuns = append(out.NodeRuns, rv.nodeRunView(nr, false))
		}
		if page.NextCursor == "" {
			return out, nil
		}
		f.Cursor = page.NextCursor
	}
}

func (s *Server) getFlowRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := newQuery(r)
	wait := q.wait(0)
	if !q.ok(w) {
		return
	}
	st := s.opts.Store
	fr, err := st.GetFlowRun(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if wait > 0 && !settledFlowRun(fr) {
		err = s.waitFor(r.Context(), wait, store.ResFlowRun, id, func() (bool, error) {
			cur, err := st.GetFlowRun(r.Context(), id)
			if err != nil {
				return false, err
			}
			fr = cur
			return settledFlowRun(cur), nil
		})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	out, err := s.flowRunDetail(r, *fr)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type nodeRunDetailView struct {
	nodeRunView
	AgentRuns []agentRunView `json:"agent_runs"`
}

func (s *Server) getNodeRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := newQuery(r)
	wait := q.wait(0)
	if !q.ok(w) {
		return
	}
	st := s.opts.Store
	nr, err := st.GetNodeRun(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if wait > 0 && !settledNodeRun(nr) {
		err = s.waitFor(r.Context(), wait, store.ResNodeRun, id, func() (bool, error) {
			cur, err := st.GetNodeRun(r.Context(), id)
			if err != nil {
				return false, err
			}
			nr = cur
			return settledNodeRun(cur), nil
		})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	rv := s.resolver(r.Context())
	out := nodeRunDetailView{nodeRunView: rv.nodeRunView(*nr, false), AgentRuns: []agentRunView{}}
	runs, err := st.ListAgentRuns(r.Context(), store.AgentRunFilter{NodeRunID: id, ListOpts: store.ListOpts{Limit: 500, Oldest: true}})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for _, ar := range runs.Items {
		v, err := rv.agentRunView(ar, false)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.AgentRuns = append(out.AgentRuns, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- flow versions ----

type flowVersionSummaryView struct {
	FlowID    string    `json:"flow_id"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
	RunCount  int       `json:"run_count"`
}

type flowVersionView struct {
	flowVersionSummaryView
	YAML string     `json:"yaml"`
	Flow *spec.Flow `json:"flow"`
}

func (s *Server) listFlowVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := newQuery(r)
	limit := q.limit()
	cursor := q.str("cursor")
	if cursor != "" && !hashRE.MatchString(cursor) {
		q.fail("cursor is not a cursor returned by this API")
	}
	if !q.ok(w) {
		return
	}
	versions, err := s.opts.Store.ListFlowVersions(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(versions) == 0 && s.opts.Workspace().Flow(id) == nil {
		notFound(w, "no flow %q", id)
		return
	}
	if cursor != "" {
		i := slices.IndexFunc(versions, func(v store.FlowVersion) bool { return v.Hash == cursor })
		if i < 0 {
			badRequest(w, "cursor is not a cursor returned by this API")
			return
		}
		versions = versions[i+1:]
	}
	counts, err := s.opts.Store.FlowVersionRunCounts(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := pageView[flowVersionSummaryView]{Items: []flowVersionSummaryView{}}
	if len(versions) > limit {
		versions = versions[:limit]
		out.NextCursor = &versions[limit-1].Hash
	}
	for _, v := range versions {
		out.Items = append(out.Items, flowVersionSummaryView{FlowID: v.FlowID, Hash: v.Hash, CreatedAt: utc(v.FirstSeen), RunCount: counts[v.Hash]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getFlowVersion(w http.ResponseWriter, r *http.Request) {
	id, hash := r.PathValue("id"), r.PathValue("hash")
	if !hashRE.MatchString(hash) {
		badRequest(w, "hash must be 64 lowercase hex characters")
		return
	}
	v, err := s.opts.Store.GetFlowVersion(r.Context(), id, hash)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	counts, err := s.opts.Store.FlowVersionRunCounts(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f, _ := spec.ParseFlow(id, []byte(v.YAML))
	writeJSON(w, http.StatusOK, flowVersionView{
		flowVersionSummaryView: flowVersionSummaryView{FlowID: v.FlowID, Hash: v.Hash, CreatedAt: utc(v.FirstSeen), RunCount: counts[hash]},
		YAML:                   v.YAML, Flow: flowValue(f),
	})
}

// ---- agent runs ----

func (s *Server) listAgentRuns(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	opts := q.page()
	status := q.enum("status", agentStatuses...)
	runtimeName := q.slug("runtime")
	flowRunID := q.ulid("flow_run_id")
	nodeRunID := q.ulid("node_run_id")
	assistSessionID := q.ulid("assist_session_id")
	if !q.ok(w) {
		return
	}
	f := store.AgentRunFilter{Runtime: runtimeName, FlowRunID: flowRunID, NodeRunID: nodeRunID, AssistSessionID: assistSessionID, ListOpts: opts}
	if status != "" {
		f.Statuses = []store.AgentStatus{store.AgentStatus(status)}
	}
	page, err := s.opts.Store.ListAgentRuns(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	out := pageView[agentRunView]{Items: make([]agentRunView, 0, len(page.Items)), NextCursor: nextCursor(page.NextCursor)}
	for _, ar := range page.Items {
		v, err := rv.agentRunView(ar, false)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

type agentRunDetailView struct {
	agentRunView
	PhaseRunIDs []string       `json:"phase_run_ids"`
	Approvals   []approvalView `json:"approvals"`
}

func (s *Server) getAgentRun(w http.ResponseWriter, r *http.Request) {
	st := s.opts.Store
	ar, err := st.GetAgentRun(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	base, err := rv.agentRunView(*ar, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := agentRunDetailView{agentRunView: base, PhaseRunIDs: []string{}, Approvals: []approvalView{}}
	// The phases of a flow run's agent node, or the turns of an assist session.
	phases, err := st.ListAgentRuns(r.Context(), store.AgentRunFilter{NodeRunID: ar.NodeRunID, AssistSessionID: ar.AssistSessionID, Slim: true, ListOpts: store.ListOpts{Limit: 500, Oldest: true}})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for _, p := range phases.Items {
		out.PhaseRunIDs = append(out.PhaseRunIDs, p.ID)
	}
	approvals, err := st.ListApprovals(r.Context(), store.ApprovalFilter{AgentRunID: ar.ID, ListOpts: store.ListOpts{Limit: 500, Oldest: true}})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	for _, a := range approvals.Items {
		v, err := rv.approvalView(a)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Approvals = append(out.Approvals, v)
	}
	writeJSON(w, http.StatusOK, out)
}

const (
	defaultEventLimit = 200
	maxEventLimit     = 1000
	storeEventChunk   = 500
)

type agentEventListView struct {
	Items   []agentEventView `json:"items"`
	LastSeq int64            `json:"last_seq"`
	HasMore bool             `json:"has_more"`
}

func (s *Server) listAgentEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := newQuery(r)
	after := int64(0)
	if v := q.str("after_seq"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			q.fail("after_seq must be a non-negative integer")
		} else {
			after = n
		}
	}
	limit := defaultEventLimit
	if v := q.str("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxEventLimit {
			q.fail("limit must be an integer from 1 to %d", maxEventLimit)
		} else {
			limit = n
		}
	}
	kinds := q.list("kind")
	if !q.ok(w) {
		return
	}
	st := s.opts.Store
	if _, err := st.GetAgentRun(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	// Read limit+1 events to learn whether another page follows; the store
	// serves at most 500 per call.
	var evs []store.AgentEvent
	cursor := after
	for len(evs) <= limit {
		chunk, err := st.ListAgentEvents(r.Context(), store.AgentEventFilter{
			AgentRunID: id, AfterSeq: cursor, Kinds: kinds, Limit: min(limit+1-len(evs), storeEventChunk),
		})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		evs = append(evs, chunk...)
		if len(chunk) == 0 {
			break
		}
		cursor = chunk[len(chunk)-1].Seq
	}
	out := agentEventListView{Items: []agentEventView{}, LastSeq: after}
	if len(evs) > limit {
		evs, out.HasMore = evs[:limit], true
	}
	for _, ev := range evs {
		out.Items = append(out.Items, s.agentEventView(ev, false))
		out.LastSeq = ev.Seq
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listAgentPermissionRequests(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st := s.opts.Store
	if _, err := st.GetAgentRun(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	out := struct {
		Items []permissionRequestView `json:"items"`
	}{Items: []permissionRequestView{}}
	f := store.PermissionRequestFilter{AgentRunID: id, ListOpts: store.ListOpts{Limit: 500, Oldest: true}}
	for {
		page, err := st.ListPermissionRequests(r.Context(), f)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		for _, p := range page.Items {
			v, err := rv.permissionRequestView(p)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			out.Items = append(out.Items, v)
		}
		if page.NextCursor == "" {
			break
		}
		f.Cursor = page.NextCursor
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) downloadAgentRunRaw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ar, err := s.opts.Store.GetAgentRun(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	data, err := s.opts.Store.ReadContent(ar.RawJSONRPC)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="agent-run-`+id+`.jsonl"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data) //nolint:gosec // served as application/octet-stream with nosniff, never rendered
}

// ---- approvals ----

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	opts := q.page()
	var statuses []store.ApprovalState
	for _, st := range q.list("status") {
		if !slices.Contains(approvalStates, st) {
			q.fail("status must be a comma-separated list of %s", strings.Join(approvalStates, ", "))
			break
		}
		statuses = append(statuses, store.ApprovalState(st))
	}
	typ := q.enum("type", approvalTypes...)
	agentRunID := q.ulid("agent_run_id")
	flowRunID := q.ulid("flow_run_id")
	if !q.ok(w) {
		return
	}
	f := store.ApprovalFilter{Statuses: statuses, Type: store.ApprovalType(typ), AgentRunID: agentRunID, FlowRunID: flowRunID, ListOpts: opts}
	page, err := s.opts.Store.ListApprovals(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	out := pageView[approvalView]{Items: make([]approvalView, 0, len(page.Items)), NextCursor: nextCursor(page.NextCursor)}
	for _, a := range page.Items {
		v, err := rv.approvalView(a)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	a, err := s.opts.Store.GetApproval(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.resolver(r.Context()).approvalView(*a)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- notifications ----

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	opts := q.page()
	flowID := q.slug("flow_id")
	channel := q.enum("channel", store.ChannelApp, store.ChannelTerminalNotifier, store.ChannelNone)
	if !q.ok(w) {
		return
	}
	page, err := s.opts.Store.ListNotifications(r.Context(), store.NotificationFilter{FlowID: flowID, Channel: channel, ListOpts: opts})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pageView[store.Notification]{Items: page.Items, NextCursor: nextCursor(page.NextCursor)})
}

// ---- inbox ----

func (s *Server) listInbox(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	opts := q.page()
	minPriority := q.enum("priority", priorities...)
	maxPriority := q.enum("max_priority", priorities...)
	read := q.boolp("read")
	starred := q.boolp("starred")
	archived := q.enum("archived", "true", "false", "any")
	dryRun := q.boolp("dry_run")
	flowID := q.slug("flow_id")
	search := q.str("q")
	if q.v.Has("q") && search == "" {
		q.fail("q must not be empty")
	}
	sortBy := q.enum("sort", "priority", "time")
	if !q.ok(w) {
		return
	}
	f := store.InboxFilter{Read: read, Starred: starred, DryRun: dryRun, FlowID: flowID, Search: search, ByPriority: sortBy != "time", ListOpts: opts}
	switch archived {
	case "true":
		t := true
		f.Archived = &t
	case "any":
	default:
		no := false
		f.Archived = &no
	}
	if minPriority != "" || maxPriority != "" {
		if f.Priorities = priorityRange(minPriority, maxPriority); len(f.Priorities) == 0 {
			badRequest(w, "max_priority is below priority")
			return
		}
	}
	page, err := s.opts.Store.ListInbox(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rv := s.resolver(r.Context())
	out := pageView[inboxItemView]{Items: make([]inboxItemView, 0, len(page.Items)), NextCursor: nextCursor(page.NextCursor)}
	for _, it := range page.Items {
		v, err := rv.inboxView(it)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getInboxItem(w http.ResponseWriter, r *http.Request) {
	it, err := s.opts.Store.GetInboxItem(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.resolver(r.Context()).inboxView(*it)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateInboxItem(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		Read     *bool `json:"read"`
		Starred  *bool `json:"starred"`
		Archived *bool `json:"archived"`
	}
	if !decodeJSON(w, r, &patch, false) {
		return
	}
	if patch.Read == nil && patch.Starred == nil && patch.Archived == nil {
		fieldProblem(w, "", "set at least one of read, starred, archived")
		return
	}
	it, err := s.opts.Store.SetInboxFlags(r.Context(), r.PathValue("id"), store.InboxFlags{Read: patch.Read, Starred: patch.Starred, Archived: patch.Archived})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.resolver(r.Context()).inboxView(*it)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
