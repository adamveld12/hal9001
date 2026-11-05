package e2e

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// jsonOf decodes s (CLI stdout or an API body) into a generic value so that two
// renderings of the same resource compare equal regardless of indentation.
func jsonOf(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

// cliJSON runs the CLI, requires exit code 0 and returns its stdout as JSON.
func (d *Daemon) cliJSON(stdin string, args ...string) any {
	d.t.Helper()
	out, errOut, code := d.CLI(stdin, args...)
	if code != 0 {
		d.t.Fatalf("hal9001 %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errOut)
	}
	return jsonOf(d.t, out)
}

// sameAsAPI fails unless the CLI output v equals the API response body for path.
func (d *Daemon) sameAsAPI(what string, v any, path string) {
	d.t.Helper()
	api := jsonOf(d.t, string(d.Get(path).Want(200).Body))
	if !reflect.DeepEqual(v, api) {
		got, _ := json.MarshalIndent(v, "", " ")
		want, _ := json.MarshalIndent(api, "", " ")
		d.t.Errorf("%s: CLI output differs from GET %s\nCLI: %s\nAPI: %s", what, path, got, want)
	}
}

// Scenario 20: the CLI is a thin client of the REST API (REQUIREMENTS §10.2),
// so a complete path (inject, wait, inbox, approval, killswitch, status) can be
// driven with the CLI alone, its JSON output equals the API's for the same
// resource, and errors exit non-zero with the problem+json on stderr.
func TestScenario20CLIMirrorsAPI(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", script{LoadSession: true, Turns: []turn{
		{When: inMode("default"), Steps: []step{say("Plan: create hello.txt")}},
		{When: inMode("bypass"), Steps: []step{
			{Op: fakeagent.OpWriteFile, Path: "hello.txt", Data: "hi\n"},
			say("executed"),
		}},
	}})
	d.Flow("f", agentFlow("f", "src", "approve", "    approve: proposal"))
	d.Start()

	// events inject <sensor> reads the item from stdin and prints the API's inject result.
	inj, ok := d.cliJSON(`{"id":"42"}`, "events", "inject", "src").(map[string]any)
	if !ok {
		t.Fatal("events inject: output is not an object")
	}
	runs, _ := inj["flow_runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("events inject: flow_runs = %v", inj["flow_runs"])
	}
	runID, _ := runs[0].(map[string]any)["id"].(string)
	eventID, _ := inj["event"].(map[string]any)["id"].(string)
	if runID == "" || eventID == "" {
		t.Fatalf("events inject: no run or event id in %v", inj)
	}

	// runs get --wait returns once the run awaits approval; its output is the API's.
	parked := d.cliJSON("", "runs", "get", runID, "--wait")
	if st, _ := parked.(map[string]any)["flow_run"].(map[string]any)["status"].(string); st != "awaiting_approval" {
		t.Fatalf("runs get --wait: status %q, want awaiting_approval: %v", st, parked)
	}
	d.sameAsAPI("parked run", parked, "/api/flow-runs/"+runID)

	// approvals list shows the proposal; the CLI filter equals the API filter.
	pending := d.cliJSON("", "approvals", "list", "--status", "pending")
	d.sameAsAPI("pending approvals", pending, "/api/approvals?status=pending")
	ap := d.WaitApproval("proposal")
	if !strings.Contains(ap.ProposalMD, "Plan: create hello.txt") {
		t.Errorf("proposal text: %q", ap.ProposalMD)
	}
	if _, err := os.Stat(d.Path("data/state/f/work/hello.txt")); err == nil {
		t.Fatal("the plan ran before approval")
	}
	if st := d.GetStatus(); st.PendingApprovals != 1 {
		t.Errorf("pending approvals in status: %d", st.PendingApprovals)
	}
	d.sameAsAPI("status", d.cliJSON("", "status"), "/api/status")

	// approvals approve --comment resumes the agent; the run then succeeds.
	approved, _ := d.cliJSON("", "approvals", "approve", ap.ID, "--comment", "go ahead").(map[string]any)
	if approved["status"] != "approved" || approved["comment"] != "go ahead" {
		t.Errorf("approvals approve: %v", approved)
	}
	done := d.cliJSON("", "runs", "get", runID, "--wait")
	if st, _ := done.(map[string]any)["flow_run"].(map[string]any)["status"].(string); st != "succeeded" {
		t.Fatalf("runs get --wait after approval: status %q: %v", st, done)
	}
	d.sameAsAPI("finished run", done, "/api/flow-runs/"+runID)
	d.sameAsAPI("event", d.cliJSON("", "events", "get", eventID), "/api/events/"+eventID)
	if b, err := os.ReadFile(d.Path("data/state/f/work/hello.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("approved plan not executed: %q %v", b, err)
	}
	d.sameAsAPI("approval after decision", d.cliJSON("", "approvals", "get", ap.ID), "/api/approvals/"+ap.ID)

	// inbox list equals the API's and holds the item the flow produced.
	inbox := d.cliJSON("", "inbox", "list")
	d.sameAsAPI("inbox", inbox, "/api/inbox")
	if items := d.Inbox(); len(items) != 1 || items[0].Title != "done 42" {
		t.Errorf("inbox: %+v", items)
	}

	// killswitch pause / resume change the state the API reports.
	paused, _ := d.cliJSON("", "killswitch", "pause", "--reason", "cli test").(map[string]any)
	if paused["paused"] != true || paused["reason"] != "cli test" {
		t.Errorf("killswitch pause: %v", paused)
	}
	if ks := d.GetStatus().KillSwitch; !ks.Paused || ks.Reason != "cli test" {
		t.Errorf("API kill switch after CLI pause: %+v", ks)
	}
	d.sameAsAPI("paused killswitch", d.cliJSON("", "killswitch", "get"), "/api/killswitch")
	resumed, _ := d.cliJSON("", "killswitch", "resume").(map[string]any)
	if resumed["paused"] != false || d.GetStatus().KillSwitch.Paused {
		t.Errorf("killswitch resume: %v", resumed)
	}

	// Errors: non-zero exit, nothing on stdout, the problem+json on stderr.
	out, errOut, code := d.CLI("", "runs", "get", "does-not-exist")
	if code == 0 || out != "" {
		t.Fatalf("unknown run: exit %d stdout %q", code, out)
	}
	var problem struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal([]byte(errOut), &problem); err != nil {
		t.Fatalf("stderr is not problem+json: %v\n%s", err, errOut)
	}
	api := d.Get("/api/flow-runs/does-not-exist").Want(404)
	if problem.Status != 404 || problem.Type == "" || problem.Type != "urn:hal9001:problem:"+api.Problem() {
		t.Errorf("stderr problem = %+v, API problem = %s", problem, api.Problem())
	}
	if _, _, code := d.CLI("not json", "events", "inject", "src"); code == 0 {
		t.Error("events inject accepted input that is not JSON")
	}
	if _, _, code := d.CLI("", "no-such-command"); code == 0 {
		t.Error("an unknown command exited 0")
	}
}

