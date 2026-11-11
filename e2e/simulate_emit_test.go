package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// simulateDaemon has the saved flow `f` (two schema ports behind inbox nodes) and a fake runtime whose script must never
// run: a simulated agent node starts no agent.
func simulateDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newAssistDaemon(t, func(d *Daemon) {
		srcSensor(d)
		d.Runtime("fake", script{Turns: []turn{{Steps: []step{say("the agent must not run")}}}})
		d.Flow("f", portsFlow(twoPortAgent("")))
		d.assistRuntime(false, turn{Steps: []step{say("ok")}})
	})
	return d
}

func testNodeSim(d *Daemon, sim map[string]any) (res struct {
	NodeRun NodeRun `json:"node_run"`
}, code int) {
	d.t.Helper()
	body := map[string]any{"node_id": "work", "input": map[string]any{"source": "json", "json": map[string]any{"id": "7"}}}
	if sim != nil {
		body["simulate_emit"] = sim
	}
	r := d.Post("/api/flows/f/test-node", body)
	if r.Status == 200 {
		r.JSON(&res)
	}
	return res, r.Status
}

// test-node: the agent node emits the given output on the port without starting an agent; the node run says it was
// simulated and its stdout is the payload downstream nodes would receive.
func TestSimulateEmitTestNodeExplicitOutput(t *testing.T) {
	d := simulateDaemon(t)
	res, code := testNodeSim(d, map[string]any{"port": "needs_human", "output": map[string]any{"priority": "urgent", "body": "disk"}})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	n := res.NodeRun
	if n.Status != "done" || n.Decision != "emitted" || len(n.Ports) != 1 || n.Ports[0] != "needs_human" || n.Reason != "simulated: emit_needs_human" {
		t.Fatalf("node run: %+v", n)
	}
	var payload struct {
		Input map[string]any `json:"input"`
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(d.NodeDetail(n.ID).Stdout.Text), &payload); err != nil {
		t.Fatalf("stdout is not the payload: %v", err)
	}
	out, _ := payload.Agent["output"].(map[string]any)
	if payload.Input["id"] != "7" || payload.Agent["port"] != "needs_human" || payload.Agent["status"] != "simulated" || out["body"] != "disk" {
		t.Errorf("payload: %+v", payload)
	}
	if calls := d.AgentCalls("fake"); len(calls) != 0 {
		t.Errorf("an agent was started: %v", calls)
	}
	if len(d.AgentRuns(n.ID)) != 0 {
		t.Error("an agent run was recorded")
	}
}

