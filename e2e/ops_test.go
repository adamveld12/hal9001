package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---- helpers ----

// cleanGitEnv clears the GIT_* variables of the test process (set when the
// suite runs inside a git hook) so git in tests and in the daemon only looks
// at the directory it is told to use.
func cleanGitEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GIT_") {
			t.Setenv(k, "") // registers the restore
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// git runs git in dir with a fixed identity and no signing.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("pid file %s: %q", path, b)
	}
	return pid
}

// agentRunsWith lists the agent runs with the given status.
func (d *Daemon) agentRunsWith(status string) []AgentRun {
	d.t.Helper()
	var l struct {
		Items []AgentRun `json:"items"`
	}
	d.Get("/api/agent-runs?limit=200&status=" + status).Want(200).JSON(&l)
	return l.Items
}

// ---- scenario 8 ----

// Scenario 8: "Auto-mode agent in `worktree` cwd makes a change in an isolated
// worktree, not the source repo." The run cwd is under data/worktrees/, the
// source repo stays clean, and the worktree is removed when the run succeeds.
func TestScenario08AutoAgentWorktree(t *testing.T) {
	cleanGitEnv(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "README.md")
	git(t, repo, "commit", "-q", "-m", "init")
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))

	proof := t.TempDir()
	d := newDaemon(t)
	d.IdleSensor("src")
	prompt := "none"
	d.Prompt("p", prompt, "Create hello.txt.")
	d.Runtime("fake", script{Turns: []turn{{
		When: inMode("bypass"),
		Steps: []step{
			editPermission("c1", "hello.txt", "hi from the agent\n"),
			{Op: "exec", Cmd: []string{"sh", "-c", "cp hello.txt " + shq(filepath.Join(proof, "hello.txt")) +
				" && cp README.md " + shq(filepath.Join(proof, "README.md")) +
				" && git rev-parse HEAD > " + shq(filepath.Join(proof, "head"))}},
			say("created hello.txt"),
		},
	}}})
	d.Flow("wt", opsAutoFlow("wt", "src", "    cwd: {mode: worktree, repo: "+strconv.Quote(repo)+", ref: HEAD}\n    allow:\n      tools: ['^Write hello']\n"))
	d.Start()

	run := d.RunOf("src", map[string]any{"id": "1"})
	ars := d.AgentRuns(d.Node(run, "work").ID)
	if len(ars) != 1 {
		t.Fatalf("agent runs: %+v", ars)
	}
	ar := ars[0]
	if ar.Status != "succeeded" || ar.Policy != "auto" {
		t.Errorf("agent run: %+v", ar)
	}
	wtRoot := d.Path("data/worktrees") + string(os.PathSeparator)
	if !strings.HasPrefix(ar.Cwd, wtRoot) || ar.Cwd == repo {
		t.Errorf("agent cwd %q is not under %s", ar.Cwd, wtRoot)
	}
	if perms := d.Permissions(ar.ID); len(perms) != 1 || perms[0].Decision != "allow" || perms[0].DecidedBy != "policy" {
		t.Errorf("permission requests: %+v", perms)
	}

	// The change was made in the worktree ...
	if got, err := os.ReadFile(filepath.Join(proof, "hello.txt")); err != nil || string(got) != "hi from the agent\n" {
		t.Errorf("worktree copy of hello.txt: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(proof, "README.md")); err != nil || string(got) != "source\n" {
		t.Errorf("worktree did not contain the repo's files: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(proof, "head")); err != nil || strings.TrimSpace(string(got)) != head {
		t.Errorf("worktree HEAD %q, want %s (%v)", got, head, err)
	}
	// ... and the source repo is untouched.
	if fileExists(filepath.Join(repo, "hello.txt")) {
		t.Error("hello.txt was written into the source repo")
	}
	if st := git(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("source repo is dirty:\n%s", st)
	}
	if after := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD")); after != head {
		t.Errorf("source HEAD moved to %s", after)
	}
	// A successful run removes its worktree.
	d.Eventually("the worktree to be removed", func() bool { return !fileExists(ar.Cwd) })
	if list := git(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, ar.Cwd) {
		t.Errorf("the repo still lists the worktree:\n%s", list)
	}
}

