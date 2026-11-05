package api

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

func needJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
}

type previewReply struct {
	JQ          string   `json:"jq"`
	Description []string `json:"description"`
	Decision    string   `json:"decision"`
	Outputs     []struct {
		Port    string         `json:"port"`
		Payload map[string]any `json:"payload"`
	} `json:"outputs"`
	Reason      *string `json:"reason"`
	Error       *string `json:"error"`
	StepResults []struct {
		Step         int     `json:"step"`
		OutputsCount int     `json:"outputs_count"`
		Dropped      bool    `json:"dropped"`
		Error        *string `json:"error"`
	} `json:"step_results"`
	Diagnostics []spec.Diagnostic `json:"diagnostics"`
}

const previewSteps = `[
  {"type":"filter","conditions":[{"field":"severity","op":"not_equals","value":"noise"}]},
  {"type":"set","field":"label","value":{"kind":"function","function":"uppercase","field":"severity"}},
  {"type":"route","default_port":"normal","rules":[{"port":"urgent","conditions":[{"field":"severity","op":"equals","value":"urgent"}]}]}
]`

func TestPreviewTransform(t *testing.T) {
	needJQ(t)
	e := newEnv(t)
	preview := func(steps, input string) *resp {
		return e.do(http.MethodPost, "/api/transform/preview", `{"steps":`+steps+`,"input":`+input+`}`)
	}

	var routed previewReply
	preview(previewSteps, `{"id":1,"severity":"urgent"}`).want(200).decode(&routed)
	if routed.Decision != "emitted" || len(routed.Outputs) != 1 || routed.Outputs[0].Port != "urgent" ||
		routed.Outputs[0].Payload["label"] != "URGENT" || routed.Outputs[0].Payload["_port"] != nil {
		t.Fatalf("routed: %+v", routed)
	}
	if len(routed.Description) != 3 || !strings.HasPrefix(routed.Description[0], "Keep only items where severity") ||
		!strings.Contains(routed.JQ, "# step 3:") || routed.Reason == nil || routed.Error != nil || len(routed.Diagnostics) != 0 {
		t.Errorf("routed details: %+v", routed)
	}
	if len(routed.StepResults) != 3 || routed.StepResults[2].OutputsCount != 1 {
		t.Errorf("step results: %+v", routed.StepResults)
	}

	var dropped previewReply
	preview(previewSteps, `{"id":2,"severity":"noise"}`).want(200).decode(&dropped)
	if dropped.Decision != "dropped" || len(dropped.Outputs) != 0 || dropped.Reason == nil ||
		*dropped.Reason != `Dropped by step 1: it did not meet the condition: severity is not "noise"` {
		t.Errorf("dropped: %+v reason %v", dropped, dropped.Reason)
	}
	if len(dropped.StepResults) != 3 || !dropped.StepResults[0].Dropped || dropped.StepResults[1].OutputsCount != 0 || dropped.StepResults[1].Dropped {
		t.Errorf("dropped step results: %+v", dropped.StepResults)
	}

	// A step that fails at run time: adding a named field to a list.
	var failed previewReply
	preview(`[{"type":"set","field":"x","value":{"kind":"constant","value":1}}]`, `[1,2]`).want(200).decode(&failed)
	if failed.Decision != "error" || failed.Error == nil || len(failed.StepResults) != 1 || failed.StepResults[0].Error == nil {
		t.Errorf("runtime error: %+v", failed)
	}

	// Invalid steps are reported as diagnostics with request-relative paths, not as HTTP errors.
	var invalid previewReply
	preview(`[{"type":"filter","conditions":[{"field":"a","op":"equals"}]}]`, `{}`).want(200).decode(&invalid)
	if invalid.Decision != "error" || invalid.JQ != "" || len(invalid.Diagnostics) != 1 ||
		invalid.Diagnostics[0].Path != "steps[0].conditions[0].value" || invalid.Diagnostics[0].Severity != spec.SeverityError {
		t.Errorf("invalid: %+v", invalid)
	}

	// No steps passes the item through.
	var empty previewReply
	preview(`[]`, `{"a":1}`).want(200).decode(&empty)
	if empty.Decision != "emitted" || len(empty.Outputs) != 1 || len(empty.Diagnostics) != 1 || empty.Diagnostics[0].Severity != spec.SeverityWarning {
		t.Errorf("empty: %+v", empty)
	}

	// The input may be any JSON value, including null.
	if r := preview(`[{"type":"filter","conditions":[{"field":".","op":"is_empty"}]}]`, `null`).want(200); !strings.Contains(string(r.Body), `"decision":"emitted"`) {
		t.Errorf("null input: %s", r.Body)
	}

	// Malformed requests.
	e.do(http.MethodPost, "/api/transform/preview", `{"steps":[]}`).want(422)
	e.do(http.MethodPost, "/api/transform/preview", `{"steps":[{"type":"filter","bogus":1}],"input":{}}`).want(400)
	e.do(http.MethodPost, "/api/transform/preview", `not json`).want(400)
}

