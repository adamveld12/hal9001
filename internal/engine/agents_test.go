package engine_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/core"
	"hal9001/internal/store"
)

type (
	script = fakeagent.Script
	turn   = fakeagent.Turn
	step   = fakeagent.Step
)

func whenMode(m string) *fakeagent.When { return &fakeagent.When{Mode: m} }

func say(text string) step { return step{Op: fakeagent.OpMessage, Text: text} }

// approveFlow is an approve-mode (proposal) agent node with done / refused follow-ups.
func approveFlow() string {
	return `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: approve
    approve: proposal
    prompt: p
  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
  - {id: refused, type: inbox, title: "refused {{.input.input.id}}", summary_md: "{{.input.agent.comment}}"}
edges:
  - {from: work, to: done}
  - {from: work, port: denied, to: refused}
`
}

func (h *harness) stateFile(node, name string) string {
	return filepath.Join(h.home, "data", "state", "f", node, name)
}

func inboxTitles(items []store.InboxItem) map[string]store.InboxItem {
	m := map[string]store.InboxItem{}
	for _, it := range items {
		m[it.Title] = it
	}
	return m
}

// proposalScript plans in the ask mode and executes (writes hello.txt) in the full mode.
func proposalScript(load bool) script {
	return script{LoadSession: load, Turns: []turn{
		{When: whenMode("default"), Steps: []step{say("Plan: create hello.txt")}},
		{When: whenMode("bypass"), Steps: []step{
			{Op: fakeagent.OpWriteFile, Path: "hello.txt", Data: "hi\n"},
			say("executed"),
		}},
	}}
}

// Scenario 7, approve: the proposal is queued with a notification, approving
// resumes the same session under the auto policy, and the DAG continues.
func TestScenario7ProposalApproveResumesSameSession(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.prompt("Review {{.input.id}}")
	h.runtime("fake", proposalScript(true))
	h.flow("f", approveFlow())
	h.start()

	runID := h.injectRun(`{"id":"42"}`)
	h.waitRun(runID, store.FlowRunAwaitingApproval)
	ap := h.waitProposal()
	if !strings.Contains(h.text(ap.Proposal), "Plan: create hello.txt") {
		t.Errorf("proposal text: %q", h.text(ap.Proposal))
	}
	work := h.node(runID, "work")
	runs := h.agentRuns(work.ID)
	if work.Status != store.NodeAwaitingApproval || len(runs) != 1 || runs[0].Phase != store.PhasePropose || runs[0].Status != store.AgentAwaitingApproval ||
		runs[0].Policy != store.PolicyReadonly || runs[0].SessionID == "" || ap.AgentRunID != runs[0].ID {
		t.Fatalf("parked state: node %+v runs %+v", work, runs)
	}
	if !strings.Contains(h.text(runs[0].Prompt), "Review 42") {
		t.Errorf("rendered prompt not stored: %q", h.text(runs[0].Prompt))
	}
	notes := h.notes.kind("proposal")
	if len(notes) != 1 || notes[0].URL != uiBase+"/inbox/approval/"+ap.ID || !strings.Contains(notes[0].Body, "Plan: create hello.txt") {
		t.Fatalf("approval notifications: %+v", notes)
	}
	if _, err := os.Stat(h.stateFile("work", "hello.txt")); err == nil {
		t.Fatal("the plan was executed before approval")
	}

	decided, err := h.eng.DecideApproval(t.Context(), ap.ID, core.ApprovalDecision{Approve: true, Comment: "go ahead", Principal: "me"})
	if err != nil || decided.Status != store.ApprovalApproved || decided.DecidedBy != "me" || decided.Comment != "go ahead" {
		t.Fatalf("decide: %+v %v", decided, err)
	}
	h.waitRun(runID, store.FlowRunSucceeded)

	runs = h.agentRuns(work.ID)
	if len(runs) != 2 {
		t.Fatalf("agent runs: %d, want propose + execute\n%s", len(runs), h.dumpRun(runID))
	}
	p1, p2 := runs[0], runs[1]
	if p1.Status != store.AgentSucceeded || p2.Phase != store.PhaseExecute || p2.Policy != store.PolicyAuto || p2.Status != store.AgentSucceeded ||
		p2.ResumedVia != store.ResumedLoad || p2.SessionID != p1.SessionID || p2.PrevAgentRunID != p1.ID {
		t.Errorf("phases:\n%+v\n%+v", p1, p2)
	}
	if prompt := h.text(p2.Prompt); !strings.HasPrefix(prompt, agent.ApprovedPrompt) || !strings.Contains(prompt, "go ahead") {
		t.Errorf("execute prompt: %q", prompt)
	}
	if b, err := os.ReadFile(h.stateFile("work", "hello.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("approved plan not executed: %q %v", b, err)
	}
	if _, ok := inboxTitles(h.inboxItems())["done 42"]; !ok {
		t.Errorf("downstream node did not run: %+v", h.inboxItems())
	}
	if n := h.node(runID, "work"); n.Status != store.NodeDone || n.Decision != store.DecisionEmitted || len(n.Ports) != 1 || n.Ports[0] != "out" {
		t.Errorf("node run after resume: %+v", n)
	}

	if _, err := h.eng.DecideApproval(t.Context(), ap.ID, core.ApprovalDecision{Approve: false}); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("deciding twice: %v, want ErrInvalidState", err)
	}
	if _, err := h.eng.DecideApproval(t.Context(), "01NOPE", core.ApprovalDecision{Approve: true}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown approval: %v", err)
	}
}

