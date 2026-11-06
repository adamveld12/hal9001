package engine_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

func countLines(path string) int {
	b, _ := os.ReadFile(path)
	return len(strings.Fields(string(b)))
}

// Scenario 14, pause only: sensors stop polling, polls and node tests are
// rejected, injected events and replays are accepted but stay queued until the
// switch is released.
func TestScenario14PauseStopsSensorsAndQueuesWork(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a 1s sensor schedule")
	}
	h := newHarness(t)
	polls := filepath.Join(t.TempDir(), "polls")
	h.sensor("src", "id: src\ninterval: 1s\nlang: bash\nscript: echo p >> "+polls+"\nkey: \"{{.id}}\"\ntimeout: 10s\n")
	h.flow("f", flowYAML("f", "src"))
	h.start()
	h.eventually("a scheduled poll", func() bool { return countLines(polls) >= 1 })

	ks, err := h.eng.Pause(t.Context(), false, "tester")
	if err != nil || !ks.Paused || ks.KillRunning || ks.By != "tester" {
		t.Fatalf("pause: %+v %v", ks, err)
	}
	if persisted, err := h.st.GetKillSwitch(t.Context()); err != nil || !persisted.Paused || persisted.By != "tester" {
		t.Fatalf("persisted state: %+v %v", persisted, err)
	}
	time.Sleep(300 * time.Millisecond) // a poll that was already running finishes
	before := countLines(polls)
	time.Sleep(2500 * time.Millisecond)
	if after := countLines(polls); after != before {
		t.Fatalf("sensor polled while paused: %d -> %d", before, after)
	}
	if _, err := h.poll(t.Context(), "src"); !errors.Is(err, core.ErrPaused) {
		t.Errorf("poll while paused: %v, want ErrPaused", err)
	}
	if _, _, err := h.eng.TestNode(t.Context(), core.TestNodeRequest{FlowID: "f", NodeID: "note", Input: []byte(`{"id":"1"}`)}); !errors.Is(err, core.ErrPaused) {
		t.Errorf("test node while paused: %v, want ErrPaused", err)
	}

	// Injection and replay queue.
	res, err := h.eng.InjectEvent(t.Context(), "src", []byte(`{"id":"queued"}`))
	if err != nil || len(res.FlowRunIDs) != 1 {
		t.Fatalf("inject while paused: %+v %v", res, err)
	}
	rp, err := h.eng.Replay(t.Context(), res.Event.ID, "f", false)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	for _, id := range []string{res.FlowRunIDs[0], rp.ID} {
		if st := h.run(id).Status; st != store.FlowRunQueued {
			t.Fatalf("run %s is %s while paused, want queued", id, st)
		}
	}

	if ks, err := h.eng.Resume(t.Context(), "tester"); err != nil || ks.Paused {
		t.Fatalf("resume: %+v %v", ks, err)
	}
	h.waitRun(res.FlowRunIDs[0], store.FlowRunSucceeded)
	h.waitRun(rp.ID, store.FlowRunSucceeded)
	h.eventually("polling to resume", func() bool { return countLines(polls) > before })
}

