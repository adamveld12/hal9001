package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/transform"
)

const (
	transformPreviewTimeout = 10 * time.Second
	transformRunTimeout     = 5 * time.Second
	defaultSamples          = 10
	maxSamples              = 50
)

func issueDiagnostics(issues []transform.Issue) []spec.Diagnostic {
	out := make([]spec.Diagnostic, len(issues))
	for i, is := range issues {
		sev := spec.SeverityError
		if is.Severity == transform.SeverityWarning {
			sev = spec.SeverityWarning
		}
		out[i] = spec.Diagnostic{Path: is.Path, Severity: sev, Code: is.Code, Message: is.Message}
	}
	return out
}

type transformStepResultView struct {
	Step         int     `json:"step"`
	Description  string  `json:"description"`
	OutputsCount int     `json:"outputs_count"`
	Dropped      bool    `json:"dropped"`
	Error        *string `json:"error"`
}

type transformPreviewView struct {
	JQ          string                    `json:"jq"`
	Description []string                  `json:"description"`
	Decision    string                    `json:"decision"`
	Outputs     []transform.Output        `json:"outputs"`
	Reason      *string                   `json:"reason"`
	Error       *string                   `json:"error"`
	StepResults []transformStepResultView `json:"step_results"`
	Diagnostics []spec.Diagnostic         `json:"diagnostics"`
}

// previewTransform runs steps on a sample, step by step. Compute only.
func (s *Server) previewTransform(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Steps []transform.Step `json:"steps"`
		Input json.RawMessage  `json:"input"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if len(req.Input) == 0 {
		fieldProblem(w, "input", "input is required (one JSON value)")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), transformPreviewTimeout)
	defer cancel()
	res := transform.Preview(ctx, req.Steps, req.Input, transform.NewRunner("", transformRunTimeout))
	if ctx.Err() != nil && res.Decision == transform.DecisionError && res.Error == "" {
		res.Error = "the preview took too long"
	}
	v := transformPreviewView{
		JQ: res.JQ, Description: res.Description, Decision: res.Decision, Outputs: res.Outputs,
		Reason: nonEmpty(res.Reason), Error: nonEmpty(res.Error),
		StepResults: make([]transformStepResultView, len(res.StepResults)), Diagnostics: issueDiagnostics(res.Issues),
	}
	for i, sr := range res.StepResults {
		v.StepResults[i] = transformStepResultView{
			Step: sr.Step, Description: sr.Description, OutputsCount: sr.OutputsCount, Dropped: sr.Dropped, Error: nonEmpty(sr.Error),
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// compileTransform returns the jq program for steps.
func (s *Server) compileTransform(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Steps []transform.Step `json:"steps"`
	}
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if issues := transform.Validate(req.Steps); transform.HasErrors(issues) {
		validationProblem(w, "the steps have errors", issueDiagnostics(issues))
		return
	}
	jq, err := transform.Compile(req.Steps)
	if err != nil {
		validationProblem(w, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		JQ          string   `json:"jq"`
		Description []string `json:"description"`
	}{JQ: jq, Description: transform.Describe(req.Steps)})
}

type flowSampleView struct {
	Source     string          `json:"source"`
	EventID    *string         `json:"event_id"`
	NodeRunID  *string         `json:"node_run_id"`
	FromNodeID *string         `json:"from_node_id"`
	SensorID   *string         `json:"sensor_id"`
	CreatedAt  time.Time       `json:"created_at"`
	Payload    json.RawMessage `json:"payload"`
}

// listFlowNodeSamples returns recent real inputs for a node: event payloads
// for an entry node, upstream node outputs for any other.
func (s *Server) listFlowNodeSamples(w http.ResponseWriter, r *http.Request) {
	flowID := r.PathValue("id")
	q := newQuery(r)
	nodeID := q.slug("node_id")
	if nodeID == "" && q.ok(w) {
		fieldProblem(w, "node_id", "node_id is required")
		return
	}
	limit := defaultSamples
	if v := q.str("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxSamples {
			q.fail("limit must be an integer from 1 to %d", maxSamples)
		} else {
			limit = n
		}
	}
	if !q.ok(w) {
		return
	}
	raw, _, err := s.readFile(spec.KindFlow, flowID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	flow, _ := spec.ParseFlow(flowID, raw)
	items := []flowSampleView{}
	if flow != nil {
		// A node that is not in the saved flow yet has no incoming edges there, so it is sampled like an entry node.
		if items, err = s.collectSamples(r.Context(), flow, nodeID, limit); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Items []flowSampleView `json:"items"`
	}{items})
}

func (s *Server) collectSamples(ctx context.Context, flow *spec.Flow, nodeID string, limit int) ([]flowSampleView, error) {
	st := s.opts.Store
	items := []flowSampleView{}
	var incoming []spec.Edge
	for _, e := range flow.Edges {
		if e.To == nodeID {
			incoming = append(incoming, e)
		}
	}
	if len(incoming) == 0 {
		for _, sensor := range flow.Subscribe {
			page, err := st.ListEvents(ctx, store.EventFilter{SensorID: sensor, ListOpts: store.ListOpts{Limit: limit}})
			if err != nil {
				return nil, err
			}
			for i := range page.Items {
				ev := &page.Items[i]
				items = append(items, flowSampleView{
					Source: "event", EventID: &ev.ID, SensorID: &ev.SensorID, CreatedAt: utc(ev.CreatedAt), Payload: s.contentJSON(ev.Payload),
				})
			}
		}
	}
	kinds := map[string]store.FlowRunKind{}
	for _, e := range incoming {
		from := flow.NodeByID(e.From)
		if from == nil || e.Port == spec.PortError {
			continue
		}
		declared := slices.DeleteFunc(from.OutPorts(), func(p string) bool { return p == spec.PortError })
		page, err := st.ListNodeRuns(ctx, store.NodeRunFilter{
			FlowID: flow.ID, NodeID: e.From, Statuses: []store.NodeRunStatus{store.NodeDone}, Decision: store.DecisionEmitted,
			ListOpts: store.ListOpts{Limit: limit * 3},
		})
		if err != nil {
			return nil, err
		}
		found := 0
		for i := range page.Items {
			nr := &page.Items[i]
			kind, seen := kinds[nr.FlowRunID]
			if !seen {
				fr, err := st.GetFlowRun(ctx, nr.FlowRunID)
				if err != nil {
					continue
				}
				kind = fr.Kind
				kinds[nr.FlowRunID] = kind
			}
			if kind == store.KindTest {
				continue
			}
			data, err := st.ReadContent(nr.Stdout)
			if err != nil {
				continue
			}
			oc := runner.ParseNodeOutput(data, 0, declared)
			if oc.Decision != runner.DecisionEmitted {
				continue
			}
			at := nr.QueuedAt
			if nr.EndedAt != nil {
				at = *nr.EndedAt
			}
			for _, o := range oc.Outputs {
				if o.Port != e.Port || found >= limit {
					continue
				}
				found++
				items = append(items, flowSampleView{
					Source: "node_run", NodeRunID: &nr.ID, FromNodeID: &e.From, CreatedAt: utc(at), Payload: o.Payload,
				})
			}
			if found >= limit {
				break
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