// An agent without loadSession gets a new session whose prompt carries the
// original prompt, the proposal and the comment; the fallback is recorded.
func TestProposalResumeFallsBackToNewSession(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.prompt("Review {{.input.id}}")
	h.runtime("fake", proposalScript(false))
	h.flow("f", approveFlow())
	h.start()
	runID := h.injectRun(`{"id":"7"}`)
	h.waitRun(runID, store.FlowRunAwaitingApproval)
	ap := h.waitProposal()
	if _, err := h.eng.DecideApproval(t.Context(), ap.ID, core.ApprovalDecision{Approve: true, Comment: "mind the tests"}); err != nil {
		t.Fatal(err)
	}
	h.waitRun(runID, store.FlowRunSucceeded)
	runs := h.agentRuns(h.node(runID, "work").ID)
	if len(runs) != 2 || runs[1].ResumedVia != store.ResumedNewSession {
		t.Fatalf("runs: %+v", runs)
	}
	prompt := h.text(runs[1].Prompt)
	for _, want := range []string{"Review 7", "Plan: create hello.txt", "mind the tests", agent.ApprovedPrompt} {
		if !strings.Contains(prompt, want) {
			t.Errorf("fallback prompt lacks %q: %q", want, prompt)
		}
	}
	if strings.Contains(prompt, agent.ProposalSuffix) {
		t.Error("the proposal-phase suffix leaked into the execute prompt")
	}
}

// Scenario 7, deny: the denial (without steering text) is recorded, nothing runs, and the node routes
// to its denied port. A denial with steering revises the proposal instead (e2e/approvals_steering_test.go).
func TestScenario7ProposalDenyRoutesToDenied(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.prompt("Review {{.input.id}}")
	h.runtime("fake", proposalScript(true))
	h.flow("f", approveFlow())
	h.start()
	runID := h.injectRun(`{"id":"43"}`)
	h.waitRun(runID, store.FlowRunAwaitingApproval)
	ap := h.waitProposal()

	decided, err := h.eng.DecideApproval(t.Context(), ap.ID, core.ApprovalDecision{Approve: false, Principal: "me"})
	if err != nil || decided.Status != store.ApprovalDenied {
		t.Fatalf("decide: %+v %v", decided, err)
	}
	h.waitRun(runID, store.FlowRunSucceeded)
	work := h.node(runID, "work")
	runs := h.agentRuns(work.ID)
	if work.Status != store.NodeDone || len(work.Ports) != 1 || work.Ports[0] != "denied" || work.Error != "denied" || len(runs) != 1 || runs[0].Status != store.AgentDenied {
		t.Fatalf("denied node %+v runs %+v", work, runs)
	}
	items := inboxTitles(h.inboxItems())
	if it, ok := items["refused 43"]; !ok || it.SummaryMD != "" {
		t.Errorf("denied branch: %+v", items)
	}
	if _, ok := items["done 43"]; ok {
		t.Error("the out branch ran after a denial")
	}
	if _, err := os.Stat(h.stateFile("work", "hello.txt")); err == nil {
		t.Error("a denied plan was executed")
	}
}

