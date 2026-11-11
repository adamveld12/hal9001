package e2e

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

const approvalFlowYAML = `id: ask
enabled: true
subscribe: [src]
nodes:
  - id: confirm
    type: approval
    title: "Ship {{.input.name}}?"
    description: "Asks whether to deploy {{.input.name}}"
    body: "Deploy of **{{.input.name}}**"
    fields: [name, meta.env]
    priority: high
    timeout: TIMEOUT
  - {id: yes, type: inbox, title: "yes {{.input.input.name}}", summary_md: "{{.input.approval.decision}}|{{.input.approval.comment}}|{{.input.approval.decided_by}}"}
  - {id: no, type: inbox, title: "no {{.input.input.name}}", summary_md: "{{.input.approval.decision}}|{{.input.approval.comment}}"}
edges:
  - {from: confirm, port: approved, to: yes}
  - {from: confirm, port: denied, to: no}
`

type requestApproval struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	AgentID     *string `json:"agent_run_id"`
	NodeRun     string  `json:"node_run_id"`
	FlowRun     string  `json:"flow_run_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Priority    string  `json:"priority"`
	Details     struct {
		BodyMD string                        `json:"body_md"`
		DryRun bool                          `json:"dry_run"`
		Fields []struct{ Key, Value string } `json:"fields"`
	} `json:"details"`
}

func (d *Daemon) requestApproval() requestApproval {
	d.t.Helper()
	ap := d.WaitApproval("request")
	var out requestApproval
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&out)
	return out
}

func approvalNodeFlow(timeout string) string {
	y := strings.Replace(approvalFlowYAML, "    timeout: TIMEOUT\n", "", 1)
	if timeout != "" {
		y = strings.Replace(approvalFlowYAML, "TIMEOUT", timeout, 1)
	}
	return y
}

// Approving continues on port approved with the decision and comment in the payload; the pending approval is a
// request approval with the rendered title, body and fields, and the notification deep-links to it.
func TestApprovalNodeApprove(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1", "name": "api", "meta": map[string]any{"env": "prod"}})
	d.WaitRun(run, "awaiting_approval")
	ap := d.requestApproval()
	if ap.Type != "request" || ap.AgentID != nil || ap.FlowRun != run || ap.Title != "Ship api?" || ap.Description != "Asks whether to deploy api" ||
		!strings.Contains(ap.Details.BodyMD, "**api**") || ap.Priority != "high" || ap.Details.DryRun ||
		len(ap.Details.Fields) != 2 || ap.Details.Fields[1].Key != "meta.env" || ap.Details.Fields[1].Value != "prod" {
		t.Fatalf("approval: %+v", ap)
	}
	if n := d.Node(d.GetRun(run, 0), "confirm"); n.Status != "awaiting_approval" || n.NodeType != "approval" {
		t.Errorf("parked node: %+v", n)
	}
	d.Eventually("the approval notification", func() bool {
		for _, n := range d.Notifications() {
			if n["open"] == "http://localhost:"+strconv.Itoa(d.Port())+"/inbox/approval/"+ap.ID && n["subtitle"] == "Ship api?" && n["message"] == "Asks whether to deploy api" {
				return true
			}
		}
		return false
	})
	if got := d.Approvals("pending"); len(got) != 1 {
		t.Fatalf("pending: %+v", got)
	}

	d.Decide(ap.ID, true, map[string]any{"comment": "go ahead"})
	det := d.WaitRun(run, "succeeded")
	if n := d.Node(det, "yes"); n.Status != "done" || n.Stdin.Text == "" {
		t.Fatalf("approved branch:\n%s", describeRun(det))
	}
	items := d.InboxByTitle()
	if it, ok := items["yes api"]; !ok || !strings.HasPrefix(it.SummaryMD, "approved|go ahead|") {
		t.Errorf("inbox: %+v", items)
	}
	if _, ok := items["no api"]; ok {
		t.Errorf("denied branch ran: %+v", items)
	}
	if r := d.Post("/api/approvals/"+ap.ID+"/deny", nil).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("deciding twice: %s", r)
	}
}

func TestApprovalNodeDeny(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1", "name": "api"})
	d.WaitRun(run, "awaiting_approval")
	d.Decide(d.requestApproval().ID, false, map[string]any{"comment": "not now"})
	det := d.WaitRun(run, "succeeded")
	if d.Node(det, "no").Status != "done" {
		t.Fatalf("denied branch:\n%s", describeRun(det))
	}
	items := d.InboxByTitle()
	if it, ok := items["no api"]; !ok || it.SummaryMD != "denied|not now" {
		t.Errorf("inbox: %+v", items)
	}
	if _, ok := items["yes api"]; ok {
		t.Errorf("approved branch ran: %+v", items)
	}
}

// An approval that times out takes on_timeout's branch (denied by default) with decision timeout.
func TestApprovalNodeTimeout(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow("1s"))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1", "name": "api"})
	det := d.WaitRun(run, "succeeded")
	if d.Node(det, "no").Status != "done" {
		t.Fatalf("timeout branch:\n%s", describeRun(det))
	}
	if it := d.InboxByTitle()["no api"]; it.SummaryMD != "timeout|timed out" {
		t.Errorf("inbox: %+v", it)
	}
	if got := d.Approvals("expired"); len(got) != 1 || got[0].Type != "request" {
		t.Errorf("expired approvals: %+v", got)
	}
}

// A pending approval survives a daemon restart and can still be decided; the flow continues afterwards.
func TestApprovalNodeSurvivesRestart(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1", "name": "api"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.requestApproval()

	d.Restart()
	if det := d.GetRun(run, 0); det.FlowRun.Status != "awaiting_approval" || d.Node(det, "confirm").Status != "awaiting_approval" {
		t.Fatalf("after restart:\n%s", describeRun(det))
	}
	d.Decide(ap.ID, true, nil)
	if det := d.WaitRun(run, "succeeded"); d.Node(det, "yes").Status != "done" {
		t.Errorf("after deciding:\n%s", describeRun(det))
	}
}

// Approvals can be decided while the kill switch is engaged; the downstream node queues until resume.
func TestApprovalNodeDecidedWhilePaused(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1", "name": "api"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.requestApproval()
	d.Post("/api/killswitch/pause", map[string]any{}).Want(200)
	d.Decide(ap.ID, true, nil)
	d.Eventually("the next node to queue", func() bool {
		det := d.GetRun(run, 0)
		for _, n := range det.NodeRuns {
			if n.NodeID == "yes" && n.Status == "queued" {
				return true
			}
		}
		return false
	})
	d.Post("/api/killswitch/resume", nil).Want(200)
	d.WaitRun(run, "succeeded")
}

// A dry run still asks, but the approval says so.
func TestApprovalNodeDryRun(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", strings.Replace(approvalNodeFlow(""), "enabled: true\n", "enabled: true\ndry_run: true\n", 1))
	d.Start()

	run := d.InjectRun("src", map[string]any{"id": "1", "name": "api"})
	d.WaitRun(run, "awaiting_approval")
	ap := d.requestApproval()
	if !ap.Details.DryRun || !strings.HasPrefix(ap.Details.BodyMD, "This is a dry run: approving or denying only exercises the flow's branches.") {
		t.Fatalf("dry-run approval: %+v", ap)
	}
	d.Eventually("the dry-run notification", func() bool {
		for _, n := range d.Notifications() {
			if strings.HasSuffix(n["open"], "/inbox/approval/"+ap.ID) {
				return n["subtitle"] == "[dry run] Ship api?"
			}
		}
		return false
	})
	d.Decide(ap.ID, true, nil)
	d.WaitRun(run, "succeeded")
}

// "Run node on sample" takes the simulated decision instead of waiting and creates no approval.
func TestApprovalNodeTestNodeSimulates(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Start()

	for sim, port := range map[string]string{"": "approved", "approved": "approved", "denied": "denied"} {
		body := map[string]any{"node_id": "confirm", "input": map[string]any{"source": "json", "json": map[string]any{"id": "1", "name": "api"}}}
		if sim != "" {
			body["simulate_approval"] = sim
		}
		var res struct {
			NodeRun NodeRun `json:"node_run"`
		}
		d.Post("/api/flows/ask/test-node", body).Want(200).JSON(&res)
		if res.NodeRun.Status != "done" || len(res.NodeRun.Ports) != 1 || res.NodeRun.Ports[0] != port {
			t.Errorf("simulate %q: %+v", sim, res.NodeRun)
		}
	}
	d.Post("/api/flows/ask/test-node", map[string]any{
		"node_id": "confirm", "simulate_approval": "maybe", "input": map[string]any{"source": "json", "json": map[string]any{}},
	}).Want(400, 422)
	if got := d.Approvals(""); len(got) != 0 {
		t.Errorf("a test created approvals: %+v", got)
	}
}

const approvalEditFlowYAML = `id: ask
enabled: true
subscribe: [src]
nodes:
  - id: confirm
    type: approval
    title: "Send reply?"
    description: "Asks whether to send the drafted reply"
    editable: [draft, tags]
  - {id: yes, type: inbox, title: "sent", summary_md: "{{.input.input.draft}}|{{.input.approval.edited}}|{{.input.approval.edits.draft.before}}|{{index .input.input.tags 0}}"}
  - {id: no, type: inbox, title: "dropped", summary_md: "{{.input.input.draft}}|{{.input.approval.edited}}"}