// Pause without kill lets running nodes finish but starts nothing new.
func TestPauseWithoutKillLetsRunningNodesFinish(t *testing.T) {
	h := newHarness(t)
	gate := filepath.Join(t.TempDir(), "gate")
	h.idleSensor()
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: slow
    type: command
    lang: bash
    script: |
      cat
      while [ ! -f `+gate+` ]; do sleep 0.02; done
  - {id: after, type: inbox, title: "after"}
edges:
  - {from: slow, to: after}
`)
	h.start()
	runID := h.injectRun(`{"id":"1"}`)
	h.eventually("slow node running", func() bool { return h.nodeStatus(runID, "slow") == store.NodeRunning })
	if _, err := h.eng.Pause(t.Context(), false, "me"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.eventually("slow node to finish", func() bool { return h.node(runID, "slow").Status == store.NodeDone })
	time.Sleep(300 * time.Millisecond)
	if got := len(h.nodeRuns(runID)); got != 2 || h.node(runID, "after").Status != store.NodeQueued {
		t.Fatalf("a new node started while paused:\n%s", h.dumpRun(runID))
	}
	if st := h.run(runID).Status; st != store.FlowRunRunning {
		t.Errorf("flow run is %s, want running (work remains queued)", st)
	}
	if _, err := h.eng.Resume(t.Context(), "me"); err != nil {
		t.Fatal(err)
	}
	h.waitRun(runID, store.FlowRunSucceeded)
}

// Scenario 14, pause + kill: running agents and commands are terminated and
// recorded as killed; the state and the queued work survive a restart.
func TestScenario14PauseAndKillTerminatesRunningWork(t *testing.T) {
	h := newHarness(t)
	pids := filepath.Join(t.TempDir(), "pids")
	h.idleSensor()
	h.prompt("go")
	h.runtime("fake", script{Turns: []turn{{Steps: []step{{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "wait"}}}, {Op: fakeagent.OpHang}}}}})
	h.flow("agent", "id: agent\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: work, type: agent, runtime: fake, mode: auto, prompt: p}\n")
	h.flow("cmd", `id: cmd
enabled: true
subscribe: [src]
nodes:
  - id: sleeper
    type: command
    lang: bash
    script: |
      sleep 300 &
      echo "$$ $!" >> `+pids+`
      wait
`)
	h.flow("quick", flowYAML("quick", "src"))
	h.start()

	res := h.inject(`{"id":"1"}`)
	runs := map[string]string{}
	for _, id := range res.FlowRunIDs {
		runs[h.run(id).FlowID] = id
	}
	h.waitRun(runs["quick"], store.FlowRunSucceeded)
	h.eventually("agent and command running", func() bool {
		a := h.agentRuns(h.node(runs["agent"], "work").ID)
		return countLines(pids) == 2 && len(a) == 1 && a[0].Status == store.AgentRunning
	})
	pidList := func() []int {
		b, _ := os.ReadFile(pids)
		var out []int
		for _, f := range strings.Fields(string(b)) {
			n, _ := strconv.Atoi(f)
			out = append(out, n)
		}
		return out
	}

	ks, err := h.eng.Pause(t.Context(), true, "me")
	if err != nil || !ks.Paused || !ks.KillRunning {
		t.Fatalf("pause+kill: %+v %v", ks, err)
	}
	h.waitRun(runs["agent"], store.FlowRunKilled)
	h.waitRun(runs["cmd"], store.FlowRunKilled)
	an := h.node(runs["agent"], "work")
	ar := h.agentRuns(an.ID)[0]
	if an.Status != store.NodeKilled || an.Error != "killed" || ar.Status != store.AgentKilled {
		t.Errorf("agent node %+v run %+v", an, ar)
	}
	if cn := h.node(runs["cmd"], "sleeper"); cn.Status != store.NodeKilled || cn.Error != "killed" {
		t.Errorf("command node %+v", cn)
	}
	for _, pid := range pidList() {
		h.eventually("process "+strconv.Itoa(pid)+" to be gone", func() bool { return !pidAlive(pid) })
	}

	// Work arriving now is queued, and everything survives a restart.
	queued := h.inject(`{"id":"2"}`)
	h.restart()
	ks, err = h.st.GetKillSwitch(t.Context())
	if err != nil || !ks.Paused || !ks.KillRunning {
		t.Fatalf("kill switch after restart: %+v %v", ks, err)
	}
	time.Sleep(400 * time.Millisecond)
	for _, id := range queued.FlowRunIDs {
		if st := h.run(id).Status; st != store.FlowRunQueued {
			t.Fatalf("run %s is %s after restart while paused, want queued", id, st)
		}
	}
	if _, err := h.eng.Resume(t.Context(), "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range queued.FlowRunIDs {
		if h.run(id).FlowID == "quick" {
			h.waitRun(id, store.FlowRunSucceeded)
		}
	}
}

// Scenario 15: a restart marks running work interrupted (not retried), leaves
// proposal approvals pending and approvable, and keeps queued runs.
func TestScenario15RestartInterruptsRunningWork(t *testing.T) {
	h := newHarness(t)
	pids := filepath.Join(t.TempDir(), "pids")
	h.idleSensor()
	h.prompt("go")
	h.runtime("fake", proposalScript(true))
	h.flow("slow", `id: slow
enabled: true
subscribe: [src]
nodes:
  - id: sleeper
    type: command
    lang: bash
    script: |
      sleep 300 &
      echo "$!" >> `+pids+`
      wait
`)
	h.flow("f", approveFlow("proposal", ""))
	h.start()

	res := h.inject(`{"id":"1"}`)
	byFlow := map[string]string{}
	for _, id := range res.FlowRunIDs {
		byFlow[h.run(id).FlowID] = id
	}
	h.waitRun(byFlow["f"], store.FlowRunAwaitingApproval)
	ap := h.waitApproval(store.ApprovalProposal)
	h.eventually("sleeper running", func() bool { return countLines(pids) == 1 })

	// A crash leaves rows behind that Stop never saw; the next start sorts them out.
	stale := &store.FlowRun{FlowID: "slow", FlowHash: h.run(byFlow["slow"]).FlowHash, EventID: res.Event.ID, Status: store.FlowRunRunning}
	if err := h.st.CreateFlowRun(t.Context(), stale); err != nil {
		t.Fatal(err)
	}
	staleNode := &store.NodeRun{FlowRunID: stale.ID, NodeID: "sleeper", NodeType: store.NodeCommand, Status: store.NodeRunning}
	if err := h.st.CreateNodeRun(t.Context(), staleNode); err != nil {
		t.Fatal(err)
	}

	h.restart()

	for _, id := range []string{byFlow["slow"], stale.ID} {
		fr := h.run(id)
		if fr.Status != store.FlowRunInterrupted {
			t.Errorf("flow run %s is %s after restart, want interrupted\n%s", id, fr.Status, h.dumpRun(id))
		}
	}
	if n := h.node(byFlow["slow"], "sleeper"); n.Status != store.NodeInterrupted || n.Error != "interrupted" {
		t.Errorf("running node after restart: %+v", n)
	}
	if n := h.node(stale.ID, "sleeper"); n.Status != store.NodeInterrupted {
		t.Errorf("crashed node after restart: %+v", n)
	}
	b, _ := os.ReadFile(pids)
	for _, f := range strings.Fields(string(b)) {
		pid, _ := strconv.Atoi(f)
		h.eventually("sleeper to be gone", func() bool { return !pidAlive(pid) })
	}
	time.Sleep(300 * time.Millisecond)
	if got := countLines(pids); got != 1 {
		t.Errorf("an interrupted node was retried: %d starts", got)
	}

	// The pending proposal survived and is still approvable.
	if st := h.run(byFlow["f"]).Status; st != store.FlowRunAwaitingApproval {
		t.Fatalf("proposal run is %s after restart", st)
	}
	if got := h.approvals(store.ApprovalPending); len(got) != 1 || got[0].ID != ap.ID {
		t.Fatalf("pending approvals after restart: %+v", got)
	}
	if _, err := h.eng.DecideApproval(t.Context(), ap.ID, core.ApprovalDecision{Approve: true, Comment: "after restart"}); err != nil {
		t.Fatal(err)
	}
	h.waitRun(byFlow["f"], store.FlowRunSucceeded)
	runs := h.agentRuns(h.node(byFlow["f"], "work").ID)
	if len(runs) != 2 || runs[1].ResumedVia != store.ResumedLoad {
		t.Errorf("resume after restart: %+v", runs)
	}
	if _, err := os.Stat(h.stateFile("work", "hello.txt")); err != nil {
		t.Errorf("approved plan did not execute after the restart: %v", err)
	}
}

// A per-tool approval dies with its agent process: after a restart it is
// expired and the run interrupted.
func TestPerToolApprovalExpiresOnRestart(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.prompt("go")
	h.runtime("fake", perToolScript())
	h.flow("f", approveFlow("per_tool", ""))
	h.start()
	runID := h.injectRun(`{"id":"1"}`)
	ap := h.waitApproval(store.ApprovalToolCall)
	h.restart()
	got, _ := h.st.GetApproval(t.Context(), ap.ID)
	if got.Status != store.ApprovalExpired {
		t.Errorf("approval after restart: %+v", got)
	}
	if st := h.run(runID).Status; st != store.FlowRunInterrupted {
		t.Errorf("run is %s, want interrupted\n%s", st, h.dumpRun(runID))
	}
	if _, err := h.eng.DecideApproval(t.Context(), ap.ID, core.ApprovalDecision{Approve: true}); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("deciding an expired approval: %v", err)
	}
}

// Hot reload (Apply) never drops running work: a run keeps the flow version it
// started with, later events use the new files, and new env values apply.
func TestApplyKeepsRunningWorkAndUsesNewFiles(t *testing.T) {
	h := newHarness(t)
	gate := filepath.Join(t.TempDir(), "gate")
	h.idleSensor()
	h.env("GREETING=old\n")
	flow := func(tag string) string {
		return `id: f
enabled: true
subscribe: [src]
max_concurrent: 5
nodes:
  - id: slow
    type: command
    lang: bash
    script: |
      cat > /dev/null
      while [ ! -f ` + gate + ` ]; do sleep 0.02; done
      echo "{\"greeting\":\"$GREETING\"}"
  - {id: note, type: inbox, title: "` + tag + ` {{.input.greeting}}"}
edges:
  - {from: slow, to: note}
`
	}
	h.flow("f", flow("v1"))
	h.start()
	first := h.injectRun(`{"id":"1"}`)
	h.eventually("first run's command to start", func() bool { return h.nodeStatus(first, "slow") == store.NodeRunning })

	h.flow("f", flow("v2"))
	h.env("GREETING=new\n")
	h.reload()
	second := h.injectRun(`{"id":"2"}`)
	if h.run(first).FlowHash == h.run(second).FlowHash {
		t.Fatal("the second run did not pick up the new flow version")
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.waitRun(first, store.FlowRunSucceeded)
	h.waitRun(second, store.FlowRunSucceeded)
	titles := map[string]bool{}
	for _, it := range h.inboxItems() {
		titles[it.Title] = true
	}
	// The command of the first run read its env at start (old); the second started after the reload.
	if !titles["v1 old"] || !titles["v2 new"] {
		t.Errorf("inbox titles: %v", titles)
	}

	// Disabling the flow stops fan-out.
	h.flow("f", strings.Replace(flow("v3"), "enabled: true", "enabled: false", 1))
	h.reload()
	if res := h.inject(`{"id":"3"}`); len(res.FlowRunIDs) != 0 {
		t.Errorf("disabled flow still fanned out: %v", res.FlowRunIDs)
	}
}

// ProbeRuntime reports what the agent advertises, and failures as results.
func TestProbeRuntime(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.runtime("fake", script{LoadSession: true, Models: []string{"fast", "smart"}, Turns: []turn{{Steps: []step{say("x")}}}})
	h.runtime("crash", script{Startup: []step{{Op: fakeagent.OpStderr, Text: "cannot start: missing token"}, {Op: fakeagent.OpExit, Code: 3}}, Turns: []turn{{Steps: []step{say("x")}}}})
	h.start()
	h.runtimes["missing"] = "  missing:\n    command: [/nonexistent/acp-agent]\n"
	h.reload()

	res, err := h.eng.ProbeRuntime(t.Context(), "fake")
	if err != nil || !res.OK || res.Error != "" {
		t.Fatalf("probe: %+v %v", res, err)
	}
	if !strings.Contains(string(res.AgentInfo), "fake-acp-agent") || !strings.Contains(string(res.Capabilities), `"loadSession":true`) ||
		strings.Join(res.Modes, ",") != "default,bypass" || strings.Join(res.ConfigOptionIDs, ",") != "model" || res.Duration <= 0 || len(res.ResolvedCommand) == 0 {
		t.Errorf("probe result: %+v", res)
	}
	for _, name := range []string{"crash", "missing"} {
		res, err := h.eng.ProbeRuntime(t.Context(), name)
		if err != nil || res.OK || res.Error == "" {
			t.Errorf("probe %s: %+v %v, want ok=false with an error", name, res, err)
		}
	}
	if res, _ := h.eng.ProbeRuntime(t.Context(), "crash"); !strings.Contains(res.Error, "missing token") {
		t.Errorf("crash probe lacks the agent's stderr: %q", res.Error)
	}
	if _, err := h.eng.ProbeRuntime(t.Context(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown runtime: %v", err)
	}
}

// Runtime config's {{data}} placeholder resolves to <home>/data, and the
// isolated agent home it names is created with a symlink to the existing login
// so settings in the real home cannot bypass the approval policy (docs/acp-gate.md).
func TestRuntimeIsolatedHomeIsCreatedWithCredentialLink(t *testing.T) {
	realHome := t.TempDir()
	login := filepath.Join(realHome, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(login), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(login, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", realHome)
	h := newHarness(t)
	h.write("config.yaml", `runtimes:
  codex:
    command: [codex-acp]
    modes: {ask: read-only, full: agent-full-access}
    env:
      CODEX_HOME: "{{data}}/runtimes/codex-home"
`)
	ws := spec.Load(h.home)
	if got := ws.Config.Value.Runtimes["codex"].Env["CODEX_HOME"]; got != spec.DataPlaceholder+"/runtimes/codex-home" {
		t.Fatalf("config: %q", got)
	}
	h.eng.Apply(ws)
	// codex-acp is not installed in tests: the probe reports failure, but only after setting the runtime up.
	if res, err := h.eng.ProbeRuntime(t.Context(), "codex"); err != nil || res.OK {
		t.Fatalf("probe: %+v %v", res, err)
	}
	home := filepath.Join(h.home, "data", "runtimes", "codex-home")
	if target, err := os.Readlink(filepath.Join(home, "auth.json")); err != nil || target != login {
		t.Errorf("credential link in %s: %q %v, want -> %s", home, target, err, login)
	}
}