func (h *harness) promptNote(name string) {
	h.t.Helper()
	h.write("prompts/"+name+".md", "---\nname: "+name+"\ndescription: test\noutput: note\n---\nTriage {{.input.id}}")
}

// A readonly agent produces a structured note: an inbox item, a notification
// with a deep link, and a stored transcript.
func TestReadonlyAgentNoteBecomesInboxItem(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.promptNote("triage")
	h.runtime("fake", script{Turns: []turn{{When: whenMode("default"), Steps: []step{
		{Op: fakeagent.OpThought, Text: "thinking"},
		{Op: fakeagent.OpPermission, ID: "r1", Kind: "read", Title: "Read thread"},
		say(`All good. {"title":"Deploy is failing","priority":"high","summary_md":"Roll back.","suggested_reply":"On it","links":[{"label":"PR","url":"https://example.com/pr"}]}`),
	}}}})
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: triage, type: agent, runtime: fake, mode: readonly, prompt: triage}
`)
	h.start()
	runID := h.injectRun(`{"id":"9"}`)
	h.waitRun(runID, store.FlowRunSucceeded)

	items := h.inboxItems()
	if len(items) != 1 {
		t.Fatalf("inbox: %+v", items)
	}
	it := items[0]
	run := h.agentRuns(h.node(runID, "triage").ID)[0]
	if it.Title != "Deploy is failing" || it.Priority != store.PriorityHigh || it.SummaryMD != "Roll back." || it.SuggestedReply != "On it" || len(it.Links) != 1 ||
		it.SourceType != store.SourceAgentRun || it.AgentRunID != run.ID || it.FlowRunID != runID || it.ParseError || it.DryRun {
		t.Errorf("inbox item: %+v", it)
	}
	notes := h.notes.kind("note")
	if len(notes) != 1 || notes[0].URL != uiBase+"/inbox/"+it.ID || notes[0].Subtitle != "Deploy is failing" {
		t.Errorf("notifications: %+v", notes)
	}
	if run.Mode != store.ModeReadonly || run.Policy != store.PolicyReadonly || run.Status != store.AgentSucceeded || run.AgentInfo == nil ||
		len(run.Capabilities) == 0 || run.StopReason != "end_turn" || len(run.Argv) == 0 || run.Cwd == "" {
		t.Errorf("agent run: %+v", run)
	}
	if res := h.text(run.Result); !strings.Contains(res, `"title":"Deploy is failing"`) || !strings.Contains(res, `"parse_error":false`) {
		t.Errorf("result JSON: %s", res)
	}
	if raw := h.text(run.RawJSONRPC); !strings.Contains(raw, "session/prompt") || !strings.Contains(raw, `"dir":"->"`) {
		t.Errorf("raw JSON-RPC: %.200s", raw)
	}
	evs, err := h.st.ListAgentEvents(t.Context(), store.AgentEventFilter{AgentRunID: run.ID})
	if err != nil || len(evs) < 3 {
		t.Fatalf("transcript: %d events %v", len(evs), err)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d", i, ev.Seq)
		}
	}
	perms, _ := h.st.ListPermissionRequests(t.Context(), store.PermissionRequestFilter{AgentRunID: run.ID})
	if len(perms.Items) != 1 || perms.Items[0].Decision != store.PermReject || perms.Items[0].MatchedRule != "lockdown" {
		t.Errorf("permission requests: %+v", perms.Items)
	}
}

// A final message that is not a note still reaches the inbox, flagged parse_error.
func TestUnparseableNoteFallsBackToRawMessage(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.promptNote("triage")
	h.runtime("fake", script{Turns: []turn{{Steps: []step{say("I could not decide.")}}}})
	h.flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: triage, type: agent, runtime: fake, mode: readonly, prompt: triage}\n")
	h.start()
	h.waitRun(h.injectRun(`{"id":"1"}`), store.FlowRunSucceeded)
	items := h.inboxItems()
	if len(items) != 1 || !items[0].ParseError || items[0].Priority != store.PriorityNormal || !strings.Contains(items[0].SummaryMD, "I could not decide.") {
		t.Fatalf("inbox: %+v", items)
	}
}

// Regression: readonly mode used to parse the final message as a note whatever the output, so an agent with output
// none (inline or from its prompt file) produced an "Unstructured agent result" item. Only output: note makes a note.
func TestReadonlyAgentWithoutNoteOutputMakesNoInboxItem(t *testing.T) {
	for name, node := range map[string]string{
		"inline none":      "{id: a, type: agent, runtime: fake, mode: readonly, instructions: Look., output: none}",
		"prompt file none": "{id: a, type: agent, runtime: fake, mode: readonly, prompt: p}",
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.idleSensor()
			h.prompt("Look at {{.input.id}}")
			h.runtime("fake", script{Turns: []turn{{Steps: []step{say("just free text")}}}})
			h.flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - "+node+"\n")
			h.start()
			runID := h.injectRun(`{"id":"1"}`)
			h.waitRun(runID, store.FlowRunSucceeded)
			if items := h.inboxItems(); len(items) != 0 {
				t.Fatalf("inbox: %+v", items)
			}
			run := h.agentRuns(h.node(runID, "a").ID)[0]
			if !strings.Contains(h.text(run.Result), `"parse_error":false`) {
				t.Errorf("result: %s", h.text(run.Result))
			}
		})
	}
}

// Scenario 9 (agent part): in a dry-run flow an auto-mode agent is forced to
// read-only and can not change anything; its note is tagged dry-run.
func TestScenario9DryRunForcesAgentReadonly(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.promptNote("triage")
	h.runtime("fake", script{Turns: []turn{
		{When: whenMode("default"), Steps: []step{say(`{"title":"looked","priority":"normal","summary_md":"ok"}`)}},
		{When: whenMode("bypass"), Steps: []step{{Op: fakeagent.OpWriteFile, Path: "oops.txt", Data: "x"}, say(`{"title":"acted"}`)}},
	}})
	h.flow("f", "id: f\nenabled: true\ndry_run: true\nsubscribe: [src]\nnodes:\n  - {id: act, type: agent, runtime: fake, mode: auto, prompt: triage}\n")
	h.start()
	runID := h.injectRun(`{"id":"1"}`)
	h.waitRun(runID, store.FlowRunSucceeded)
	run := h.agentRuns(h.node(runID, "act").ID)[0]
	if run.Mode != store.ModeReadonly || run.Policy != store.PolicyReadonly {
		t.Errorf("dry-run agent: %+v", run)
	}
	if _, err := os.Stat(h.stateFile("act", "oops.txt")); err == nil {
		t.Error("dry-run agent changed a file")
	}
	items := h.inboxItems()
	if len(items) != 1 || items[0].Title != "looked" || !items[0].DryRun {
		t.Errorf("inbox: %+v", items)
	}
}

// gitRepo makes a repo with one commit on main, with git's repository
// environment cleared (hooks export GIT_DIR into test processes).
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = dir
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "GIT_") {
				env = append(env, kv)
			}
		}
		env = append(env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	return dir
}

// Scenario 8: an auto agent in a worktree cwd changes the isolated worktree,
// never the source repo; the worktree is removed when the run succeeds and kept
// when it fails.
func TestScenario8AutoAgentWorksInIsolatedWorktree(t *testing.T) {
	repo := gitRepo(t)
	out := t.TempDir()
	h := newHarness(t)
	h.idleSensor()
	h.prompt("{{.input.id}}")
	h.runtime("fake", script{Turns: []turn{
		{When: &fakeagent.When{Mode: "bypass", PromptMatches: "fail"}, Steps: []step{
			{Op: fakeagent.OpWriteFile, Path: "partial.txt", Data: "x"},
			{Op: fakeagent.OpError, Text: "boom"},
		}},
		{When: whenMode("bypass"), Steps: []step{
			{Op: fakeagent.OpWriteFile, Path: "changed.txt", Data: "agent was here\n"},
			{Op: fakeagent.OpExec, Cmd: []string{"sh", "-c", "pwd > " + out + "/cwd && cp changed.txt " + out + "/changed.txt"}},
			say("done"),
		}},
	}})
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: auto
    prompt: p
    cwd: {mode: worktree, repo: "`+repo+`", ref: main}
`)
	h.start()
	runID := h.injectRun(`{"id":"ok"}`)
	h.waitRun(runID, store.FlowRunSucceeded)
	run := h.agentRuns(h.node(runID, "work").ID)[0]
	wtDir := filepath.Join(h.home, "data", "worktrees")
	if run.Mode != store.ModeAuto || run.Policy != store.PolicyAuto || !strings.HasPrefix(run.Cwd, wtDir+string(filepath.Separator)) || run.WorktreePath != run.Cwd {
		t.Fatalf("agent run cwd %q worktree %q", run.Cwd, run.WorktreePath)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "cwd")); !strings.Contains(strings.TrimSpace(string(b)), "worktrees") {
		t.Errorf("agent ran in %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(out, "changed.txt")); err != nil || string(b) != "agent was here\n" {
		t.Errorf("agent's change: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "changed.txt")); err == nil {
		t.Error("the source repo was changed")
	}
	if _, err := os.Stat(run.Cwd); err == nil {
		t.Error("worktree of a succeeded run was not removed")
	}

	failID := h.injectRun(`{"id":"fail"}`)
	h.waitRun(failID, store.FlowRunFailed)
	failed := h.agentRuns(h.node(failID, "work").ID)[0]
	if failed.Status != store.AgentFailed || failed.Cwd == "" {
		t.Fatalf("failed agent run: %+v", failed)
	}
	if _, err := os.Stat(filepath.Join(failed.Cwd, "partial.txt")); err != nil {
		t.Errorf("worktree of a failed run must be kept for debugging: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "partial.txt")); err == nil {
		t.Error("the source repo was changed by the failing run")
	}
	if n := h.node(failID, "work"); n.Status != store.NodeFailed || n.Error == "" || n.Reason == "" {
		t.Errorf("failed node: %+v", n)
	}
}

// cwd: a plain string is a directory the agent runs in (templated); the default
// is the node's state dir.
func TestAgentRunsInConfiguredDirectory(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t)
	h.idleSensor()
	h.prompt("go")
	h.runtime("fake", script{Turns: []turn{{Steps: []step{{Op: fakeagent.OpWriteFile, Path: "here.txt", Data: "x"}, say("ok")}}}})
	h.flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: work, type: agent, runtime: fake, mode: auto, prompt: p, cwd: \"{{.input.dir}}\"}\n")
	h.start()
	runID := h.injectRun(`{"id":"1","dir":"` + dir + `"}`)
	h.waitRun(runID, store.FlowRunSucceeded)
	run := h.agentRuns(h.node(runID, "work").ID)[0]
	if run.Cwd != dir || run.WorktreePath != "" {
		t.Errorf("agent run cwd %q worktree %q, want %q", run.Cwd, run.WorktreePath, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "here.txt")); err != nil {
		t.Errorf("agent did not run in the configured directory: %v", err)
	}

	bad := h.injectRun(`{"id":"2","dir":"/nonexistent/dir"}`)
	h.waitRun(bad, store.FlowRunFailed)
	if n := h.node(bad, "work"); n.Status != store.NodeFailed || !strings.Contains(n.Reason, "not an existing directory") {
		t.Errorf("missing directory: %+v", n)
	}
}

