package api

import (
	"net/http"
	"sort"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

type queueItemView struct {
	FlowRun        flowRunView `json:"flow_run"`
	Position       int         `json:"position"`
	WaitingFor     string      `json:"waiting_for"`
	QueuedAt       time.Time   `json:"queued_at"`
	WaitingNodeIDs []string    `json:"waiting_node_ids"`
}

type queueFlowLimitView struct {
	FlowID        string `json:"flow_id"`
	MaxConcurrent int    `json:"max_concurrent"`
	Running       int    `json:"running"`
}

type queueLimitsView struct {
	MaxAgentRuns     int                  `json:"max_agent_runs"`
	MaxCommandRuns   int                  `json:"max_command_runs"`
	AgentRunsInUse   int                  `json:"agent_runs_in_use"`
	CommandRunsInUse int                  `json:"command_runs_in_use"`
	Flows            []queueFlowLimitView `json:"flows"`
}

type queueView struct {
	Items                 []queueItemView `json:"items"`
	Total                 int             `json:"total"`
	WaitingForFlowSlot    int             `json:"waiting_for_flow_slot"`
	WaitingForAgentSlot   int             `json:"waiting_for_agent_slot"`
	WaitingForCommandSlot int             `json:"waiting_for_command_slot"`
	Limits                queueLimitsView `json:"limits"`
}

func (s *Server) getQueue(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	flowID := q.slug("flow_id")
	if !q.ok(w) {
		return
	}
	qs, err := s.opts.Store.ListQueue(r.Context(), flowID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ks, err := s.opts.Store.GetKillSwitch(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	ws := s.opts.Workspace()
	cfg := ws.Config.Value
	v := queueView{Items: make([]queueItemView, 0, len(qs.Items)), Limits: queueLimitsView{
		MaxAgentRuns: cfg.MaxAgentRuns, MaxCommandRuns: cfg.MaxCommandRuns,
		AgentRunsInUse: qs.NodeInUse[store.NodeAgent], CommandRunsInUse: qs.NodeInUse[store.NodeCommand],
		Flows: []queueFlowLimitView{},
	}}
	flows := map[string]bool{}
	for i, it := range qs.Items {
		wf := "flow_slot"
		switch {
		case ks.Paused:
			wf = "kill_switch"
		case it.WaitingType == store.NodeAgent:
			wf = "agent_slot"
		case it.WaitingType != "":
			wf = "command_slot"
		}
		switch wf {
		case "flow_slot":
			v.WaitingForFlowSlot++
		case "agent_slot":
			v.WaitingForAgentSlot++
		case "command_slot":
			v.WaitingForCommandSlot++
		}
		flows[it.FlowRun.FlowID] = true
		v.Items = append(v.Items, queueItemView{
			FlowRun: flowRunFull(it.FlowRun), Position: i + 1, WaitingFor: wf, QueuedAt: utc(it.QueuedAt),
			WaitingNodeIDs: nonNilStrings(it.WaitingNodeIDs),
		})
	}
	v.Total = len(v.Items)
	ids := make([]string, 0, len(flows))
	for id := range flows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		limit := spec.DefaultMaxConcurrent
		if f := ws.Flow(id); f != nil && f.Value != nil && f.Value.MaxConcurrent > 0 {
			limit = f.Value.MaxConcurrent
		}
		v.Limits.Flows = append(v.Limits.Flows, queueFlowLimitView{FlowID: id, MaxConcurrent: limit, Running: qs.FlowRunning[id]})
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) removeFromQueue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("flow_run_id")
	if !ulidRE.MatchString(id) {
		badRequest(w, "flow_run_id must be a ULID")
		return
	}
	fr, err := s.opts.Engine.RemoveQueued(r.Context(), id, principal(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, flowRunFull(*fr))
}

func (s *Server) clearQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FlowID string `json:"flow_id"`
	}
	if !decodeJSON(w, r, &req, true) {
		return
	}
	if req.FlowID != "" && !spec.ValidID(req.FlowID) {
		fieldProblem(w, "flow_id", "flow_id must be a flow id")
		return
	}
	ids, err := s.opts.Engine.ClearQueue(r.Context(), req.FlowID, principal(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": len(ids), "flow_run_ids": nonNilStrings(ids)})
}