// Without an output a minimal payload is generated from the port's schema (required fields; first enum value; placeholder strings).
func TestSimulateEmitTestNodeGeneratedOutput(t *testing.T) {
	d := simulateDaemon(t)
	res, code := testNodeSim(d, map[string]any{"port": "reviewed"})
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	var payload struct {
		Agent struct {
			Output map[string]any `json:"output"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(d.NodeDetail(res.NodeRun.ID).Stdout.Text), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Agent.Output["priority"] != "urgent" || payload.Agent.Output["body"] != "body" || len(payload.Agent.Output) != 2 {
		t.Errorf("generated output: %v", payload.Agent.Output)
	}
}

// The output is validated like a real call; a port that does not exist, or a node that is not an agent, is refused.
func TestSimulateEmitIsValidated(t *testing.T) {
	d := simulateDaemon(t)
	r := d.Post("/api/flows/f/test-node", map[string]any{
		"node_id": "work", "input": map[string]any{"source": "json", "json": map[string]any{}},
		"simulate_emit": map[string]any{"port": "reviewed", "output": map[string]any{"priority": "meh"}},
	})
	if r.Status != 400 && r.Status != 422 || !strings.Contains(string(r.Body), "/priority") || !strings.Contains(string(r.Body), "missing property 'body'") {
		t.Errorf("invalid output: %d %s", r.Status, r.Body)
	}
	for name, sim := range map[string]map[string]any{
		"unknown port": {"port": "nope"},
		"error port":   {"port": "error"},
		"bad name":     {"port": "No Such"},
	} {
		if _, code := testNodeSim(d, sim); code != 400 && code != 422 {
			t.Errorf("%s: status %d", name, code)
		}
	}
	r = d.Post("/api/flows/f/test-node", map[string]any{
		"node_id": "shown", "input": map[string]any{"source": "json", "json": map[string]any{}},
		"simulate_emit": map[string]any{"port": "reviewed"},
	})
	if r.Status != 400 && r.Status != 422 {
		t.Errorf("simulate_emit on an inbox node: %d %s", r.Status, r.Body)
	}
}

// Flow test (assist draft test): the simulated node is marked simulated, emits its payload on the port, and downstream
// nodes run as live (the inbox node records what it would create); an agent node without simulate_emit stays skipped.
func TestSimulateEmitInDraftFlowTest(t *testing.T) {
	d := simulateDaemon(t)
	s := d.assistCreateFlow("")
	d.Put("/api/assist/sessions/"+s.ID+"/draft", map[string]any{"yaml": strings.Replace(portsFlow(twoPortAgent("")), "id: f\n", "id: mine\n", 1)}).Want(200)

	res := d.assistFlowTest(s.ID, map[string]any{
		"input":         map[string]any{"id": "7"},
		"simulate_emit": map[string]any{"work": map[string]any{"port": "reviewed", "output": map[string]any{"priority": "urgent", "body": "fine"}}},
	})
	work, shown := res.node("work"), res.node("shown")
	if res.Status != "succeeded" || work == nil || !work.Simulated || work.Status != "done" || len(work.Outputs) != 1 || work.Outputs[0].Port != "reviewed" {
		t.Fatalf("work: %+v (run %s)", work, res.Status)
	}
	if shown == nil || shown.WouldCreate == nil || shown.WouldCreate.Title != "Reviewed fine" || shown.WouldCreate.Priority != "urgent" {
		t.Errorf("downstream did not run as live: %+v", shown)
	}
	if res.node("asked") != nil {
		t.Error("the other port's branch ran")
	}

	// Generated payload on the other port.
	res = d.assistFlowTest(s.ID, map[string]any{
		"input":         map[string]any{"id": "7"},
		"simulate_emit": map[string]any{"work": map[string]any{"port": "needs_human"}},
	})
	if asked := res.node("asked"); asked == nil || asked.WouldCreate == nil || asked.WouldCreate.Title != "Needs body" {
		t.Errorf("generated payload did not reach the asked node: %+v", asked)
	}

	// Without simulate_emit the agent node is skipped, as before.
	res = d.assistFlowTest(s.ID, map[string]any{"input": map[string]any{"id": "7"}})
	if work := res.node("work"); work == nil || work.Status != "skipped" || work.Simulated {
		t.Errorf("agent node without simulate_emit: %+v", work)
	}

	// An invalid payload is rejected before anything runs.
	r := d.Post("/api/assist/sessions/"+s.ID+"/test", map[string]any{
		"input":         map[string]any{"id": "7"},
		"simulate_emit": map[string]any{"work": map[string]any{"port": "reviewed", "output": map[string]any{"priority": "meh"}}},
	})
	if r.Status != 400 && r.Status != 422 || !strings.Contains(string(r.Body), "/priority") {
		t.Errorf("invalid payload: %d %s", r.Status, r.Body)
	}
	if calls := d.AgentCalls("fake"); len(calls) != 0 {
		t.Errorf("an agent was started: %v", calls)
	}
}

// The CLI passes the option: `flows test-node --simulate-emit port=json` (the assist test flag is covered by the CLI tests).
func TestSimulateEmitCLI(t *testing.T) {
	d := simulateDaemon(t)
	out, errOut, code := d.CLI("", "flows", "test-node", "f", "work", "--json", `{"id":"7"}`, "--simulate-emit", `reviewed={"priority":"normal","body":"cli"}`)
	if code != 0 || !strings.Contains(out, "simulated: emit_reviewed") || !strings.Contains(out, `cli`) {
		t.Fatalf("flows test-node: code %d out %s err %s", code, out, errOut)
	}
}