edges:
  - {from: confirm, port: approved, to: yes}
  - {from: confirm, port: denied, to: no}
`

type editableApproval struct {
	Editable []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"editable"`
	Edits map[string]struct{ Before, After json.RawMessage } `json:"edits"`
}

// Approving with edits applies them to the downstream input and records before/after; undeclared paths are 422;
// a denial ignores edits.
func TestApprovalNodeEdits(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalEditFlowYAML)
	d.Start()
	payload := map[string]any{"id": "1", "draft": "hi", "tags": []string{"a", "b"}, "other": "x"}

	run := d.InjectRun("src", payload)
	d.WaitRun(run, "awaiting_approval")
	ap := d.requestApproval()
	var view editableApproval
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&view)
	if len(view.Editable) != 2 || view.Editable[0].Key != "draft" || string(view.Editable[0].Value) != `"hi"` ||
		string(view.Editable[1].Value) != `["a","b"]` {
		t.Fatalf("editable: %+v", view.Editable)
	}
	d.Post("/api/approvals/"+ap.ID+"/approve", map[string]any{"edits": map[string]any{"other": "y"}}).Want(422)
	d.Decide(ap.ID, true, map[string]any{"edits": map[string]any{"draft": "hello there", "tags": []string{"z"}}})
	d.WaitRun(run, "succeeded")
	if it := d.InboxByTitle()["sent"]; it.SummaryMD != "hello there|true|hi|z" {
		t.Errorf("approved with edits: %+v", it)
	}
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&view)
	if e := view.Edits["draft"]; string(e.Before) != `"hi"` || string(e.After) != `"hello there"` || len(view.Edits) != 2 {
		t.Errorf("recorded edits: %+v", view.Edits)
	}

	payload["id"] = "2"
	run2 := d.InjectRun("src", payload)
	d.WaitRun(run2, "awaiting_approval")
	d.Decide(d.requestApproval().ID, false, map[string]any{"edits": map[string]any{"draft": "nope"}})
	d.WaitRun(run2, "succeeded")
	if it := d.InboxByTitle()["dropped"]; it.SummaryMD != "hi|false" {
		t.Errorf("denied: %+v", it)
	}
}
