package e2e

import (
	"os"
	"strings"
	"testing"
)

// gateFlowYAML is a flow with a gated command node `act`: when approved it runs (and leaves a mark in the node's
// state dir) and `after` follows; when denied `refused` follows. HEAD is inserted after the flow's enabled line, ACT
// holds extra lines of node act, EDGES the denied edge (empty for an unwired denied port).
func gateFlowYAML(head, act string, denied bool) string {
	y := `id: g
enabled: true
` + head + `subscribe: [src]
nodes:
  - id: act
    type: command
    lang: bash
    script: |
      cat > /dev/null
      echo ran >> "$HAL_STATE_DIR/ran"
      echo '{"ran": true}'
    requires_approval: true
` + act + `  - {id: after, type: inbox, title: "after {{.input.ran}}"}
`
	if denied {
		y += `  - {id: refused, type: inbox, title: "refused {{.input.input.id}}", summary_md: "{{.input.approval.decision}}|{{.input.approval.comment}}|{{.input.approval.approval_id}}|{{.input.approval.decided_by}}"}
`
	}
	y += "edges:\n  - {from: act, to: after}\n"
	if denied {
		y += "  - {from: act, port: denied, to: refused}\n"
	}
	return y
}

type gateApproval struct {
	ID      string `json:"id"`
	FlowRun string `json:"flow_run_id"`
	NodeRun string `json:"node_run_id"`
	Request struct {
		Gate   bool                          `json:"gate"`
		Title  string                        `json:"title"`
		BodyMD string                        `json:"body_md"`
		DryRun bool                          `json:"dry_run"`
		Fields []struct{ Key, Value string } `json:"fields"`
	} `json:"request"`
	ExpiresAt string `json:"expires_at"`
}

func (d *Daemon) gateApproval() gateApproval {
	d.t.Helper()
	ap := d.WaitApproval("request")
	var out gateApproval
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&out)
	return out
}

// marks counts how many times the gated command ran.
func (d *Daemon) marks() int {
	b, err := os.ReadFile(d.Path("data/state/g/act/ran"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "ran\n")
}

// A gated command node parks as awaiting_approval without running; the approval shows the node's configuration and
// input; approving runs the node and the flow continues.
func TestRequiresApprovalApprove(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", true))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "7", "name": "api"})
	parked := d.WaitRun(run, "awaiting_approval")
	ap := d.gateApproval()
	if n := d.Node(parked, "act"); n.Status != "awaiting_approval" || n.NodeType != "command" || !strings.HasPrefix(n.Reason, "awaiting approval "+ap.ID) {
		t.Fatalf("parked node: %+v", n)
	}
	if !ap.Request.Gate || ap.Request.Title != "g: run act?" || ap.FlowRun != run || ap.NodeRun == "" || ap.Request.DryRun || ap.ExpiresAt != "" {
		t.Fatalf("approval: %+v", ap)
	}
	for _, want := range []string{"echo ran", `"name": "api"`} {
		if !strings.Contains(ap.Request.BodyMD, want) {
			t.Errorf("approval body lacks %q:\n%s", want, ap.Request.BodyMD)
		}
	}
	if len(ap.Request.Fields) < 3 || ap.Request.Fields[1].Key != "node" || ap.Request.Fields[1].Value != "act (command)" {
		t.Errorf("fields: %+v", ap.Request.Fields)
	}
	if d.marks() != 0 {
		t.Fatal("the node ran before it was approved")
	}

	d.Decide(ap.ID, true, map[string]any{"comment": "go"})
	det := d.WaitRun(run, "succeeded")
	if n := d.Node(det, "act"); n.Status != "done" || len(n.Ports) != 1 || n.Ports[0] != "out" || n.ExitCode == nil || *n.ExitCode != 0 {
		t.Fatalf("approved node:\n%s", describeRun(det))
	}
	items := d.InboxByTitle()
	if _, ok := items["after true"]; !ok {
		t.Errorf("downstream did not run after approval: %+v", items)
	}
	if _, ok := items["refused 7"]; ok {
		t.Errorf("denied branch ran: %+v", items)
	}
	if d.marks() != 1 {
		t.Errorf("node ran %d times, want 1", d.marks())
	}
}

// Denying skips the node and sends its input, with the decision and comment, down port denied.
func TestRequiresApprovalDeny(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", true))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "7"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.gateApproval()
	d.Decide(ap.ID, false, map[string]any{"comment": "not now"})
	det := d.WaitRun(run, "succeeded")
	n := d.Node(det, "act")
	if n.Status != "done" || len(n.Ports) != 1 || n.Ports[0] != "denied" || n.ExitCode != nil || n.Error != "denied" || !strings.HasPrefix(n.Reason, "denied: not now") {
		t.Fatalf("denied node:\n%s", describeRun(det))
	}
	if d.marks() != 0 {
		t.Error("a denied node ran")
	}
	items := d.InboxByTitle()
	it, ok := items["refused 7"]
	if !ok || !strings.HasPrefix(it.SummaryMD, "denied|not now|"+ap.ID+"|") {
		t.Errorf("denied branch: %+v", items)
	}
	if _, ok := items["after true"]; ok {
		t.Errorf("approved branch ran: %+v", items)
	}
}

// With nothing wired to denied the branch just stops: the node run is done on port denied and the flow run succeeds.
func TestRequiresApprovalDenyUnwired(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", false))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "7"})
	d.WaitRun(run, "awaiting_approval")
	d.Decide(d.gateApproval().ID, false, nil)
	det := d.WaitRun(run, "succeeded")
	if n := d.Node(det, "act"); n.Status != "done" || len(n.Ports) != 1 || n.Ports[0] != "denied" {
		t.Fatalf("denied node:\n%s", describeRun(det))
	}
	if len(det.NodeRuns) != 1 || d.marks() != 0 || len(d.Inbox()) != 0 {
		t.Errorf("the branch did not stop:\n%s", describeRun(det))
	}
}

// approval_timeout expires the approval; the node is denied (decision timeout) and never runs.
func TestRequiresApprovalTimeout(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "    approval_timeout: 1s\n", true))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "7"})
	det := d.WaitRun(run, "succeeded")
	if n := d.Node(det, "act"); n.Status != "done" || n.Ports[0] != "denied" || n.Error != "approval_timeout" {
		t.Fatalf("timed-out node:\n%s", describeRun(det))
	}
	if it := d.InboxByTitle()["refused 7"]; !strings.HasPrefix(it.SummaryMD, "timeout|timed out|") {
		t.Errorf("denied branch: %+v", it)
	}
	if d.marks() != 0 {
		t.Error("a timed-out node ran")
	}
	if got := d.Approvals("expired"); len(got) != 1 || got[0].Type != "request" {
		t.Errorf("expired approvals: %+v", got)
	}
}

