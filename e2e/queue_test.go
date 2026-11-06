package e2e

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The run queue: flow runs waiting for a flow slot, and running flow runs
// whose only unfinished node is queued behind max_agent_runs. Removing a run
// ends it as `cancelled` and keeps it in history.

type queueResp struct {
	Items []struct {
		FlowRun        FlowRun  `json:"flow_run"`
		Position       int      `json:"position"`
		WaitingFor     string   `json:"waiting_for"`
		QueuedAt       string   `json:"queued_at"`
		WaitingNodeIDs []string `json:"waiting_node_ids"`
	} `json:"items"`
	Total                 int `json:"total"`
	WaitingForFlowSlot    int `json:"waiting_for_flow_slot"`
	WaitingForAgentSlot   int `json:"waiting_for_agent_slot"`
	WaitingForCommandSlot int `json:"waiting_for_command_slot"`
	Limits                struct {
		MaxAgentRuns   int `json:"max_agent_runs"`
		AgentRunsInUse int `json:"agent_runs_in_use"`
		Flows          []struct {
			FlowID        string `json:"flow_id"`
			MaxConcurrent int    `json:"max_concurrent"`
			Running       int    `json:"running"`
		} `json:"flows"`
	} `json:"limits"`
}

func (r queueResp) ids() []string {
	out := []string{}
	for _, it := range r.Items {
		out = append(out, it.FlowRun.ID)
	}
	return out
}

func (d *Daemon) queue(query string) queueResp {
	d.t.Helper()
	var q queueResp
	d.Get("/api/queue" + query).Want(200).JSON(&q)
	return q
}

// queueFixture is a daemon whose single agent slot (max_agent_runs: 1) is held
// by run `c1` until the gate file exists. Flow `conc` allows 5 concurrent runs;
// flow `solo` allows 1.
type queueFixture struct {
	*Daemon
	gate string
	c1   string // holds the agent slot
	c2   string // waits for the agent slot
	c3   string // waits for the agent slot
	s1   string // solo: running, waits for the agent slot
	s2   string // solo: queued behind s1 (flow slot)
}

