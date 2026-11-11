package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/spec"
)

// AI-assisted flow authoring end to end: the same session machinery as sensors with target kind flow, the scripted
// fake ACP agent, and the real hal9001 binary on the agent's PATH.

type assistFlowDraft struct {
	Kind    string  `json:"kind"`
	YAML    string  `json:"yaml"`
	Hash    string  `json:"hash"`
	FlowID  *string `json:"flow_id"`
	Sensor  *string `json:"sensor_id"`
	Prompts []struct {
		Name   string `json:"name"`
		YAML   string `json:"yaml"`
		Exists bool   `json:"exists"`
	} `json:"prompts"`
	Sensors []struct {
		ID          string           `json:"id"`
		YAML        string           `json:"yaml"`
		Exists      bool             `json:"exists"`
		BaseHash    *string          `json:"base_hash"`
		Diagnostics []map[string]any `json:"diagnostics"`
	} `json:"sensors"`
	Diagnostics []map[string]any `json:"diagnostics"`
	UpdatedBy   string           `json:"updated_by"`
}

type assistFlowDetail struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Error  *string `json:"error"`
	Target struct {
		Kind string  `json:"kind"`
		ID   *string `json:"id"`
	} `json:"target"`
	Draft assistFlowDraft `json:"draft"`
}

type flowTestNode struct {
	NodeID    string  `json:"node_id"`
	NodeRunID string  `json:"node_run_id"`
	Status    string  `json:"status"`
	Decision  *string `json:"decision"`
	Reason    *string `json:"reason"`
	Ports     []string
	Simulated bool `json:"simulated"`
	Outputs   []struct {
		Port    string         `json:"port"`
		Payload map[string]any `json:"payload"`
	} `json:"outputs"`
	WouldCreate *struct {
		Title    string `json:"title"`
		Priority string `json:"priority"`
	} `json:"would_create"`
}

type flowTestJSON struct {
	Kind      string         `json:"kind"`
	FlowRunID string         `json:"flow_run_id"`
	Status    string         `json:"status"`
	Input     map[string]any `json:"input"`
	Nodes     []flowTestNode `json:"nodes"`
	Edges     []struct {
		From string `json:"from"`
		To   string `json:"to"`
		Port string `json:"port"`
	} `json:"edges_taken"`
}

func (r flowTestJSON) node(id string) *flowTestNode {
	for i := range r.Nodes {
		if r.Nodes[i].NodeID == id {
			return &r.Nodes[i]
		}
	}
	return nil
}

func (d *Daemon) assistCreateFlow(id string) assistFlowDetail {
	d.t.Helper()
	target := map[string]any{"kind": "flow"}
	if id != "" {
		target["id"] = id
	}
	var s assistFlowDetail
	d.Post("/api/assist/sessions", map[string]any{"target": target, "runtime": "fake"}).Want(201).JSON(&s)
	return s
}

func (d *Daemon) assistFlowGet(id string) assistFlowDetail {
	d.t.Helper()
	var s assistFlowDetail
	d.Get("/api/assist/sessions/" + id).Want(200).JSON(&s)
	return s
}

// assistFlowTest runs POST /test as the user.
func (d *Daemon) assistFlowTest(id string, body map[string]any) flowTestJSON {
	d.t.Helper()
	var res flowTestJSON
	d.Post("/api/assist/sessions/"+id+"/test", body).Want(200).JSON(&res)
	if res.Kind != "flow" {
		d.t.Fatalf("test kind = %q", res.Kind)
	}
	return res
}

// assistWrite is the agent writing a draft file with draft_write (flow.yaml or prompts/<name>.md).
func assistWrite(id, rel, data string) step {
	return mcpStep(id, "draft_write", map[string]any{"path": rel, "content": data}, "")
}

const (
	triagePrompt = "---\nname: new-triage\ndescription: triage the item\noutput: note\n---\nRead {{.input.title}} and say whether it needs attention.\n"

	// Drops noise, sends urgent items to the inbox, hands the rest to an agent.
	urgentFlowYAML = `id: urgent-to-inbox
enabled: false
dry_run: true
subscribe: [src]
nodes:
  - id: triage
    type: transform
    steps:
      - type: filter
        conditions:
          - {field: severity, op: not_equals, value: noise}
      - type: route
        default_port: normal
        rules:
          - port: urgent
            conditions:
              - {field: severity, op: equals, value: urgent}
    ui: {x: 100, y: 100}
  - id: note
    type: inbox
    title: "Urgent: {{.input.title}}"
    priority: urgent
    summary_md: "{{.input.text}}"
    ui: {x: 400, y: 40}
  - id: look
    type: agent
    runtime: fake
    mode: readonly
    prompt: new-triage
    ui: {x: 400, y: 200}
edges:
  - {from: triage, port: urgent, to: note}
  - {from: triage, port: normal, to: look}
`
)