// ---- scenario 9 ----

// dryNodes are the nodes shared by the dry-run tests: an auto agent, a
// side-effect command that writes effectFile, a command that reports
// $HAL_DRY_RUN, and inbox nodes downstream of each.
func dryNodes(effectFile string) string {
	return `nodes:
  - id: work
    type: agent
    runtime: fake
    mode: auto
    prompt: p
  - id: effect
    type: command
    lang: bash
    side_effect: true
    script: |
      echo ran >> ` + shq(effectFile) + `
      echo '{"id":"effect"}'
  - id: probe
    type: command
    lang: bash
    script: |
      echo "{\"dry\":\"${HAL_DRY_RUN:-}\"}"
  - {id: done, type: inbox, title: "agent {{.input.input.id}}"}
  - {id: after-effect, type: inbox, title: "after effect"}
  - {id: probed, type: inbox, title: "probe [{{.input.dry}}]"}
edges:
  - {from: work, to: done}
  - {from: effect, to: after-effect}
  - {from: probe, to: probed}
`
}

// Scenario 9: "Dry-run flow: agent forced readonly, `side_effect` node shown
// `skipped:dry_run`, inbox item tagged." Dry-run inbox items do not count as
// unread in the status.
func TestScenario09DryRunFlow(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Create hello.txt.")
	effectFile := d.Path("effect.log")
	d.Runtime("fake", script{Turns: []turn{
		{When: inMode("default"), Steps: []step{
			editPermission("c1", "hello.txt", "from readonly\n", say("edit was rejected")),
			say("investigated only"),
		}},
		{When: inMode("bypass"), Steps: []step{
			{Op: "write_file", Path: "bypass.txt", Data: "agent ran with full permissions\n"},
			say("wrote bypass.txt"),
		}},
	}})
	d.Flow("dry", "id: dry\nenabled: true\ndry_run: true\nsubscribe: [src]\n"+dryNodes(effectFile))
	d.Start()

	run := d.RunOf("src", map[string]any{"id": "1"})
	if !run.FlowRun.DryRun {
		t.Errorf("flow run of a dry_run flow: dry_run=false\n%s", describeRun(run))
	}

	// Agent node: forced readonly, the edit is rejected by policy, the run succeeds.
	work := d.Node(run, "work")
	if work.Status != "done" || work.Decision != "emitted" {
		t.Errorf("agent node: %+v", work)
	}
	ars := d.AgentRuns(work.ID)
	if len(ars) != 1 {
		t.Fatalf("agent runs: %+v", ars)
	}
	ar := ars[0]
	if ar.Policy != "readonly" || ar.Status != "succeeded" {
		t.Errorf("agent run: mode=%s policy=%s status=%s", ar.Mode, ar.Policy, ar.Status)
	}
	perms := d.Permissions(ar.ID)
	if len(perms) != 1 || perms[0].ToolKind != "edit" || perms[0].Decision != "reject" || perms[0].DecidedBy != "policy" || perms[0].Reason == "" {
		t.Errorf("permission requests: %+v", perms)
	}
	for _, f := range []string{"hello.txt", "bypass.txt"} {
		if fileExists(filepath.Join(ar.Cwd, f)) {
			t.Errorf("%s was written by the dry-run agent", f)
		}
	}
	if tr := d.Transcript(ar.ID); !strings.Contains(tr, "edit was rejected") || strings.Contains(tr, "wrote bypass.txt") {
		t.Errorf("transcript is not that of the readonly turn:\n%s", tr)
	}

	// Side-effect node: skipped with reason dry_run; nothing happened.
	eff := d.Node(run, "effect")
	if eff.Status != "skipped" || eff.Decision != "skipped" || eff.Reason != "skipped:dry_run" {
		t.Errorf("side_effect node: %+v", eff)
	}
	if fileExists(effectFile) {
		t.Errorf("side_effect node ran: %s", d.Read("effect.log"))
	}
	for _, n := range run.NodeRuns {
		if n.NodeID == "after-effect" {
			t.Errorf("a node downstream of a skipped node ran: %+v", n)
		}
	}

	// Command nodes see HAL_DRY_RUN=1.
	if p := d.Node(run, "probe"); !strings.Contains(p.Stdout.Text, `"dry":"1"`) {
		t.Errorf("probe stdout: %q", p.Stdout.Text)
	}

	// Inbox items are tagged and not counted as unread.
	items := d.InboxByTitle()
	for _, title := range []string{"agent 1", "probe [1]"} {
		it, ok := items[title]
		if !ok || !it.DryRun {
			t.Errorf("inbox item %q: %+v (all: %v)", title, it, items)
		}
	}
	if _, ok := items["after effect"]; ok {
		t.Error("an inbox item was created downstream of the skipped node")
	}
	if s := d.GetStatus(); s.UnreadInbox != 0 {
		t.Errorf("unread_inbox = %d, want 0 with only dry-run items", s.UnreadInbox)
	}
}