// Scenario 12: with max_agent_runs 2, five agent-bound events leave two agent
// nodes running and three queued (visible), then all complete.
func TestScenario12AgentConcurrencyCap(t *testing.T) {
	h := newHarness(t, withConfig("max_agent_runs: 2\n"))
	gate := filepath.Join(t.TempDir(), "gate")
	h.idleSensor()
	h.prompt("go")
	h.runtime("fake", script{Turns: []turn{{Steps: []step{
		{Op: fakeagent.OpExec, Cmd: []string{"sh", "-c", "while [ ! -f " + gate + " ]; do sleep 0.02; done"}},
		say("ok"),
	}}}})
	h.flow("f", `id: f
enabled: true
subscribe: [src]
max_concurrent: 10
nodes:
  - {id: work, type: agent, runtime: fake, mode: auto, prompt: p}
`)
	h.start()
	var runs []string
	for _, id := range []string{"1", "2", "3", "4", "5"} {
		runs = append(runs, h.injectRun(`{"id":"`+id+`"}`))
	}
	count := func(st store.NodeRunStatus) int {
		page, err := h.st.ListNodeRuns(t.Context(), store.NodeRunFilter{NodeType: store.NodeAgent, Statuses: []store.NodeRunStatus{st}, Slim: true, ListOpts: store.ListOpts{Limit: 50}})
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Items)
	}
	h.eventually("2 agent nodes running and 3 queued", func() bool { return count(store.NodeRunning) == 2 && count(store.NodeQueued) == 3 })
	time.Sleep(100 * time.Millisecond) // absence check: no node may start beyond the cap
	if count(store.NodeRunning) != 2 || count(store.NodeQueued) != 3 {
		t.Fatalf("cap not held: %d running %d queued", count(store.NodeRunning), count(store.NodeQueued))
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range runs {
		h.waitRun(id, store.FlowRunSucceeded)
	}
	if count(store.NodeRunning) != 0 || count(store.NodeQueued) != 0 {
		t.Errorf("work left over: %d running %d queued", count(store.NodeRunning), count(store.NodeQueued))
	}
}