func srcSensor(d *Daemon) {
	d.IdleSensor("src")
}

// The agent drafts a flow with a transform filter and route, an inbox node and a new prompt. The draft shows up with
// its parsed id, the new prompt and no diagnostics; edits under reference/ are rejected; the user's test runs the
// flow on samples (transform decisions, inbox preview, agent node skipped) without creating anything; Save through
// the normal endpoints writes the prompt and the flow.
func TestAssistFlowAgentDraftsTestsAndIsSavedThroughTheNormalEndpoints(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.Env("GLOBAL_TOKEN=" + secretGlobalValue + "\n")
		d.Prompt("existing-prompt", "none", "Existing body.")
		d.Flow("other", "id: other\nenabled: false\ndry_run: true\nsubscribe: [src]\nnodes:\n  - id: keep\n    type: transform\n    steps:\n      - {type: remove, fields: [x]}\nedges: []\n")
		d.assistRuntime(false, turn{When: when("draft the flow$"), Steps: []step{
			say("Writing the prompt and the flow."),
			assistWrite("p1", "prompts/new-triage.md", triagePrompt),
			assistWrite("f1", "flow.yaml", urgentFlowYAML),
			assistWrite("r1", "reference/runtimes.txt", "hacked\n"),
			mcpStep("i1", "draft_read", map[string]any{"path": "instructions"}, "instructions.txt"),
			mcpStep("i2", "draft_read", map[string]any{"path": "reference/runtimes.txt"}, "runtimes-read.txt"),
			say("Done."),
		}})
	})
	otherBefore := d.Read("flows/other.yaml")
	// Two events: the newest comes first in the samples. The payload carries a secret that must be redacted.
	d.Inject("src", map[string]any{"id": "1", "title": "noise one", "severity": "noise", "text": "ignore"})
	d.Inject("src", map[string]any{"id": "2", "title": "Server down", "severity": "urgent", "text": "db is down " + secretGlobalValue})

	s := d.assistCreateFlow("")
	if s.Target.Kind != "flow" || s.Target.ID != nil || s.Draft.Kind != "flow" || s.Draft.FlowID == nil || *s.Draft.FlowID != "new-flow" ||
		s.Draft.Sensor != nil || len(s.Draft.Prompts) != 0 || s.Draft.UpdatedBy != "user" {
		t.Fatalf("new flow session = %+v", s)
	}
	if !strings.Contains(s.Draft.YAML, "enabled: false") || !strings.Contains(s.Draft.YAML, "dry_run: true") {
		t.Errorf("skeleton must be disabled and dry-run:\n%s", s.Draft.YAML)
	}

	// The draft directory: flow.yaml, prompts/ and read-only reference material.
	dir := d.assistDir(s.ID)
	for _, rel := range []string{
		"flow.yaml", "prompts", "reference/sensors/src.yaml", "reference/prompts/existing-prompt.md", "reference/runtimes.txt",
		"reference/samples/src.jsonl", "reference/samples/index.txt", "reference/docs/flows.md",
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("draft directory lacks %s: %v", rel, err)
		}
	}
	if rt, _ := os.ReadFile(filepath.Join(dir, "reference/runtimes.txt")); !strings.Contains(string(rt), "fake") || strings.Contains(string(rt), "command") {
		t.Errorf("runtimes.txt = %q, want names only", rt)
	}
	samples, _ := os.ReadFile(filepath.Join(dir, "reference/samples/src.jsonl"))
	if strings.Contains(string(samples), secretGlobalValue) || !strings.Contains(string(samples), "«GLOBAL_TOKEN»") {
		t.Errorf("samples must be redacted:\n%s", samples)
	}
	var samp struct {
		Items []struct {
			Index    int            `json:"index"`
			SensorID string         `json:"sensor_id"`
			Payload  map[string]any `json:"payload"`
		} `json:"items"`
	}
	d.Get("/api/assist/sessions/" + s.ID + "/samples").Want(200).JSON(&samp)
	if len(samp.Items) != 2 || samp.Items[0].SensorID != "src" || samp.Items[0].Payload["severity"] != "urgent" || samp.Items[1].Payload["severity"] != "noise" {
		t.Fatalf("samples = %+v", samp.Items)
	}

	got := d.assistTurn(s.ID, "draft the flow")
	if got.Status != "idle" {
		t.Fatalf("turn status %q error %v", got.Status, got.Error)
	}
	f := d.assistFlowGet(s.ID)
	if f.Draft.YAML != urgentFlowYAML || f.Draft.UpdatedBy != "agent" || f.Draft.FlowID == nil || *f.Draft.FlowID != "urgent-to-inbox" {
		t.Fatalf("draft = %+v", f.Draft)
	}
	if len(f.Draft.Prompts) != 1 || f.Draft.Prompts[0].Name != "new-triage" || f.Draft.Prompts[0].Exists || f.Draft.Prompts[0].YAML != triagePrompt {
		t.Errorf("draft prompts = %+v", f.Draft.Prompts)
	}
	for _, dg := range f.Draft.Diagnostics {
		if dg["severity"] == "error" {
			t.Errorf("unexpected diagnostic %v (the draft prompt must satisfy the agent node)", dg)
		}
	}
	// The write under reference/ failed as a tool error and the file is untouched; the others went through.
	run := d.assistRuns(s.ID)[0]
	if w := d.halPerms(run.ID, "draft_write"); len(w) != 3 {
		t.Errorf("draft_write records = %+v", w)
	}
	if tr := d.Transcript(run.ID); !strings.Contains(tr, "mcp[r1] is_error=true") || !strings.Contains(tr, "mcp[p1] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
	if rt, _ := os.ReadFile(filepath.Join(dir, "reference/runtimes.txt")); strings.Contains(string(rt), "hacked") {
		t.Error("the reference file was edited")
	}
	if rt, _ := os.ReadFile(filepath.Join(dir, "runtimes-read.txt")); !strings.Contains(string(rt), "fake") {
		t.Errorf("draft_read of the reference = %q", rt)
	}
	instr, _ := os.ReadFile(filepath.Join(dir, "instructions.txt"))
	for _, want := range []string{"PREFER `transform`", "draft_test", "`sample`", "enabled: false", "reference/", "mode: readonly", "output: note", "NEVER enable"} {
		if !strings.Contains(string(instr), want) {
			t.Errorf("the instructions lack %q", want)
		}
	}
	if n := len(d.pendingApprovals()); n != 0 {
		t.Errorf("%d approvals pending", n)
	}

	// Test on the urgent sample: the transform routes to the inbox preview; nothing is created.
	res := d.assistFlowTest(s.ID, map[string]any{"sample_index": 0})
	if res.Status != "succeeded" || res.Input["severity"] != "urgent" {
		t.Fatalf("test result = %+v", res)
	}
	tr := res.node("triage")
	if tr == nil || tr.Status != "done" || tr.Decision == nil || *tr.Decision != "emitted" || len(tr.Outputs) != 1 || tr.Outputs[0].Port != "urgent" {
		t.Fatalf("triage = %+v", tr)
	}
	if n := res.node("note"); n == nil || n.WouldCreate == nil || n.WouldCreate.Title != "Urgent: Server down" || n.WouldCreate.Priority != "urgent" || n.Status != "done" {
		t.Errorf("note = %+v", n)
	}
	if res.node("look") != nil {
		t.Error("the agent branch must not run for an urgent item")
	}
	if len(res.Edges) != 1 || res.Edges[0].From != "triage" || res.Edges[0].To != "note" || res.Edges[0].Port != "urgent" {
		t.Errorf("edges taken = %+v", res.Edges)
	}
	var inbox struct {
		Items []map[string]any `json:"items"`
	}
	d.Get("/api/inbox").Want(200).JSON(&inbox)
	if len(inbox.Items) != 0 {
		t.Errorf("a flow test created inbox items: %v", inbox.Items)
	}
	// It is a kind=test flow run, hidden from the default run lists.
	run1 := d.GetRun(res.FlowRunID, 0).FlowRun
	if run1.Kind != "test" || !run1.DryRun || run1.FlowID != "urgent-to-inbox" {
		t.Errorf("flow run = %+v", run1)
	}
	var runs struct {
		Items []map[string]any `json:"items"`
	}
	d.Get("/api/flow-runs?flow_id=urgent-to-inbox").Want(200).JSON(&runs)
	if len(runs.Items) != 0 {
		t.Errorf("test runs listed by default: %v", runs.Items)
	}

	// Noise is dropped with a reason; a normal item reaches the agent node, which is skipped.
	res = d.assistFlowTest(s.ID, map[string]any{"sample_index": 1})
	if tr := res.node("triage"); tr == nil || tr.Decision == nil || *tr.Decision != "dropped" || tr.Reason == nil || !strings.Contains(*tr.Reason, "severity") {
		t.Errorf("dropped triage = %+v", tr)
	}
	res = d.assistFlowTest(s.ID, map[string]any{"input": map[string]any{"id": "3", "title": "fyi", "severity": "info", "text": "x"}})
	look := res.node("look")
	if look == nil || look.Status != "skipped" || look.Reason == nil || *look.Reason != "assist test: agent nodes are not run" || len(look.Outputs) != 0 {
		t.Errorf("agent node = %+v", look)
	}
	if res.node("note") != nil {
		t.Error("a normal item must not reach the urgent inbox node")
	}
	// The agent's own CLI call works against the same endpoint (no command nodes: no approval).
	out, _, code := d.CLI("", "assist", "test", "--sample", "0", s.ID)
	var cli flowTestJSON
	if code != 0 || json.Unmarshal([]byte(out), &cli) != nil || cli.Kind != "flow" || cli.node("note") == nil {
		t.Errorf("assist test --sample 0: code %d out %s", code, out)
	}
	// Bad requests.
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"sample_index": 9}).Want(422)
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"sample_index": 0, "event_id": "01JC7Z3K9Q8V5T2M4N6P8R0S1T"}).Want(422)

	// Nothing real changed yet: the existing flow is as it was, no flow or prompt of the draft exists.
	if d.Read("flows/other.yaml") != otherBefore {
		t.Error("an existing flow changed before Save")
	}
	d.Get("/api/flows/urgent-to-inbox").Want(404)
	d.Get("/api/prompts/new-triage").Want(404)

	// Save, as the UI does: the new prompt first, then the flow.
	d.Post("/api/prompts", map[string]any{"markdown": f.Draft.Prompts[0].YAML}).Want(201)
	d.Post("/api/flows", map[string]any{"yaml": f.Draft.YAML}).Want(201)
	if d.Read("prompts/new-triage.md") != string(spec.DropLegacyName([]byte(triagePrompt))) || d.Read("flows/urgent-to-inbox.yaml") == "" {
		t.Error("Save did not write the prompt and the flow")
	}
	var saved struct {
		Enabled bool `json:"enabled"`
	}
	d.Get("/api/flows/urgent-to-inbox").Want(200).JSON(&saved)
	if saved.Enabled {
		t.Error("the saved flow is enabled")
	}
	// Saving the new flow makes the session an edit session on it.
	if got := d.assistFlowGet(s.ID); got.Target.ID == nil || *got.Target.ID != "urgent-to-inbox" {
		t.Errorf("target after Save = %+v", got.Target)
	}
	// The saved prompt now exists: the draft marks it so and flags the name clash.
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": f.Draft.YAML, "expected_hash": f.Draft.Hash}).Want(200)
	f = d.assistFlowGet(s.ID)
	if len(f.Draft.Prompts) != 1 || !f.Draft.Prompts[0].Exists {
		t.Errorf("after Save the draft prompt must show exists: %+v", f.Draft.Prompts)
	}
	for _, dg := range f.Draft.Diagnostics {
		if dg["severity"] != nil && strings.Contains(fmt.Sprint(dg["message"]), "already saved") {
			t.Errorf("the prompt Save just created must not be flagged as a clash: %v", dg)
		}
	}
	d.assertNoSecret(s.ID, secretGlobalValue)
}