func TestCompileTransform(t *testing.T) {
	e := newEnv(t)
	var ok struct {
		JQ          string   `json:"jq"`
		Description []string `json:"description"`
	}
	e.do(http.MethodPost, "/api/transform/compile", `{"steps":`+previewSteps+`}`).want(200).decode(&ok)
	if !strings.Contains(ok.JQ, "# step 1: Keep only items where severity is not") || !strings.Contains(ok.JQ, `_port`) || len(ok.Description) != 3 {
		t.Errorf("compiled: %+v", ok)
	}
	r := e.do(http.MethodPost, "/api/transform/compile", `{"steps":[{"type":"limit","field":"a"}]}`).want(422)
	if r.problem() != "validation-failed" || !strings.Contains(string(r.Body), "steps[0].count") {
		t.Errorf("invalid compile: %s", r.Body)
	}
}

// ---- samples ----

const samplesFlow = `id: tflow
enabled: true
subscribe: [src]
nodes:
  - id: first
    type: transform
    steps:
      - {type: filter, conditions: [{field: severity, op: exists}]}
      - type: route
        default_port: other
        rules: [{port: urgent, conditions: [{field: severity, op: equals, value: urgent}]}]
  - id: after-urgent
    type: transform
    steps: [{type: pick, fields: [id]}]
  - id: after-other
    type: command
    lang: jq
    script: '.'
edges:
  - {from: first, port: urgent, to: after-urgent}
  - {from: first, port: other, to: after-other}
`