// A dry run still asks, and the approval says so.
func TestRequiresApprovalDryRun(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("dry_run: true\n", "", true))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "7"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.gateApproval()
	if !ap.Request.DryRun || !strings.HasPrefix(ap.Request.BodyMD, "This is a dry run: approving or denying only exercises the flow's branches.") {
		t.Fatalf("dry-run approval: %+v", ap.Request)
	}
	d.Decide(ap.ID, true, nil)
	d.WaitRun(run, "succeeded")
}

// A pending gate survives a daemon restart and can still be decided.
func TestRequiresApprovalSurvivesRestart(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", true))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "7"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.gateApproval()

	d.Restart()
	if det := d.GetRun(run, 0); det.FlowRun.Status != "awaiting_approval" || d.Node(det, "act").Status != "awaiting_approval" {
		t.Fatalf("after restart:\n%s", describeRun(det))
	}
	d.Decide(ap.ID, true, nil)
	d.WaitRun(run, "succeeded")
	if d.marks() != 1 {
		t.Errorf("node ran %d times, want 1", d.marks())
	}
}

// "Run node on sample" of a gated node takes the simulated decision instead of waiting.
func TestRequiresApprovalTestNodeSimulates(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", true))
	d.Start()

	for sim, port := range map[string]string{"": "out", "approved": "out", "denied": "denied"} {
		body := map[string]any{"node_id": "act", "input": map[string]any{"source": "json", "json": map[string]any{"id": "1"}}}
		if sim != "" {
			body["simulate_approval"] = sim
		}
		var res struct {
			NodeRun NodeRun `json:"node_run"`
		}
		d.Post("/api/flows/g/test-node", body).Want(200).JSON(&res)
		if res.NodeRun.Status != "done" || len(res.NodeRun.Ports) != 1 || res.NodeRun.Ports[0] != port {
			t.Errorf("simulate %q: %+v", sim, res.NodeRun)
		}
	}
	if got := d.Approvals(""); len(got) != 0 {
		t.Errorf("a test created approvals: %+v", got)
	}
}

const gateInboxFlowYAML = `id: gi
enabled: true
subscribe: [src]
nodes:
  - {id: note, type: inbox, title: "Look at {{.input.id}}", summary_md: "body {{.input.id}}", requires_approval: true}
  - {id: denied-note, type: inbox, title: "skipped {{.input.input.id}}"}
edges:
  - {from: note, port: denied, to: denied-note}
`