func (f *queueFixture) open() {
	f.t.Helper()
	if err := os.WriteFile(f.gate, nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func newQueueFixture(t *testing.T) *queueFixture {
	t.Helper()
	d := newDaemon(t)
	d.IdleSensor("src")
	d.IdleSensor("src2")
	d.Config("max_agent_runs: 1\n")
	d.Prompt("p", "none", "Wait for the gate.")
	f := &queueFixture{Daemon: d, gate: d.Path("gate")}
	d.Runtime("fake", script{Turns: []turn{{
		When: inMode("bypass"),
		Steps: []step{
			{Op: "exec", Cmd: []string{"sh", "-c", "while [ ! -f " + shq(f.gate) + " ]; do sleep 0.05; done"}},
			say("gate opened"),
		},
	}}})
	for _, fl := range []struct{ id, sensor, max string }{{"conc", "src", "5"}, {"solo", "src2", "1"}} {
		d.Flow(fl.id, `id: `+fl.id+`
enabled: true
max_concurrent: `+fl.max+`
subscribe: [`+fl.sensor+`]
nodes:
  - {id: work, type: agent, runtime: fake, mode: auto, prompt: p}
  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
edges:
  - {from: work, to: done}
`)
	}
	d.Start()
	t.Cleanup(f.open) // release the agent so shutdown is quick

	f.c1 = d.InjectRun("src", map[string]any{"id": "c1"})
	d.Eventually("c1 to hold the agent slot", func() bool { return len(d.agentRunsWith("running")) == 1 })
	grow := func(sensor, id, waitingFor string, total int) string {
		run := d.InjectRun(sensor, map[string]any{"id": id})
		d.Eventually(id+" to wait for "+waitingFor, func() bool {
			q := d.queue("")
			return q.Total == total && q.Items[total-1].FlowRun.ID == run && q.Items[total-1].WaitingFor == waitingFor
		})
		return run
	}
	f.c2 = grow("src", "c2", "agent_slot", 1)
	f.c3 = grow("src", "c3", "agent_slot", 2)
	f.s1 = grow("src2", "s1", "agent_slot", 3)
	f.s2 = grow("src2", "s2", "flow_slot", 4)
	return f
}

func TestQueueListOrderAndWaitingFor(t *testing.T) {
	f := newQueueFixture(t)
	q := f.queue("")
	if got, want := q.ids(), []string{f.c2, f.c3, f.s1, f.s2}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("queue order = %v, want %v (c1 holds the slot and is not queued)", got, want)
	}
	wantWaiting := []string{"agent_slot", "agent_slot", "agent_slot", "flow_slot"}
	for i, it := range q.Items {
		if it.Position != i+1 || it.WaitingFor != wantWaiting[i] {
			t.Errorf("item %d: position=%d waiting_for=%s, want %d and %s", i, it.Position, it.WaitingFor, i+1, wantWaiting[i])
		}
		if it.QueuedAt == "" {
			t.Errorf("item %d has no queued_at", i)
		}
		if i < 3 && (len(it.WaitingNodeIDs) != 1 || it.WaitingNodeIDs[0] != "work") {
			t.Errorf("item %d waiting_node_ids = %v, want [work]", i, it.WaitingNodeIDs)
		}
		if i == 3 && it.FlowRun.Status != "queued" {
			t.Errorf("flow-slot item status = %s, want queued", it.FlowRun.Status)
		}
	}
	if q.WaitingForAgentSlot != 3 || q.WaitingForFlowSlot != 1 || q.WaitingForCommandSlot != 0 {
		t.Errorf("totals: agent=%d flow=%d command=%d, want 3, 1, 0", q.WaitingForAgentSlot, q.WaitingForFlowSlot, q.WaitingForCommandSlot)
	}
	if q.Limits.MaxAgentRuns != 1 || q.Limits.AgentRunsInUse != 1 {
		t.Errorf("agent limits: %+v, want 1 of 1 in use", q.Limits)
	}
	for _, fl := range q.Limits.Flows {
		switch fl.FlowID {
		case "conc":
			if fl.MaxConcurrent != 5 || fl.Running != 3 {
				t.Errorf("conc limits %+v, want max 5, 3 running", fl)
			}
		case "solo":
			if fl.MaxConcurrent != 1 || fl.Running != 1 {
				t.Errorf("solo limits %+v, want max 1, 1 running", fl)
			}
		default:
			t.Errorf("unexpected flow in limits: %+v", fl)
		}
	}
	if only := f.queue("?flow_id=solo"); strings.Join(only.ids(), ",") != f.s1+","+f.s2 {
		t.Errorf("queue of flow solo = %v, want [s1 s2]", only.ids())
	}
	f.Get("/api/queue?flow_id=Not%20A%20Slug").Want(400)

	f.open()
	for _, id := range []string{f.c1, f.c2, f.c3, f.s1, f.s2} {
		f.WaitRun(id, "succeeded")
	}
	if q := f.queue(""); q.Total != 0 {
		t.Errorf("queue after everything ran: %v", q.ids())
	}
}

func TestQueueRemoveOne(t *testing.T) {
	f := newQueueFixture(t)

	// A caller long-polling the queued run is woken by the cancellation.
	woke := make(chan FlowRunDetail, 1)
	go func() {
		var det FlowRunDetail
		f.Get("/api/flow-runs/" + f.c3 + "?wait=30s").Want(200).JSON(&det)
		woke <- det
	}()
	time.Sleep(100 * time.Millisecond)

	var fr FlowRun
	f.Delete("/api/queue/" + f.c3).Want(200).JSON(&fr)
	if fr.ID != f.c3 || fr.Status != "cancelled" || fr.Error != "removed from queue" {
		t.Fatalf("removed run = %+v, want cancelled with reason %q", fr, "removed from queue")
	}
	select {
	case det := <-woke:
		if det.FlowRun.Status != "cancelled" {
			t.Errorf("?wait returned status %s, want cancelled", det.FlowRun.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("?wait did not return when the run was cancelled")
	}

	det := f.GetRun(f.c3, 0)
	if n := f.Node(det, "work"); n.Status != "cancelled" || n.Reason != "removed from queue" {
		t.Errorf("node run = %s/%q, want cancelled/%q", n.Status, n.Reason, "removed from queue")
	}
	if got, want := f.queue("").ids(), []string{f.c2, f.s1, f.s2}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("queue after removal = %v, want %v", got, want)
	}
	// Removing it again is a conflict, not a second cancellation.
	if p := f.Delete("/api/queue/" + f.c3).Want(409).Problem(); p != "invalid-state" {
		t.Errorf("second removal problem = %q, want invalid-state", p)
	}

	// The cancelled run is not counted as a failure in the flow's health.
	var flows struct {
		Items []struct {
			ID     string `json:"id"`
			Health struct {
				Failed    int     `json:"failed"`
				ErrorRate float64 `json:"error_rate"`
				LastRun   *string `json:"last_run_status"`
			} `json:"health"`
		} `json:"items"`
	}
	f.Get("/api/flows").Want(200).JSON(&flows)
	for _, fl := range flows.Items {
		if fl.ID == "conc" && (fl.Health.Failed != 0 || fl.Health.ErrorRate != 0) {
			t.Errorf("flow conc health = %+v after a removal, want no failures", fl.Health)
		}
	}

	f.open()
	for _, id := range []string{f.c1, f.c2, f.s1, f.s2} {
		f.WaitRun(id, "succeeded")
	}
	// The removed run never started, even after the slot freed up.
	if got := f.GetRun(f.c3, 0); got.FlowRun.Status != "cancelled" || f.Node(got, "work").StartedAt != "" {
		t.Errorf("removed run after the others finished: %s", describeRun(got))
	}
	var runs struct {
		Items []AgentRun `json:"items"`
	}
	f.Get("/api/agent-runs?flow_run_id=" + f.c3).Want(200).JSON(&runs)
	if len(runs.Items) != 0 {
		t.Errorf("removed run has %d agent runs, want none", len(runs.Items))
	}
}

func TestQueueClear(t *testing.T) {
	f := newQueueFixture(t)

	var res struct {
		Cancelled int      `json:"cancelled"`
		IDs       []string `json:"flow_run_ids"`
	}
	f.Post("/api/queue/clear", map[string]any{"flow_id": "solo"}).Want(200).JSON(&res)
	if res.Cancelled != 2 || strings.Join(res.IDs, ",") != f.s1+","+f.s2 {
		t.Fatalf("clear of flow solo = %+v, want s1 and s2", res)
	}
	for _, id := range []string{f.s1, f.s2} {
		if st := f.GetRun(id, 0).FlowRun.Status; st != "cancelled" {
			t.Errorf("run %s is %s, want cancelled", id, st)
		}
	}
	if got, want := f.queue("").ids(), []string{f.c2, f.c3}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("queue after clearing solo = %v, want %v", got, want)
	}

	f.Post("/api/queue/clear", nil).Want(200).JSON(&res)
	if res.Cancelled != 2 || strings.Join(res.IDs, ",") != f.c2+","+f.c3 {
		t.Fatalf("clear all = %+v, want c2 and c3", res)
	}
	f.Post("/api/queue/clear", nil).Want(200).JSON(&res)
	if res.Cancelled != 0 || res.IDs == nil || len(res.IDs) != 0 {
		t.Errorf("clearing an empty queue = %+v, want 0 and []", res)
	}
	// The run that holds the agent slot is not queued and keeps going.
	if st := f.GetRun(f.c1, 0).FlowRun.Status; st != "running" {
		t.Errorf("c1 is %s after clearing, want running", st)
	}
	f.open()
	f.WaitRun(f.c1, "succeeded")
	if s := f.GetStatus(); s.QueuedFlowRuns != 0 || s.QueuedAgentRuns != 0 {
		t.Errorf("status after clear = %+v, want nothing queued", s)
	}
	f.Post("/api/queue/clear", map[string]any{"flow_id": "Bad Id"}).Want(422)
}

func TestQueueRemoveRefusals(t *testing.T) {
	f := newQueueFixture(t)

	// c1 is running a node: not removable.
	resp := f.Delete("/api/queue/" + f.c1).Want(409)
	if p := resp.Problem(); p != "invalid-state" {
		t.Errorf("problem = %q, want invalid-state", p)
	}
	if !strings.Contains(string(resp.Body), "kill switch") {
		t.Errorf("409 body does not point at the kill switch: %s", resp.Body)
	}
	if st := f.GetRun(f.c1, 0).FlowRun.Status; st != "running" {
		t.Errorf("c1 is %s after the refused removal, want running", st)
	}
	f.Delete("/api/queue/01ARZ3NDEKTSV4RRFFQ69G5FAV").Want(404)
	f.Delete("/api/queue/not-a-ulid").Want(400)

	f.open()
	f.WaitRun(f.c1, "succeeded")
	f.Delete("/api/queue/" + f.c1).Want(409)
}

func TestQueueCLI(t *testing.T) {
	f := newQueueFixture(t)
	out, _, code := f.CLI("", "queue", "list")
	if code != 0 || !strings.Contains(out, f.c2) || !strings.Contains(out, "flow_slot") {
		t.Fatalf("queue list: code %d\n%s", code, out)
	}
	if out, errs, code := f.CLI("", "queue", "remove", f.c2); code != 0 || !strings.Contains(out, "cancelled") {
		t.Fatalf("queue remove: code %d\n%s\n%s", code, out, errs)
	}
	if _, _, code := f.CLI("", "queue", "remove", f.c1); code == 0 {
		t.Error("queue remove of a run that is running a node succeeded")
	}
	if out, errs, code := f.CLI("", "queue", "clear", "--flow", "solo", "--yes"); code != 0 || !strings.Contains(out, f.s2) {
		t.Fatalf("queue clear --flow: code %d\n%s\n%s", code, out, errs)
	}
	if out, errs, code := f.CLI("", "queue", "clear", "--yes"); code != 0 || !strings.Contains(out, f.c3) {
		t.Fatalf("queue clear: code %d\n%s\n%s", code, out, errs)
	}
	if q := f.queue(""); q.Total != 0 {
		t.Errorf("queue after clear = %v", q.ids())
	}
}