func (e *env) writeFlow(id, yaml string) {
	e.t.Helper()
	if err := os.WriteFile(filepath.Join(e.home, spec.DirFlows, id+".yaml"), []byte(yaml), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.ws.Store(spec.Load(e.home))
}

type samplesReply struct {
	Items []struct {
		Source     string         `json:"source"`
		EventID    *string        `json:"event_id"`
		NodeRunID  *string        `json:"node_run_id"`
		FromNodeID *string        `json:"from_node_id"`
		SensorID   *string        `json:"sensor_id"`
		Payload    map[string]any `json:"payload"`
	} `json:"items"`
}

func TestFlowNodeSamples(t *testing.T) {
	e := newEnv(t)
	e.writeFlow("tflow", samplesFlow)
	e1 := e.event("src", "1", `{"id":"e1","severity":"urgent"}`)
	e2 := e.event("src", "2", `{"id":"e2","severity":"low"}`)
	e.event("other-sensor", "3", `{"id":"not-subscribed"}`)

	// Entry node: the newest events of the subscribed sensors.
	var entry samplesReply
	e.get("/api/flows/tflow/samples?node_id=first").want(200).decode(&entry)
	if len(entry.Items) != 2 || entry.Items[0].Source != "event" || *entry.Items[0].EventID != e2.ID || *entry.Items[1].EventID != e1.ID ||
		*entry.Items[0].SensorID != "src" || entry.Items[0].Payload["id"] != "e2" || entry.Items[0].NodeRunID != nil {
		t.Errorf("entry samples: %+v", entry.Items)
	}
	var limited samplesReply
	e.get("/api/flows/tflow/samples?node_id=first&limit=1").want(200).decode(&limited)
	if len(limited.Items) != 1 || *limited.Items[0].EventID != e2.ID {
		t.Errorf("limit: %+v", limited.Items)
	}

	// Downstream nodes: what `first` emitted on the port that leads to them, newest first; test runs are skipped.
	live := e.flowRun("tflow", e1.ID, store.KindLive, store.FlowRunSucceeded)
	live2 := e.flowRun("tflow", e2.ID, store.KindLive, store.FlowRunSucceeded)
	test := e.flowRun("tflow", e1.ID, store.KindTest, store.FlowRunSucceeded)
	emit := func(frID, stdout string) *store.NodeRun {
		return e.nodeRun(frID, "first", store.NodeDone, func(n *store.NodeRun) {
			n.Decision, n.Stdout = store.DecisionEmitted, store.Inline(stdout)
		})
	}
	n1 := emit(live.ID, `{"id":"e1","severity":"urgent","_port":"urgent"}`+"\n"+`{"id":"e1b","_port":"other"}`)
	n2 := emit(live2.ID, `{"id":"e2","_port":"other"}`)
	emit(test.ID, `{"id":"from-test","_port":"urgent"}`)
	e.nodeRun(live.ID, "first", store.NodeDone, func(n *store.NodeRun) { n.Decision = store.DecisionDropped })

	var urgent samplesReply
	e.get("/api/flows/tflow/samples?node_id=after-urgent").want(200).decode(&urgent)
	if len(urgent.Items) != 1 || urgent.Items[0].Source != "node_run" || *urgent.Items[0].NodeRunID != n1.ID || *urgent.Items[0].FromNodeID != "first" ||
		urgent.Items[0].EventID != nil || urgent.Items[0].Payload["id"] != "e1" || urgent.Items[0].Payload["_port"] != nil {
		t.Errorf("urgent samples: %+v", urgent.Items)
	}
	var other samplesReply
	e.get("/api/flows/tflow/samples?node_id=after-other").want(200).decode(&other)
	if len(other.Items) != 2 || *other.Items[0].NodeRunID != n2.ID || other.Items[0].Payload["id"] != "e2" || other.Items[1].Payload["id"] != "e1b" {
		t.Errorf("other samples: %+v", other.Items)
	}

	// Nothing to sample: a node that is not in the saved flow, or no runs yet.
	var none samplesReply
	e.get("/api/flows/tflow/samples?node_id=not-saved-yet").want(200).decode(&none)
	if none.Items == nil || len(none.Items) != 0 {
		t.Errorf("unknown node: %+v", none)
	}
	e.writeFlow("quiet", strings.Replace(samplesFlow, "id: tflow", "id: quiet", 1))
	e.get("/api/flows/quiet/samples?node_id=after-urgent").want(200).decode(&none)
	if len(none.Items) != 0 {
		t.Errorf("no runs: %+v", none)
	}

	e.get("/api/flows/missing/samples?node_id=first").want(404)
	e.get("/api/flows/tflow/samples").want(422)
	e.get("/api/flows/tflow/samples?node_id=first&limit=0").want(400)
	e.get("/api/flows/tflow/samples?node_id=first&limit=51").want(400)
	e.get("/api/flows/tflow/samples?node_id=bad%20id").want(400)
}

// A flow with transform nodes is validated and saved like any other: errors come back
// with the full path of the step field.
func TestValidateFlowWithTransformNode(t *testing.T) {
	e := newEnv(t)
	flow := `{"flow":{"id":"v","enabled":true,"subscribe":["demo-ticker"],"nodes":[
	  {"id":"a","type":"transform","steps":[{"type":"filter","conditions":[{"field":"x","op":"matches","value":"("}]},{"type":"set","field":"y","value":{"kind":"constant","value":1}}]},
	  {"id":"b","type":"transform","steps":[{"type":"route","rules":[{"port":"p","conditions":[{"field":"x","op":"exists"}]},{"port":"p","conditions":[{"field":"x","op":"exists"}]}]}]},
	  {"id":"c","type":"inbox","title":"t"}],
	  "edges":[{"from":"a","to":"b"},{"from":"b","port":"q","to":"c"}]}}`
	var v struct {
		Valid       bool              `json:"valid"`
		Diagnostics []spec.Diagnostic `json:"diagnostics"`
	}
	e.do(http.MethodPost, "/api/flows/validate", flow).want(200).decode(&v)
	got := map[string]string{}
	for _, d := range v.Diagnostics {
		got[d.Path] = d.Code
	}
	if v.Valid || got["nodes[0].steps[0].conditions[0].value"] != "invalid_regex" ||
		got["nodes[1].steps[0].rules[1].port"] != "duplicate_id" || got["edges[1].port"] != "undeclared_port" {
		t.Errorf("diagnostics: %+v", v.Diagnostics)
	}
}