// A gated inbox node creates its item only once approved; the approval shows the rendered title and summary.
func TestRequiresApprovalInboxNode(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("gi", gateInboxFlowYAML)
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.gateApproval()
	if ap.Request.Title != "gi: run note?" || !strings.Contains(ap.Request.BodyMD, "body 1") {
		t.Errorf("approval: %+v", ap.Request)
	}
	var titles bool
	for _, f := range ap.Request.Fields {
		titles = titles || (f.Key == "title" && f.Value == "Look at 1")
	}
	if !titles || len(d.Inbox()) != 0 {
		t.Fatalf("fields %+v, inbox %+v", ap.Request.Fields, d.Inbox())
	}
	d.Decide(ap.ID, true, nil)
	d.WaitRun(run, "succeeded")
	if items := d.InboxByTitle(); len(items) != 1 || items["Look at 1"].Title == "" {
		t.Errorf("inbox: %+v", items)
	}

	run2 := d.InjectRun("src", map[string]any{"id": "2"})
	d.WaitRun(run2, "awaiting_approval")
	d.Decide(d.gateApproval().ID, false, nil)
	d.WaitRun(run2, "succeeded")
	items := d.InboxByTitle()
	if _, ok := items["Look at 2"]; ok {
		t.Errorf("a denied inbox node created its item: %+v", items)
	}
	if _, ok := items["skipped 2"]; !ok {
		t.Errorf("denied branch: %+v", items)
	}
}

const gateAgentFlowYAML = `id: ga
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: approve
    approve: proposal
    prompt: p
    requires_approval: true
  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
  - id: refused
    type: inbox
    title: "refused {{.input.input.id}}"
    summary_md: '{{.input.approval.decision}}|{{.input.approval.comment}}'
edges:
  - {from: work, to: done}
  - {from: work, port: denied, to: refused}
`

// The gate comes first: the agent does not start until it is approved, and then its own proposal approval follows.
func TestRequiresApprovalAgentGateThenProposal(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", proposalScript(true))
	d.Flow("ga", gateAgentFlowYAML)
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "42"})
	parked := d.WaitRun(run, "awaiting_approval")
	ap := d.gateApproval()
	work := d.Node(parked, "work")
	if work.Status != "awaiting_approval" || len(d.AgentRuns(work.ID)) != 0 || len(d.Approvals("pending")) != 1 {
		t.Fatalf("before the gate is approved: node %+v", work)
	}
	if !strings.Contains(ap.Request.BodyMD, "Review 42") {
		t.Errorf("gate body lacks the instructions:\n%s", ap.Request.BodyMD)
	}
	d.Decide(ap.ID, true, nil)

	prop := d.WaitApproval("proposal")
	if prop.NodeID != "work" || !strings.Contains(prop.ProposalMD, proposalPlan) {
		t.Fatalf("proposal approval: %+v", prop)
	}
	d.Eventually("the node to park on the proposal", func() bool {
		return d.Node(d.GetRun(run, 0), "work").Status == "awaiting_approval"
	})
	if d.fileThere(d.workFile("ga", "hello.txt")) {
		t.Fatal("the plan was executed before the proposal was approved")
	}
	d.Decide(prop.ID, true, nil)
	done := d.WaitRun(run, "succeeded")
	if n := d.Node(done, "work"); n.Status != "done" || len(d.AgentRuns(n.ID)) != 2 {
		t.Errorf("agent node:\n%s", describeRun(done))
	}
	if !d.fileThere(d.workFile("ga", "hello.txt")) {
		t.Error("the approved plan was not executed")
	}
}

// Denying the gate means the agent never starts; the denied port carries the gate's approval, not an agent summary.
func TestRequiresApprovalAgentGateDenied(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", proposalScript(true))
	d.Flow("ga", gateAgentFlowYAML)
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "42"})
	d.WaitRun(run, "awaiting_approval")
	d.Decide(d.gateApproval().ID, false, map[string]any{"comment": "no agents today"})
	det := d.WaitRun(run, "succeeded")
	if n := d.Node(det, "work"); n.Status != "done" || n.Ports[0] != "denied" || len(d.AgentRuns(n.ID)) != 0 {
		t.Fatalf("denied agent node:\n%s", describeRun(det))
	}
	if it := d.InboxByTitle()["refused 42"]; it.SummaryMD != "denied|no agents today" {
		t.Errorf("denied branch: %+v", it)
	}
}
