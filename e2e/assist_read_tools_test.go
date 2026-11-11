package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The assist agent can read everything the CLI can inspect, without approval, and can build a GitHub issue link;
// anything that changes something is still gated. The scripted fake agent calls the tools through hal's real MCP
// server, the way a runtime does.

const brokenFlowYAML = `id: broken
enabled: true
subscribe: [src]
nodes:
  - id: explode
    type: command
    lang: bash
    script: |
      echo "boom: the upstream API said no" >&2
      exit 3
`

func (d *Daemon) assistFile(sessionID, name string) string {
	d.t.Helper()
	b, err := os.ReadFile(filepath.Join(d.assistDir(sessionID), name))
	if err != nil {
		d.t.Fatalf("the agent's call did not write %s: %v", name, err)
	}
	return string(b)
}

func TestAssistReadsRunsAndBuildsIssueLinksWithoutApproval(t *testing.T) {
	d := newDaemon(t)
	d.InstallCLI()
	d.IdleSensor("src")
	d.Flow("broken", brokenFlowYAML)
	d.Start()

	// Something to debug: a flow run whose command node fails.
	run := d.WaitRun(d.InjectRun("src", map[string]any{"id": "7", "title": "deploy failing"}), "failed")
	node := d.Node(run, "explode")

	// Restart with the agent's script, which can now name the ids it will look at.
	d.assistRuntime(false, turn{When: when("why$"), Steps: []step{
		mcpStep("r1", "flow_runs_list", map[string]any{"flow_id": "broken", "status": "failed", "limit": 5}, "runs.json"),
		mcpStep("r2", "flow_run_get", map[string]any{"id": run.FlowRun.ID}, "run.json"),
		mcpStep("r3", "node_run_get", map[string]any{"id": node.ID}, "node.json"),
		mcpStep("r4", "events_list", map[string]any{"sensor_id": "src", "decision": "error"}, "events.json"),
		mcpStep("r5", "flow_get", map[string]any{"id": "broken"}, "flow.json"),
		mcpStep("r6", "sensor_runs_list", map[string]any{"id": "src"}, "sensor-runs.json"),
		mcpStep("r7", "settings_get", map[string]any{}, "settings.json"),
		mcpStep("r8", "agent_runs_list", map[string]any{"flow_run_id": run.FlowRun.ID}, "agent-runs.json"),
		mcpStep("r9", "status", map[string]any{}, "status.json"),
		mcpStep("r10", "github_issue_link", map[string]any{
			"title":       "Flow \"broken\" fails with exit 3 on every run",
			"body":        "Steps:\n1. Inject an event into `src`\n2. Run fails at node `explode`\n\nExpected: success. Actual: exit 3.",
			"labels":      []string{"bug"},
			"diagnostics": true,
			"refs":        map[string]any{"flow_id": "broken", "flow_run_id": run.FlowRun.ID},
		}, "issue.json"),
		mcpStep("r11", "flow_run_get", map[string]any{"id": "../../api/settings"}, "bad.json"),
		say("The flow fails at explode with exit 3."),
	}})
	d.Restart()
	s := d.assistCreate("")
	got := d.assistTurn(s.ID, "why")
	if got.Status != "idle" {
		t.Fatalf("status %q, error %v", got.Status, got.Error)
	}

	// Every read returned data about this installation.
	var runs struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "runs.json")), &runs); err != nil || len(runs.Items) != 1 || runs.Items[0].ID != run.FlowRun.ID {
		t.Errorf("flow_runs_list = %v, %+v", err, runs)
	}
	if b := d.assistFile(s.ID, "run.json"); !strings.Contains(b, node.ID) || !strings.Contains(b, "exit_nonzero") {
		t.Errorf("flow_run_get lacks the failed node run:\n%s", b)
	}
	if b := d.assistFile(s.ID, "node.json"); !strings.Contains(b, "boom: the upstream API said no") {
		t.Errorf("node_run_get lacks the node's stderr:\n%s", b)
	}
	if b := d.assistFile(s.ID, "events.json"); !strings.Contains(b, run.FlowRun.EventID) {
		t.Errorf("events_list lacks the event:\n%s", b)
	}
	if b := d.assistFile(s.ID, "flow.json"); !strings.Contains(b, "explode") {
		t.Errorf("flow_get lacks the definition:\n%s", b)
	}
	for _, f := range []string{"sensor-runs.json", "settings.json", "agent-runs.json", "status.json"} {
		if !json.Valid([]byte(d.assistFile(s.ID, f))) {
			t.Errorf("%s is not JSON", f)
		}
	}
	if fileExists(filepath.Join(d.assistDir(s.ID), "bad.json")) {
		t.Error("a path-shaped id was accepted")
	}

	// The issue link is built, not created.
	var link struct {
		URL       string `json:"url"`
		Repo      string `json:"repo"`
		Body      string `json:"body"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(d.assistFile(s.ID, "issue.json")), &link); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link.URL)
	if err != nil || u.Host != "github.com" || u.Path != "/adamveld12/hal9001/issues/new" || link.Repo != "adamveld12/hal9001" || link.Truncated {
		t.Fatalf("link = %+v (%v)", link, err)
	}
	q := u.Query()
	if q.Get("title") != "Flow \"broken\" fails with exit 3 on every run" || q.Get("labels") != "bug" {
		t.Errorf("link query = %v", q)
	}
	body := q.Get("body")
	for _, want := range []string{"Steps:\n1. Inject an event", "<summary>Diagnostics</summary>", "hal9001: e2e", "Flow: `broken`", "Flow run: `" + run.FlowRun.ID + "`"} {
		if !strings.Contains(body, want) {
			t.Errorf("issue body lacks %q:\n%s", want, body)
		}
	}
	if body != link.Body {
		t.Error("an untruncated link carries the whole body")
	}

	// None of it asked anybody, and each call is on the agent run's record as allowed.
	if ap := d.pendingApprovals(); len(ap) != 0 {
		t.Errorf("pending approvals after reads: %v", ap)
	}
	turnRun := d.assistRuns(s.ID)[0]
	for _, tool := range []string{"flow_runs_list", "node_run_get", "github_issue_link"} {
		ps := d.halPerms(turnRun.ID, tool)
		if len(ps) != 1 || decisionOf(ps[0]) != "allow" || ps[0].ApprovalID != nil {
			t.Errorf("%s records = %+v", tool, ps)
		}
	}
	// flow_run_get ran once, and the call with a path-shaped id was refused by the tool's own policy, not run.
	if ps := d.halPerms(turnRun.ID, "flow_run_get"); len(ps) != 2 || decisionOf(ps[0]) != "allow" || decisionOf(ps[1]) != "reject" || ps[1].ApprovalID != nil {
		t.Errorf("flow_run_get records = %+v", ps)
	}

	// The tools are listed to the agent (the context block names them), and no tool that acts is among them (request_approval only asks).
	prompt := turnRun.RenderedPrompt
	for _, tool := range []string{"flow_run_get", "node_run_get", "agent_run_events", "agent_run_sandbox_log", "events_list", "settings_get", "github_issue_link", "draft_write", "request_approval"} {
		if !strings.Contains(prompt, "- "+tool+":") {
			t.Errorf("the context block does not list %s", tool)
		}
	}
	for _, tool := range []string{"flow_run", "sensor_poll", "flow_create", "send_note"} {
		if strings.Contains(prompt, "- "+tool+":") {
			t.Errorf("the context block lists the acting tool %s", tool)
		}
	}
}

func TestAssistInstructionsTeachDebuggingAndIssueReports(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("learn$"), Steps: []step{
			mcpStep("i1", "draft_read", map[string]any{"path": "instructions"}, "instructions.md"),
			say("read"),
		}})
	})
	s := d.assistCreate("")
	d.assistTurn(s.ID, "learn")
	text := d.assistFile(s.ID, "instructions.md")
	for _, want := range []string{"Looking at what hal9001 did", "Why did my flow fail", "flow_run_get", "agent_run_events", "github_issue_link", "steps to reproduce", "never put secret values"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Errorf("instructions do not mention %q", want)
		}
	}
}

func TestAssistWritesStillNeedApprovalNextToReads(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.assistRuntime(false, turn{When: when("both$"), Steps: []step{
			mcpStep("r1", "flows_list", map[string]any{}, "flows.json"),
			assistEdit(scriptYAML(1, "5m")),
			mcpStep("t1", "draft_test", map[string]any{}, "test.json"), // a changed script: asks
			mcpStep("r2", "status", map[string]any{}, "status.json"),
			say("done"),
		}})
	})
	s := d.assistCreate("")
	d.assistSend(s.ID, "both", nil)

	ap := d.waitPendingApproval()
	if ap["type"] != "tool_call" || ap["assist_session_id"] != s.ID {
		t.Fatalf("approval = %v", ap)
	}
	if !fileExists(filepath.Join(d.assistDir(s.ID), "flows.json")) {
		t.Error("the read before the gated call did not run")
	}
	if fileExists(filepath.Join(d.assistDir(s.ID), "test.json")) || fileExists(filepath.Join(d.assistDir(s.ID), "status.json")) {
		t.Error("the turn went past the gated call before it was decided")
	}
	d.assistWait(s.ID, "awaiting_approval")
	d.Post("/api/approvals/"+ap["id"].(string)+"/approve", map[string]any{}).Want(200)
	if got := d.assistWait(s.ID, "idle", "failed"); got.Status != "idle" {
		t.Fatalf("status %q, error %v", got.Status, got.Error)
	}
	for _, f := range []string{"test.json", "status.json"} {
		if !fileExists(filepath.Join(d.assistDir(s.ID), f)) {
			t.Errorf("%s was not written after the approval", f)
		}
	}
	run := d.assistRuns(s.ID)[0]
	if ps := d.halPerms(run.ID, "draft_test"); len(ps) != 1 || ps[0].ApprovalID == nil {
		t.Errorf("draft_test records = %+v", ps)
	}
	for _, tool := range []string{"flows_list", "status"} {
		if ps := d.halPerms(run.ID, tool); len(ps) != 1 || ps[0].ApprovalID != nil || decisionOf(ps[0]) != "allow" {
			t.Errorf("%s records = %+v", tool, ps)
		}
	}
}