// Command nodes run with the user's credentials, so the agent's test of a changed command node needs the user's
// approval (hash over lang, script, command, cwd and referenced env names), while a transform-only flow needs none.
func TestAssistFlowAgentTestOfCommandNodesNeedsApprovalTransformOnlyDoesNot(t *testing.T) {
	cmdFlow := `id: with-command
enabled: false
dry_run: true
subscribe: [src]
nodes:
  - id: fetch
    type: command
    lang: bash
    ports: [out]
    script: |
      jq -c '. + {token_seen: (env.GLOBAL_TOKEN != null)}'
      echo "using $GLOBAL_TOKEN" >&2
    ui: {x: 100, y: 100}
  - id: note
    type: inbox
    title: "Item {{.input.id}}"
    ui: {x: 400, y: 100}
edges:
  - {from: fetch, to: note}
`
	cmdFlow2 := strings.Replace(cmdFlow, "echo \"using $GLOBAL_TOKEN\" >&2", "echo \"now using $GLOBAL_TOKEN\" >&2", 1)
	transformFlow := `id: only-transform
enabled: false
dry_run: true
subscribe: [src]
nodes:
  - id: keep
    type: transform
    steps:
      - {type: pick, fields: [id]}
    ui: {x: 100, y: 100}
edges: []
`
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.Env("GLOBAL_TOKEN=" + secretGlobalValue + "\n")
		d.assistRuntime(false,
			turn{When: when("command flow$"), Steps: []step{
				assistWrite("f1", "flow.yaml", cmdFlow),
				mcpStep("t1", "draft_test", map[string]any{"sample": 0}, "test-result.json"),
				say("Tested."),
			}},
			turn{When: when("same command again$"), Steps: []step{
				mcpStep("t2", "draft_test", map[string]any{"sample": 0}, "test-result2.json"),
				say("Tested again."),
			}},
			turn{When: when("changed command$"), Steps: []step{
				assistWrite("f2", "flow.yaml", cmdFlow2),
				mcpStep("t3", "draft_test", map[string]any{"sample": 0}, "test-result3.json"),
				say("Tested the change."),
			}},
			turn{When: when("transform only$"), Steps: []step{
				assistWrite("f3", "flow.yaml", transformFlow),
				mcpStep("t4", "draft_test", map[string]any{"sample": 0}, "test-result4.json"),
				say("Tested the transform flow."),
			}},
		)
	})
	d.Inject("src", map[string]any{"id": "7", "title": "hello"})
	s := d.assistCreateFlow("")
	dir := d.assistDir(s.ID)
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return b
	}

	// A new command node: the agent's test asks, the user allows, the result is redacted.
	d.assistSend(s.ID, "command flow", nil)
	ap := d.waitPendingApproval()
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}
	var res flowTestJSON
	if err := json.Unmarshal(read("test-result.json"), &res); err != nil {
		t.Fatalf("test-result.json: %v: %s", err, read("test-result.json"))
	}
	fetch := res.node("fetch")
	if res.Kind != "flow" || fetch == nil || fetch.Status != "done" || len(fetch.Outputs) != 1 || fetch.Outputs[0].Payload["token_seen"] != true {
		t.Fatalf("result = %s", read("test-result.json"))
	}
	if strings.Contains(string(read("test-result.json")), secretGlobalValue) || !strings.Contains(string(read("test-result.json")), "«GLOBAL_TOKEN»") {
		t.Errorf("the result must show the redacted secret: %s", read("test-result.json"))
	}
	if n := res.node("note"); n == nil || n.WouldCreate == nil || n.WouldCreate.Title != "Item 7" {
		t.Errorf("note = %+v", n)
	}
	// The approval showed the command nodes.
	if desc, _ := json.Marshal(ap); !strings.Contains(string(desc), "draft_test") {
		t.Errorf("approval = %s", desc)
	}

	// The same command nodes again: no approval.
	d.assistSend(s.ID, "same command again", nil)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" || len(d.pendingApprovals()) != 0 {
		t.Fatalf("same code asked again: status %q, pending %v", got.Status, d.pendingApprovals())
	}
	read("test-result2.json")

	// Changing the script asks again.
	d.assistSend(s.ID, "changed command", nil)
	ap = d.waitPendingApproval()
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" {
		t.Fatalf("status %q error %v", got.Status, got.Error)
	}

	// A transform-only flow never asks.
	d.assistSend(s.ID, "transform only", nil)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" || len(d.pendingApprovals()) != 0 {
		t.Fatalf("transform-only asked: status %q, pending %v", got.Status, d.pendingApprovals())
	}
	var res4 flowTestJSON
	if err := json.Unmarshal(read("test-result4.json"), &res4); err != nil || res4.node("keep") == nil || res4.node("keep").Status != "done" {
		t.Errorf("transform result = %s (%v)", read("test-result4.json"), err)
	}
	d.assertNoSecret(s.ID, secretGlobalValue)
}