// Scenario 9, replay variant: "Optional dry-run toggle at replay time". A
// replay with dry_run: true of a non-dry flow is a dry run: HAL_DRY_RUN=1, the
// side-effect node is skipped, inbox items are tagged.
func TestScenario09DryRunAtReplay(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Create hello.txt.")
	effectFile := d.Path("effect.log")
	d.Runtime("fake", script{Turns: []turn{
		{When: inMode("default"), Steps: []step{say("investigated only")}},
		{When: inMode("bypass"), Steps: []step{say("did the work")}},
	}})
	d.Flow("plain", "id: plain\nenabled: true\nsubscribe: [src]\n"+dryNodes(effectFile))
	d.Start()

	live := d.RunOf("src", map[string]any{"id": "1"})
	if live.FlowRun.DryRun {
		t.Fatalf("live run of a non-dry flow is dry: %+v", live.FlowRun)
	}
	if p := d.Node(live, "probe"); !strings.Contains(p.Stdout.Text, `"dry":""`) {
		t.Errorf("live probe stdout: %q", p.Stdout.Text)
	}
	if e := d.Node(live, "effect"); e.Status != "done" {
		t.Errorf("live side_effect node: %+v", e)
	}
	if ars := d.AgentRuns(d.Node(live, "work").ID); len(ars) != 1 || ars[0].Policy != "auto" {
		t.Errorf("live agent runs: %+v", ars)
	}
	if got := strings.Count(d.Read("effect.log"), "ran"); got != 1 {
		t.Fatalf("live side effect ran %d times, want 1", got)
	}
	if s := d.GetStatus(); s.UnreadInbox != 3 {
		t.Fatalf("unread_inbox after the live run = %d, want 3", s.UnreadInbox)
	}

	var replay FlowRun
	d.Post("/api/events/"+live.FlowRun.EventID+"/replay", map[string]any{"flow": "plain", "dry_run": true}).Want(202).JSON(&replay)
	if replay.Kind != "replay" || !replay.DryRun || replay.ReplayOf != live.FlowRun.ID {
		t.Errorf("replay run: %+v", replay)
	}
	done := d.WaitRun(replay.ID, "succeeded")
	if !done.FlowRun.DryRun {
		t.Errorf("finished replay is not dry:\n%s", describeRun(done))
	}
	if p := d.Node(done, "probe"); !strings.Contains(p.Stdout.Text, `"dry":"1"`) {
		t.Errorf("replay probe stdout: %q", p.Stdout.Text)
	}
	if e := d.Node(done, "effect"); e.Status != "skipped" || e.Decision != "skipped" || e.Reason != "skipped:dry_run" {
		t.Errorf("replay side_effect node: %+v", e)
	}
	if got := strings.Count(d.Read("effect.log"), "ran"); got != 1 {
		t.Errorf("side effect ran %d times after the dry replay, want still 1", got)
	}
	if ars := d.AgentRuns(d.Node(done, "work").ID); len(ars) != 1 || ars[0].Policy != "readonly" {
		t.Errorf("replay agent runs: %+v", ars)
	}

	var dry, wet int
	for _, it := range d.Inbox() {
		switch {
		case it.FlowRunID == replay.ID:
			if !it.DryRun {
				t.Errorf("replay inbox item not tagged: %+v", it)
			}
			dry++
		case it.DryRun:
			t.Errorf("live inbox item tagged dry: %+v", it)
		default:
			wet++
		}
	}
	if dry != 2 || wet != 3 {
		t.Errorf("inbox items: %d dry (want 2), %d live (want 3)", dry, wet)
	}
	if s := d.GetStatus(); s.UnreadInbox != 3 {
		t.Errorf("unread_inbox = %d after the dry replay, want 3 (dry items excluded)", s.UnreadInbox)
	}
}