// The seeded demo (content/sensors/demo-ticker.yaml and content/flows/demo.yaml,
// unchanged) works out of the box: the first scheduled poll creates 3 events,
// the `noise` item is dropped, and the other two land in the inbox with the
// priorities the flow assigns.
func TestSeededDemoFlow(t *testing.T) {
	d := newDaemon(t, withDemo())
	d.Start()

	d.Eventually("the first scheduled demo-ticker poll to create 3 events and the flow to route them", func() bool {
		return len(d.Events("demo-ticker")) == 3 && len(d.Inbox()) == 2
	})
	events := d.Events("demo-ticker")
	if len(events) != 3 {
		t.Fatalf("events: %d, want 3", len(events))
	}
	items := d.InboxByTitle()
	urgent, normal := items["Urgent: Production deploy is failing"], items["Docs PR is ready for review"]
	if len(items) != 2 || urgent.Priority != "urgent" || normal.Priority != "normal" {
		t.Fatalf("inbox: %+v", items)
	}
	if !strings.Contains(urgent.SummaryMD, "rolled back twice") {
		t.Errorf("urgent summary: %q", urgent.SummaryMD)
	}

	// The newsletter event was dropped by drop-noise and never reached a note.
	var noise Event
	for _, e := range events {
		if strings.Contains(string(e.Payload), `"demo-3"`) {
			noise = e
		}
	}
	if noise.ID == "" {
		t.Fatal("no event for the noise item")
	}
	var detail struct {
		FanOut []struct {
			FlowRun FlowRun `json:"flow_run"`
		} `json:"fan_out"`
	}
	d.Get("/api/events/" + noise.ID).Want(200).JSON(&detail)
	if len(detail.FanOut) != 1 {
		t.Fatalf("noise event fan-out: %+v", detail.FanOut)
	}
	run := d.WaitRun(detail.FanOut[0].FlowRun.ID, "succeeded")
	if n := d.Node(run, "drop-noise"); n.Decision != "dropped" || n.Reason == "" || len(run.NodeRuns) != 1 {
		t.Errorf("noise item:\n%s", describeRun(run))
	}
}