// A session on an existing flow starts from its file, leaves it untouched while the agent edits, and an invalid draft
// is a 422 on test with the diagnostics.
func TestAssistFlowOnAnExistingFlowAndInvalidDrafts(t *testing.T) {
	existing := "id: mine\nenabled: true\ndry_run: false\nsubscribe: [src]\nnodes:\n  - id: keep\n    type: transform\n    steps:\n      - {type: pick, fields: [id]}\nedges: []\n"
	edited := strings.Replace(existing, "enabled: true", "enabled: false", 1)
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.Flow("mine", existing)
		d.Prompt("taken", "none", "Saved body.")
		d.assistRuntime(false, turn{When: when("tweak$"), Steps: []step{
			assistWrite("p1", "prompts/taken.md", "---\nname: taken\ndescription: x\n---\nDifferent body.\n"),
			assistWrite("f1", "flow.yaml", edited), say("Done."),
		}})
	})
	var before struct {
		Hash string `json:"hash"`
	}
	d.Get("/api/flows/mine").Want(200).JSON(&before)
	s := d.assistCreateFlow("mine")
	if s.Target.ID == nil || *s.Target.ID != "mine" || s.Draft.YAML != existing || s.Draft.Hash != before.Hash {
		t.Fatalf("session = %+v", s)
	}
	d.assistTurn(s.ID, "tweak")
	if d.Read("flows/mine.yaml") != existing {
		t.Error("the flow file changed before Save")
	}
	got := d.assistFlowGet(s.ID)
	if got.Draft.YAML != edited {
		t.Errorf("draft = %q", got.Draft.YAML)
	}
	// A draft prompt that reuses the name of a saved one with other content is flagged: Save never overwrites it.
	clash := false
	for _, dg := range got.Draft.Diagnostics {
		clash = clash || strings.Contains(fmt.Sprint(dg["message"]), `"taken" is already saved`)
	}
	if len(got.Draft.Prompts) != 1 || !got.Draft.Prompts[0].Exists || !clash {
		t.Errorf("prompts %+v diagnostics %v", got.Draft.Prompts, got.Draft.Diagnostics)
	}

	// A draft that does not validate: 422 with diagnostics, nothing runs.
	bad := "id: mine\nenabled: false\nsubscribe: [no-such-sensor]\nnodes:\n  - id: a\n    type: agent\n    runtime: fake\n    mode: readonly\n    prompt: missing-prompt\nedges: []\n"
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": bad}).Want(200)
	r := d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{"input": map[string]any{"id": 1}}).Want(422)
	if body := string(r.Body); !strings.Contains(body, "no-such-sensor") && !strings.Contains(body, "missing-prompt") {
		t.Errorf("422 body = %s", body)
	}
	// No input available: 422 as well.
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": edited}).Want(200)
	d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{}).Want(422)
}

// "Test flow" works while the kill switch is engaged, even with "pause and kill running": the test's nodes run, while
// live flow runs of the same flow stay queued.
func TestAssistFlowTestRunsWhilePaused(t *testing.T) {
	const flow = "id: paused-test\nenabled: false\ndry_run: true\nsubscribe: [src]\nnodes:\n  - id: keep\n    type: transform\n    steps:\n      - {type: remove, fields: [x]}\nedges: []\n"
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.assistRuntime(false, turn{Steps: []step{say("ok")}})
	})
	s := d.assistCreateFlow("")
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": flow}).Want(200)
	d.Post("/api/killswitch/pause", map[string]any{"kill_running": true}).Want(200)
	res := d.assistFlowTest(s.ID, map[string]any{"input": map[string]any{"id": "1", "x": "y", "title": "t"}})
	n := res.node("keep")
	if res.Status != "succeeded" || n == nil || n.Status != "done" {
		t.Fatalf("test while paused = %+v", res)
	}
	if got := d.assistTurn(s.ID, "hi"); got.Status != "idle" {
		t.Errorf("turn while paused: %+v", got)
	}
}