// ---- scenario 12 ----

// Scenario 12: "Concurrency: 5 agent-bound events with `max_agent_runs: 2` →
// 2 running, 3 queued, all complete." The flow limit is high, so the global
// agent limit binds.
func TestScenario12GlobalAgentConcurrency(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Config("max_agent_runs: 2\n")
	d.Prompt("p", "none", "Wait for the gate.")
	gate := d.Path("gate")
	d.Runtime("fake", script{Turns: []turn{{
		When: inMode("bypass"),
		Steps: []step{
			{Op: "exec", Cmd: []string{"sh", "-c", "while [ ! -f " + shq(gate) + " ]; do sleep 0.05; done"}},
			say("gate opened"),
		},
	}}})
	d.Flow("conc", `id: conc
enabled: true
max_concurrent: 5
subscribe: [src]
nodes:
  - {id: work, type: agent, runtime: fake, mode: auto, prompt: p}
  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
edges:
  - {from: work, to: done}
`)
	d.Start()

	const total = 5
	runIDs := make([]string, 0, total)
	for i := 1; i <= total; i++ {
		runIDs = append(runIDs, d.InjectRun("src", map[string]any{"id": strconv.Itoa(i)}))
	}

	// observe reads the counters and the agent run lists; the limit must hold
	// at every observation.
	observe := func() (running, queued int) {
		s := d.GetStatus()
		if s.RunningAgentRuns > 2 {
			t.Errorf("%d agent runs running, limit is 2: %+v", s.RunningAgentRuns, s)
		}
		if n := len(d.agentRunsWith("running")); n > 2 {
			t.Errorf("agent run list shows %d running, limit is 2", n)
		}
		return s.RunningAgentRuns, s.QueuedAgentRuns
	}
	d.Eventually("2 agent runs running and 3 queued", func() bool {
		r, q := observe()
		return r == 2 && q == 3
	})
	for range 8 { // the split stays put while the gate is closed
		if r, q := observe(); r != 2 || q != 3 {
			t.Fatalf("with the gate closed: running=%d queued=%d, want 2 and 3", r, q)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if n := len(d.agentRunsWith("running")); n != 2 {
		t.Errorf("agent runs with status running: %d, want 2", n)
	}
	nodeStates := map[string]int{}
	for _, id := range runIDs {
		nodeStates[d.Node(d.GetRun(id, 0), "work").Status]++
	}
	if nodeStates["running"] != 2 || nodeStates["queued"] != 3 {
		t.Errorf("agent node run states: %v, want 2 running and 3 queued", nodeStates)
	}
	if s := d.GetStatus(); s.RunningFlowRuns != total {
		t.Errorf("running flow runs = %d, want %d (the flow limit does not bind)", s.RunningFlowRuns, total)
	}
	for _, id := range runIDs {
		if st := d.GetRun(id, 0).FlowRun.Status; st != "running" {
			t.Errorf("flow run %s is %s while the gate is closed, want running", id, st)
		}
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d.Eventually("all flow runs to succeed", func() bool {
		observe()
		ok := true
		for _, id := range runIDs {
			switch st := d.GetRun(id, 0).FlowRun.Status; st {
			case "succeeded":
			case "queued", "running":
				ok = false
			default:
				t.Fatalf("flow run %s ended %s", id, st)
			}
		}
		return ok
	})
	d.Eventually("the counters to return to 0", func() bool {
		s := d.GetStatus()
		return s.RunningAgentRuns == 0 && s.QueuedAgentRuns == 0 && s.RunningFlowRuns == 0 && s.QueuedFlowRuns == 0
	})
	if n := len(d.agentRunsWith("succeeded")); n != total {
		t.Errorf("succeeded agent runs: %d, want %d", n, total)
	}
	if n := len(d.Inbox()); n != total {
		t.Errorf("inbox items: %d, want %d", n, total)
	}
}

// ---- scenario 14 ----

// Scenario 14a: "Kill switch: sensors stop polling". While paused a scheduled
// sensor does not poll, a manual poll is 423 `paused`, an injected event is
// accepted but its run stays queued, and after resume everything runs.
func TestScenario14aPausedStopsSensorsAndQueuesRuns(t *testing.T) {
	d := newDaemon(t, withClock())
	d.IdleSensor("src")
	counter := d.Path("polls.log")
	d.Sensor("tick", "id: tick\ninterval: 1s\nlang: bash\nscript: echo x >> "+shq(counter)+"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	d.Flow("watch-tick", "id: watch-tick\nenabled: true\nsubscribe: [tick]\nnodes:\n  - {id: n, type: inbox, title: tick}\n")
	d.Flow("work", "id: work\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: note, type: inbox, title: \"note {{.input.id}}\"}\n")
	d.Start()

	polls := func() int {
		b, err := os.ReadFile(counter)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "x")
	}
	d.Eventually("the sensor's first scheduled poll", func() bool { return polls() >= 1 })
	// The scheduler sets its next timer after a poll ends; a tick sent before that is lost, so tick until it polls.
	d.Eventually("the sensor to poll on schedule", func() bool {
		d.Tick(time.Second)
		return polls() >= 2
	})

	var ks KillSwitch
	d.Post("/api/killswitch/pause", map[string]any{"reason": "e2e pause"}).Want(200).JSON(&ks)
	if !ks.Paused || ks.KillRunning || ks.Reason != "e2e pause" {
		t.Errorf("pause response: %+v", ks)
	}
	if s := d.GetStatus(); !s.KillSwitch.Paused {
		t.Errorf("status after pause: %+v", s.KillSwitch)
	}
	// A poll that began before the pause may still finish; wait for it.
	d.Eventually("in-flight polls to finish", func() bool {
		var l sensorRunList
		d.Get("/api/sensors/tick/runs").Want(200).JSON(&l)
		for _, r := range l.Items {
			if r.Status == "running" {
				return false
			}
		}
		return true
	})
	before := polls()
	for range 3 {
		d.Tick(time.Second) // each tick is handled (and skipped) before Tick returns
	}
	if after := polls(); after != before {
		t.Errorf("sensor polled while paused: %d polls before, %d after three intervals", before, after)
	}

	r := d.Post("/api/sensors/tick/poll", nil).Want(423)
	if r.Problem() != "paused" {
		t.Errorf("manual poll while paused: %s", r)
	}
	if polls() != before {
		t.Error("a rejected manual poll ran the sensor")
	}

	runID := d.InjectRun("src", map[string]any{"id": "held"})
	time.Sleep(100 * time.Millisecond) // absence check: the run must not start
	if det := d.GetRun(runID, 0); det.FlowRun.Status != "queued" || len(det.NodeRuns) != 0 {
		t.Errorf("run injected while paused:\n%s", describeRun(det))
	}
	if s := d.GetStatus(); s.QueuedFlowRuns != 1 {
		t.Errorf("queued_flow_runs while paused = %d, want 1", s.QueuedFlowRuns)
	}

	d.Post("/api/killswitch/resume", nil).Want(200).JSON(&ks)
	if ks.Paused {
		t.Errorf("resume response: %+v", ks)
	}
	det := d.WaitRun(runID, "succeeded")
	if d.Node(det, "note").Status != "done" {
		t.Errorf("held run after resume:\n%s", describeRun(det))
	}
	d.Tick(time.Second)
	d.Eventually("the sensor to poll again after resume", func() bool { return polls() > before })
}

// pauseKillFixture starts a daemon with an agent that records its own pid,
// starts a child process in its process group, and then hangs.
func pauseKillFixture(t *testing.T) (d *Daemon, agentPID, childPID string) {
	t.Helper()
	d = newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Hang.")
	agentPID = d.Path("agent.pid")
	d.Runtime("fake", script{Turns: []turn{{
		When: inMode("bypass"),
		Steps: []step{
			{Op: "exec", Cmd: []string{"sh", "-c", "echo $PPID > " + shq(agentPID+".tmp") + " && mv " + shq(agentPID+".tmp") + " " + shq(agentPID)}},
			{Op: "spawn_child", Cmd: []string{"sleep", "999"}, PidFile: "child.pid"},
			{Op: "hang"},
		},
	}}})
	d.Flow("hang", opsAutoFlow("hang", "src", ""))
	d.Start()
	return d, agentPID, "child.pid"
}

// runHangingAgent injects an event and waits until the agent is running with
// its processes recorded. It returns the flow run id, the agent run, and the
// pids of the agent and of its child.
func runHangingAgent(t *testing.T, d *Daemon, agentPIDFile, childPIDName string) (string, AgentRun, int, int) {
	t.Helper()
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	var ar AgentRun
	d.Eventually("the agent to run and spawn its child", func() bool {
		runs := d.agentRunsWith("running")
		if len(runs) != 1 || runs[0].Cwd == "" {
			return false
		}
		ar = runs[0]
		return fileExists(agentPIDFile) && fileExists(filepath.Join(ar.Cwd, childPIDName))
	})
	apid, cpid := readPID(t, agentPIDFile), readPID(t, filepath.Join(ar.Cwd, childPIDName))
	if syscall.Kill(apid, 0) != nil || syscall.Kill(cpid, 0) != nil {
		t.Fatalf("agent pid %d or child pid %d is not alive", apid, cpid)
	}
	return runID, ar, apid, cpid
}

// Scenario 14b: "'pause + kill' terminates a running agent". The agent run and
// the flow run end `killed`, and the agent process and its child are gone.
func TestScenario14bPauseAndKillTerminatesAgent(t *testing.T) {
	d, agentPIDFile, childPIDName := pauseKillFixture(t)
	runID, ar, apid, cpid := runHangingAgent(t, d, agentPIDFile, childPIDName)

	var ks KillSwitch
	d.Post("/api/killswitch/pause", map[string]any{"kill_running": true, "reason": "stop everything"}).Want(200).JSON(&ks)
	if !ks.Paused || !ks.KillRunning || ks.Reason != "stop everything" {
		t.Errorf("pause response: %+v", ks)
	}

	det := d.WaitRun(runID, "killed")
	if n := d.Node(det, "work"); n.Status != "killed" {
		t.Errorf("agent node: %+v", n)
	}
	var got AgentRunDetail
	d.Get("/api/agent-runs/" + ar.ID).Want(200).JSON(&got)
	if got.Status != "killed" {
		t.Errorf("agent run status %s, want killed (error %q)", got.Status, got.Error)
	}
	for name, pid := range map[string]int{"agent": apid, "child": cpid} {
		d.EventuallyWithin(20*time.Second, name+" process to be gone", func() bool {
			return syscall.Kill(pid, 0) == syscall.ESRCH
		})
	}
	if s := d.GetStatus(); s.RunningAgentRuns != 0 || s.RunningFlowRuns != 0 {
		t.Errorf("status after kill: %+v", s)
	}
	if items := d.Inbox(); len(items) != 0 {
		t.Errorf("a killed agent produced inbox items: %+v", items)
	}
	// Still paused: a new event does not start.
	next := d.InjectRun("src", map[string]any{"id": "2"})
	time.Sleep(100 * time.Millisecond) // absence check: the run must not start
	if st := d.GetRun(next, 0).FlowRun.Status; st != "queued" {
		t.Errorf("run injected after pause+kill is %s, want queued", st)
	}
}

// Scenario 14c: "state survives restart". The paused state, who/why and the
// kill_running flag are kept across a daemon restart; queued runs stay queued
// until resume.
func TestScenario14cKillSwitchSurvivesRestart(t *testing.T) {
	d, agentPIDFile, childPIDName := pauseKillFixture(t)
	runID, _, _, _ := runHangingAgent(t, d, agentPIDFile, childPIDName)

	var set KillSwitch
	d.Post("/api/killswitch/pause", map[string]any{"kill_running": true, "reason": "before restart"}).Want(200).JSON(&set)
	d.WaitRun(runID, "killed")
	queued := d.InjectRun("src", map[string]any{"id": "2"})

	d.Restart()

	var ks KillSwitch
	d.Get("/api/killswitch").Want(200).JSON(&ks)
	if !ks.Paused || !ks.KillRunning || ks.Reason != "before restart" || ks.By != set.By {
		t.Errorf("kill switch after restart: %+v, set as %+v", ks, set)
	}
	if s := d.GetStatus(); !s.KillSwitch.Paused || !s.KillSwitch.KillRunning || s.KillSwitch.Reason != "before restart" {
		t.Errorf("status after restart: %+v", s.KillSwitch)
	}
	if st := d.GetRun(runID, 0).FlowRun.Status; st != "killed" {
		t.Errorf("killed run after restart is %s", st)
	}
	d.Post("/api/sensors/src/poll", nil).Want(423)
	time.Sleep(100 * time.Millisecond) // absence check: the run must not start
	if st := d.GetRun(queued, 0).FlowRun.Status; st != "queued" {
		t.Errorf("queued run after restart while paused is %s, want queued", st)
	}

	d.Post("/api/killswitch/resume", nil).Want(200).JSON(&ks)
	if ks.Paused || ks.KillRunning {
		t.Errorf("resume response: %+v", ks)
	}
	d.Get("/api/killswitch").Want(200).JSON(&ks)
	if ks.Paused {
		t.Errorf("kill switch after resume: %+v", ks)
	}
	// The queued run starts again (the agent hangs, so it is running).
	d.Eventually("the queued run to start after resume", func() bool {
		return d.GetRun(queued, 0).FlowRun.Status == "running"
	})
	d.Post("/api/killswitch/pause", map[string]any{"kill_running": true}).Want(200)
	d.WaitRun(queued, "killed")
}

// opsAutoFlow is a flow with one auto agent node `work` (extra: more node YAML
// lines, indented four spaces) feeding the inbox node `done`.
func opsAutoFlow(id, sensor, extra string) string {
	return `id: ` + id + `
enabled: true
subscribe: [` + sensor + `]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: auto
    prompt: p
` + extra + `  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
edges:
  - {from: work, to: done}
`
}