// Command nodes share a pool (max_command_runs) and a flow runs at most
// max_concurrent runs at once; excess work waits as queued, visible in the store.
func TestCommandPoolAndFlowConcurrencyQueueExcessWork(t *testing.T) {
	for _, tc := range []struct {
		name, config, flowMax string
		queuedKind            string // what is queued while the first runs
	}{
		{"command pool", "max_command_runs: 1\n", "10", "node"},
		{"flow max_concurrent", "", "1", "flow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, withConfig(tc.config))
			gate := filepath.Join(t.TempDir(), "gate")
			h.idleSensor()
			h.flow("f", `id: f
enabled: true
subscribe: [src]
max_concurrent: `+tc.flowMax+`
nodes:
  - id: wait
    type: command
    lang: bash
    script: |
      cat
      while [ ! -f `+gate+` ]; do sleep 0.02; done
`)
			h.start()
			var runs []string
			for _, id := range []string{"1", "2", "3"} {
				runs = append(runs, h.injectRun(`{"id":"`+id+`"}`))
			}
			nodes := func(st store.NodeRunStatus) int {
				page, _ := h.st.ListNodeRuns(t.Context(), store.NodeRunFilter{Statuses: []store.NodeRunStatus{st}, Slim: true, ListOpts: store.ListOpts{Limit: 50}})
				return len(page.Items)
			}
			flows := func(st store.FlowRunStatus) int {
				page, _ := h.st.ListFlowRuns(t.Context(), store.FlowRunFilter{Statuses: []store.FlowRunStatus{st}, ListOpts: store.ListOpts{Limit: 50}})
				return len(page.Items)
			}
			h.eventually("one running, two waiting", func() bool {
				if tc.queuedKind == "node" {
					return nodes(store.NodeRunning) == 1 && nodes(store.NodeQueued) == 2
				}
				return flows(store.FlowRunRunning) == 1 && flows(store.FlowRunQueued) == 2
			})
			time.Sleep(100 * time.Millisecond) // absence check: no run may start beyond the cap
			if nodes(store.NodeRunning) != 1 {
				t.Fatalf("cap exceeded: %d running nodes", nodes(store.NodeRunning))
			}
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, id := range runs {
				h.waitRun(id, store.FlowRunSucceeded)
			}
		})
	}
}

// A runtime without a command whose agent is not installed fails the node with the reason and the fix, not with an
// empty-command error.
func TestAgentNodeOfAnUninstalledRuntimeFailsWithTheFix(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.promptNote("review")
	h.runtimes["claude"] = "  claude:\n    kind: claude\n"
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - {id: triage, type: agent, runtime: claude, mode: readonly, prompt: review}
`)
	h.start()
	runID := h.injectRun(`{"id":"1"}`)
	h.waitRun(runID, store.FlowRunFailed)
	n := h.node(runID, "triage")
	for _, want := range []string{`runtime "claude" is not available`, "hal9001 runtimes install claude"} {
		if !strings.Contains(n.Reason, want) {
			t.Errorf("node reason %q lacks %q", n.Reason, want)
		}
	}
}
