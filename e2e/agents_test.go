package e2e

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent/fakeagent"
)

const proposalPlan = "Plan: create hello.txt"

// proposalScript plans in the ask mode and executes (writes hello.txt) in the
// full mode, as an approve+proposal agent does.
func proposalScript(load bool) script {
	return script{LoadSession: load, Turns: []turn{
		{When: inMode("default"), Steps: []step{say(proposalPlan)}},
		{When: inMode("bypass"), Steps: []step{
			{Op: fakeagent.OpWriteFile, Path: "hello.txt", Data: "hi\n"},
			say("executed"),
		}},
	}}
}

// workFile is a file in the default cwd of node `work` of flow `flow`.
func (d *Daemon) workFile(flow, name string) string {
	return d.Path("data/state/" + flow + "/work/" + name)
}

func (d *Daemon) fileThere(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// An agent node with inline instructions (no prompt file) renders its template
// and produces an inbox note.
func TestInlineInstructionsAgentNote(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		say(`Done. {"title":"Inline note","priority":"low","summary_md":"ok"}`),
	}}}})
	d.Flow("inline", `id: inline
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: |
      Triage {{.input.id}}
`)
	d.Start()

	run := d.RunOf("src", map[string]any{"id": "7"})
	runs := d.AgentRuns(d.Node(run, "work").ID)
	if len(runs) != 1 || !strings.Contains(runs[0].RenderedPrompt, "Triage 7") {
		t.Fatalf("agent runs: %+v", runs)
	}
	if items := d.Inbox(); len(items) != 1 || items[0].Title != "Inline note" {
		t.Fatalf("inbox: %+v", items)
	}
}

// Scenario 6: a readonly agent produces an inbox item with title and priority,
// its transcript and permission requests are visible through the API, and a
// notification deep-links to the item. (The notification is recorded by a fake
// terminal-notifier on PATH.)
func TestScenario06ReadonlyAgentNote(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "note", "Triage {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		{Op: fakeagent.OpThought, Text: "thinking about it"},
		{Op: fakeagent.OpPermission, ID: "r1", Kind: "read", Title: "Read thread", RawInput: map[string]any{"path": "thread.txt"}},
		say(`All good. {"title":"Deploy is failing","priority":"high","summary_md":"Roll back **now**.","suggested_reply":"On it","links":[{"label":"PR","url":"https://example.com/pr/1"}]}`),
	}}}})
	d.Flow("triage", `id: triage
enabled: true
subscribe: [src]
nodes:
  - {id: work, type: agent, runtime: fake, mode: readonly, prompt: p}
`)
	d.Start()

	run := d.RunOf("src", map[string]any{"id": "9"})
	work := d.Node(run, "work")
	runs := d.AgentRuns(work.ID)
	if work.Status != "done" || work.Decision != "emitted" || len(runs) != 1 {
		t.Fatalf("agent node: %+v runs %+v", work, runs)
	}
	ar := runs[0]
	if ar.Mode != "readonly" || ar.Policy != "readonly" || ar.Status != "succeeded" || ar.StopReason != "end_turn" || ar.SessionID == "" ||
		ar.AgentInfo == nil || ar.AgentInfo.Name == "" || !strings.Contains(ar.RenderedPrompt, "Triage 9") || len(ar.Argv) == 0 {
		t.Errorf("agent run: %+v", ar)
	}
	if !strings.Contains(string(ar.Result), "Deploy is failing") || ar.ParseError {
		t.Errorf("result %s parse_error=%v", ar.Result, ar.ParseError)
	}

	items := d.Inbox()
	if len(items) != 1 {
		t.Fatalf("inbox: %+v", items)
	}
	it := items[0]
	if it.Title != "Deploy is failing" || it.Priority != "high" || it.Source != "agent_run" || it.AgentRunID != ar.ID || it.FlowRunID != run.FlowRun.ID ||
		it.SuggestedReply != "On it" || len(it.Links) != 1 || it.Links[0].URL != "https://example.com/pr/1" || it.SummaryMD != "Roll back **now**." || it.ParseError || it.DryRun {
		t.Errorf("inbox item: %+v", it)
	}

	// Transcript: thought, tool call for the read, final message.
	kinds := map[string]bool{}
	for _, ev := range d.AgentEvents(ar.ID) {
		kinds[ev.Kind] = true
	}
	for _, k := range []string{"thought", "tool_call", "message"} {
		if !kinds[k] {
			t.Errorf("transcript lacks a %s event: %v", k, kinds)
		}
	}
	perms := d.Permissions(ar.ID)
	if len(perms) != 1 || perms[0].ToolKind != "read" || perms[0].Decision != "allow" || perms[0].DecidedBy != "policy" || perms[0].MatchedRule != "kind:read" {
		t.Errorf("permission requests: %+v", perms)
	}
	if raw := d.Get("/api/agent-runs/" + ar.ID + "/raw").Want(200); !strings.Contains(string(raw.Body), "session/prompt") {
		t.Errorf("raw JSON-RPC log lacks session/prompt: %.200s", raw.Body)
	}

	// The notification deep-links to the inbox item.
	var note map[string]string
	d.Eventually("the inbox notification", func() bool {
		for _, n := range d.Notifications() {
			if strings.Contains(n["open"], "/inbox/") {
				note = n
				return true
			}
		}
		return false
	})
	if want := "http://localhost:" + strconv.Itoa(d.Port()) + "/inbox/" + it.ID; note["open"] != want || !strings.Contains(note["title"], "Deploy is failing") {
		t.Errorf("notification %v, want open=%s", note, want)
	}
}

// readonlyFlow is a flow with one readonly agent node `work` (no denied port).
func readonlyFlow(extra string) string {
	return `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    prompt: p
` + extra
}

func approvalFlow(style, extra string) string {
	return agentFlow("f", "src", "approve", "    approve: "+style+"\n"+extra)
}

// Scenario 7, approve: the proposal is queued with a notification; approving
// resumes the same session (session/load) under the auto policy and the DAG
// continues. Denying routes to the `denied` port. Both decisions are in the trace.
func TestScenario07ProposalApproveResumesSameSession(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", proposalScript(true))
	d.Flow("f", approvalFlow("proposal", ""))
	d.Start()

	runID := d.InjectRun("src", map[string]any{"id": "42"})
	parked := d.WaitRun(runID, "awaiting_approval")
	ap := d.WaitApproval("proposal")
	work := d.Node(parked, "work")
	runs := d.AgentRuns(work.ID)
	if !strings.Contains(ap.ProposalMD, proposalPlan) || ap.FlowRunID != runID || ap.NodeID != "work" || ap.Runtime != "fake" || ap.Status != "pending" || ap.PermissionRequest != nil {
		t.Errorf("approval: %+v", ap)
	}
	if work.Status != "awaiting_approval" || len(runs) != 1 || runs[0].Phase != "propose" || runs[0].Status != "awaiting_approval" || runs[0].Policy != "readonly" ||
		runs[0].SessionID == "" || ap.AgentRunID != runs[0].ID || runs[0].ApproveStyle != "proposal" || !strings.Contains(runs[0].RenderedPrompt, "Review 42") {
		t.Fatalf("parked state: node %+v runs %+v", work, runs)
	}
	if d.fileThere(d.workFile("f", "hello.txt")) {
		t.Fatal("the plan was executed before approval")
	}
	d.Eventually("the approval notification", func() bool {
		for _, n := range d.Notifications() {
			if n["open"] == "http://localhost:"+strconv.Itoa(d.Port())+"/inbox/approval/"+ap.ID {
				return strings.Contains(n["message"], proposalPlan)
			}
		}
		return false
	})
	if st := d.GetStatus(); st.PendingApprovals != 1 {
		t.Errorf("status: %+v", st)
	}

	decided := d.Decide(ap.ID, true, map[string]any{"comment": "go ahead"})
	if decided.Status != "approved" || decided.Comment != "go ahead" || decided.DecidedBy == "" {
		t.Errorf("decision: %+v", decided)
	}
	done := d.WaitRun(runID, "succeeded")
	runs = d.AgentRuns(d.Node(done, "work").ID)
	if len(runs) != 2 {
		t.Fatalf("agent runs: %d, want propose + execute\n%s", len(runs), describeRun(done))
	}
	p1, p2 := runs[0], runs[1]
	if p1.Status != "succeeded" || p2.Phase != "execute" || p2.Policy != "auto" || p2.Status != "succeeded" || p2.ResumedVia != "load" || p2.SessionID != p1.SessionID {
		t.Errorf("phases:\n%+v\n%+v", p1, p2)
	}
	if !strings.HasPrefix(p2.RenderedPrompt, "Approved. Execute the plan.") || !strings.Contains(p2.RenderedPrompt, "go ahead") {
		t.Errorf("execute prompt: %q", p2.RenderedPrompt)
	}
	if b, err := os.ReadFile(d.workFile("f", "hello.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("approved plan not executed: %q %v", b, err)
	}
	var loaded bool
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" && c["session_id"] == p1.SessionID {
			loaded = true
		}
	}
	if !loaded {
		t.Errorf("the agent never received session/load for %s: %v", p1.SessionID, d.AgentCalls("fake"))
	}
	if _, ok := d.InboxByTitle()["done 42"]; !ok {
		t.Errorf("downstream node did not run: %+v", d.Inbox())
	}
	if n := d.Node(done, "work"); n.Status != "done" || n.Decision != "emitted" || len(n.Ports) != 1 || n.Ports[0] != "out" {
		t.Errorf("node run after resume: %+v", n)
	}
	// The decision is part of the trace.
	var detail AgentRunDetail
	d.Get("/api/agent-runs/" + p1.ID).Want(200).JSON(&detail)
	if len(detail.Approvals) != 1 || detail.Approvals[0].Status != "approved" || len(detail.PhaseRunIDs) != 2 {
		t.Errorf("agent run detail: %+v", detail)
	}

	// Deciding twice or deciding an unknown approval is an error.
	if r := d.Post("/api/approvals/"+ap.ID+"/deny", nil).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("deciding twice: %s", r)
	}
	d.Post("/api/approvals/01JC7Z3K9Q8V5T2M4N6P8R0S1T/approve", nil).Want(404)
}

func TestScenario07ProposalDenyRoutesToDeniedPort(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", proposalScript(true))
	d.Flow("f", approvalFlow("proposal", ""))
	d.Start()

	runID := d.InjectRun("src", map[string]any{"id": "43"})
	d.WaitRun(runID, "awaiting_approval")
	ap := d.WaitApproval("proposal")
	decided := d.Decide(ap.ID, false, map[string]any{"comment": "too risky"})
	if decided.Status != "denied" || decided.Comment != "too risky" {
		t.Errorf("decision: %+v", decided)
	}
	done := d.WaitRun(runID, "succeeded")
	work := d.Node(done, "work")
	runs := d.AgentRuns(work.ID)
	if work.Status != "done" || len(work.Ports) != 1 || work.Ports[0] != "denied" || work.Error != "denied" || len(runs) != 1 || runs[0].Status != "denied" {
		t.Fatalf("denied node %+v runs %+v", work, runs)
	}
	items := d.InboxByTitle()
	if it, ok := items["refused 43"]; !ok || it.SummaryMD != "too risky" {
		t.Errorf("denied branch: %+v", items)
	}
	if _, ok := items["done 43"]; ok {
		t.Error("the out branch ran after a denial")
	}
	if d.fileThere(d.workFile("f", "hello.txt")) {
		t.Error("a denied plan was executed")
	}
	// A cancel_run on a proposal is invalid; the approval stays decided.
	d.Post("/api/approvals/"+ap.ID+"/deny", map[string]any{"cancel_run": true}).Want(409, 422)
}

// An agent that does not advertise loadSession gets a new session whose prompt
// carries the original prompt, the proposal and the comment; the fallback is
// recorded on the agent run.
func TestScenario07ProposalResumeFallsBackToNewSession(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", proposalScript(false))
	d.Flow("f", approvalFlow("proposal", ""))
	d.Start()

	runID := d.InjectRun("src", map[string]any{"id": "7"})
	d.WaitRun(runID, "awaiting_approval")
	ap := d.WaitApproval("proposal")
	d.Decide(ap.ID, true, map[string]any{"comment": "mind the tests"})
	done := d.WaitRun(runID, "succeeded")
	runs := d.AgentRuns(d.Node(done, "work").ID)
	if len(runs) != 2 || runs[1].ResumedVia != "new_session" || runs[1].ResumeFallback == "" || runs[1].SessionID == runs[0].SessionID {
		t.Fatalf("runs: %+v", runs)
	}
	for _, want := range []string{"Review 7", proposalPlan, "mind the tests", "Approved. Execute the plan."} {
		if !strings.Contains(runs[1].RenderedPrompt, want) {
			t.Errorf("fallback prompt lacks %q: %q", want, runs[1].RenderedPrompt)
		}
	}
	if b, err := os.ReadFile(d.workFile("f", "hello.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("approved plan not executed: %q %v", b, err)
	}
}

func perToolScript() script {
	return script{Turns: []turn{{When: inMode("default"), Steps: []step{
		{Op: fakeagent.OpPermission, ID: "r1", Kind: "read", Title: "Read README", RawInput: map[string]any{"path": "README.md"}},
		editPermission("e1", "note.txt", "edited\n", say("edit-was-rejected")),
		say("finished"),
	}}}}
}

func perToolDaemon(t *testing.T, extra string, opts ...Option) *Daemon {
	t.Helper()
	d := newDaemon(t, opts...)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", perToolScript())
	d.Flow("f", approvalFlow("per_tool", extra))
	d.Start()
	return d
}

// Scenario 7a: a read is allowed by policy without asking; an edit appears in
// the approval queue with its diff and Allow applies it.
func TestScenario07aPerToolAllow(t *testing.T) {
	d := perToolDaemon(t, "")
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	ap := d.WaitApproval("tool_call")
	parked := d.WaitRun(runID, "awaiting_approval")
	work := d.Node(parked, "work")
	runs := d.AgentRuns(work.ID)
	if work.Status != "awaiting_approval" || len(runs) != 1 || runs[0].Status != "awaiting_approval" || runs[0].Policy != "gated" || runs[0].ApproveStyle != "per_tool" {
		t.Fatalf("waiting state: node %+v runs %+v", work, runs)
	}
	perms := d.Permissions(runs[0].ID)
	if len(perms) != 2 {
		t.Fatalf("permission requests: %+v", perms)
	}
	read, edit := perms[0], perms[1]
	if read.ToolKind != "read" || read.Decision != "allow" || read.DecidedBy != "policy" || read.ApprovalID != "" {
		t.Errorf("read request: %+v", read)
	}
	if edit.ToolKind != "edit" || edit.Decision != "" || ap.PermissionRequest == nil || ap.PermissionRequest.ID != edit.ID || edit.ApprovalID != ap.ID || !strings.Contains(string(edit.RawInput), "note.txt") {
		t.Errorf("edit request %+v / approval %+v", edit, ap)
	}
	if ap.ExpiresAt == "" {
		t.Error("tool approval has no expiry")
	}
	// The diff is part of the tool call in the transcript.
	var diff bool
	for _, ev := range d.AgentEvents(runs[0].ID) {
		if ev.ToolCallID == "e1" && strings.Contains(string(ev.Payload), `"diff"`) && strings.Contains(string(ev.Payload), "edited") {
			diff = true
		}
	}
	if !diff {
		t.Errorf("the transcript has no diff for the pending edit:\n%s", d.Transcript(runs[0].ID))
	}
	d.Eventually("the approval notification", func() bool {
		for _, n := range d.Notifications() {
			if n["open"] == "http://localhost:"+strconv.Itoa(d.Port())+"/inbox/approval/"+ap.ID {
				return true
			}
		}
		return false
	})
	if d.fileThere(d.workFile("f", "note.txt")) {
		t.Fatal("edit applied before approval")
	}

	d.Decide(ap.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	if b, err := os.ReadFile(d.workFile("f", "note.txt")); err != nil || string(b) != "edited\n" {
		t.Errorf("allowed edit: %q %v", b, err)
	}
	for _, p := range d.Permissions(runs[0].ID) {
		if p.ID == edit.ID && (p.Decision != "allow" || p.DecidedBy != "user") {
			t.Errorf("edit request after decision: %+v", p)
		}
	}
	if _, ok := d.InboxByTitle()["done 1"]; !ok {
		t.Error("downstream did not run")
	}
	if n := d.Node(done, "work"); n.Status != "done" {
		t.Errorf("node: %+v", n)
	}
}

// Scenario 7a: Deny rejects the call and the agent continues; Deny + cancel
// ends the run on port `denied`.
func TestScenario07aPerToolDeny(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
	}{{"agent continues", false}, {"cancel the run", true}} {
		t.Run(tc.name, func(t *testing.T) {
			d := perToolDaemon(t, "")
			runID := d.InjectRun("src", map[string]any{"id": "2"})
			ap := d.WaitApproval("tool_call")
			decided := d.Decide(ap.ID, false, map[string]any{"comment": "no thanks", "cancel_run": tc.cancel})
			if decided.Status != "denied" || decided.Comment != "no thanks" || decided.CancelRun == nil || *decided.CancelRun != tc.cancel {
				t.Errorf("decision: %+v", decided)
			}
			done := d.WaitRun(runID, "succeeded")
			if d.fileThere(d.workFile("f", "note.txt")) {
				t.Error("denied edit was applied")
			}
			work := d.Node(done, "work")
			run := d.AgentRuns(work.ID)[0]
			var perm PermissionRequest
			for _, p := range d.Permissions(run.ID) {
				if p.ID == ap.PermissionRequest.ID {
					perm = p
				}
			}
			titles := d.InboxByTitle()
			if tc.cancel {
				if run.Status != "denied" || len(work.Ports) != 1 || work.Ports[0] != "denied" || perm.Decision != "cancelled" {
					t.Errorf("cancelled: run %+v node %+v perm %+v", run, work, perm)
				}
				if _, ok := titles["refused 2"]; !ok {
					t.Errorf("denied branch missing: %v", titles)
				}
				return
			}
			if run.Status != "succeeded" || perm.Decision != "reject" || perm.DecidedBy != "user" || !strings.Contains(perm.Reason, "no thanks") {
				t.Errorf("rejected call: run %+v perm %+v", run, perm)
			}
			if _, ok := titles["done 2"]; !ok {
				t.Errorf("out branch missing: %v", titles)
			}
			if !strings.Contains(d.Transcript(run.ID), "edit-was-rejected") {
				t.Error("the agent did not continue after the rejection")
			}
		})
	}
}

// Scenario 7a: an approval left undecided past approval_timeout rejects the
// request, expires the approval and fails the run with `approval_timeout`.
func TestScenario07aApprovalTimeout(t *testing.T) {
	d := perToolDaemon(t, "    timeout: 1h\n    approval_timeout: 1s\n", withClock())
	runID := d.InjectRun("src", map[string]any{"id": "3"})
	ap := d.WaitApproval("tool_call")
	// Pending: the src sensor's schedule (1h), the run timeout and the approval timeout.
	if !d.Clock.WaitTimers(3, 30*time.Second) {
		t.Fatal("the schedule, run and approval timers were not registered")
	}
	d.Clock.Advance(2 * time.Second)
	failed := d.WaitRun(runID, "failed")

	n := d.Node(failed, "work")
	if n.Status != "failed" || n.Error != "approval_timeout" {
		t.Errorf("node run: %+v", n)
	}
	var got Approval
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&got)
	var perm PermissionRequest
	for _, p := range d.Permissions(got.AgentRunID) {
		if p.ID == got.PermissionRequest.ID {
			perm = p
		}
	}
	if got.Status != "expired" || perm.Decision != "expired" || perm.DecidedBy != "timeout" {
		t.Errorf("approval %+v permission %+v", got, perm)
	}
	if d.fileThere(d.workFile("f", "note.txt")) {
		t.Error("edit applied after the timeout")
	}
	if r := d.Post("/api/approvals/"+ap.ID+"/approve", nil).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("approving an expired approval: %s", r)
	}
}

// Scenario 7b: a readonly agent asked to edit a file or post via an MCP tool is
// rejected by policy, the rejection is recorded with its reason, and the file
// is unchanged.
func TestScenario07bReadonlyRejectsEditAndMCP(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		editPermission("e1", "target.txt", "changed\n", say("edit-blocked")),
		{
			Op: fakeagent.OpPermission, ID: "m1", Kind: "other", Title: "mcp__slack__post_message", RawInput: map[string]any{"channel": "#general", "text": "hi"},
			OnAllow: []step{{Op: fakeagent.OpWriteFile, Path: "posted.txt", Data: "posted\n"}}, OnReject: []step{say("post-blocked")},
		},
		say("finished"),
	}}}})
	d.Flow("f", readonlyFlow(""))
	if err := os.MkdirAll(d.Path("data/state/f/work"), 0o750); err != nil {
		t.Fatal(err)
	}
	d.Write("data/state/f/work/target.txt", "original\n")
	d.Start()

	run := d.RunOf("src", map[string]any{"id": "1"})
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	if ar.Policy != "readonly" || ar.Status != "succeeded" {
		t.Errorf("agent run: %+v", ar)
	}
	perms := d.Permissions(ar.ID)
	if len(perms) != 2 {
		t.Fatalf("permission requests: %+v", perms)
	}
	for i, kind := range []string{"edit", "other"} {
		p := perms[i]
		if p.ToolKind != kind || p.Decision != "reject" || p.DecidedBy != "policy" || p.Reason == "" || p.ApprovalID != "" {
			t.Errorf("%s request: %+v", kind, p)
		}
	}
	if b := d.Read("data/state/f/work/target.txt"); b != "original\n" {
		t.Errorf("the readonly agent changed the file: %q", b)
	}
	if d.fileThere(d.workFile("f", "posted.txt")) {
		t.Error("the MCP tool call was performed")
	}
	if tr := d.Transcript(ar.ID); !strings.Contains(tr, "edit-blocked") || !strings.Contains(tr, "post-blocked") {
		t.Errorf("the agent was not told about the rejections:\n%s", tr)
	}
	if n := len(d.Approvals("")); n != 0 {
		t.Errorf("policy rejections created %d approvals", n)
	}
}

// Scenario 7b: with allow.commands ['^gh pr view'], `gh pr view` is allowed and
// `gh pr comment` is rejected.
func TestScenario07bAllowCommands(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		execPermission("c1", "gh pr view 12", []step{{Op: fakeagent.OpWriteFile, Path: "viewed.txt", Data: "ok\n"}}),
		execPermission("c2", "gh pr comment 12 --body hi", []step{{Op: fakeagent.OpWriteFile, Path: "commented.txt", Data: "posted\n"}}, say("comment-blocked")),
		execPermission("c3", "gh pr view 12 && gh pr comment 12 --body hi", []step{{Op: fakeagent.OpWriteFile, Path: "chained.txt", Data: "chained\n"}}),
		say("finished"),
	}}}})
	d.Flow("f", readonlyFlow("    allow:\n      commands: ['^gh pr view']\n"))
	d.Start()

	run := d.RunOf("src", map[string]any{"id": "1"})
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	perms := d.Permissions(ar.ID)
	if len(perms) != 3 {
		t.Fatalf("permission requests: %+v", perms)
	}
	if v := perms[0]; v.Decision != "allow" || v.DecidedBy != "policy" || !strings.Contains(v.MatchedRule, "allow.commands[0]") {
		t.Errorf("gh pr view: %+v", v)
	}
	if c := perms[1]; c.Decision != "reject" || c.DecidedBy != "policy" || c.Reason == "" {
		t.Errorf("gh pr comment: %+v", c)
	}
	if c := perms[2]; c.Decision != "reject" || c.DecidedBy != "policy" {
		t.Errorf("a command chaining an allowed and a forbidden one must not match: %+v", c)
	}
	if !d.fileThere(d.workFile("f", "viewed.txt")) || d.fileThere(d.workFile("f", "commented.txt")) || d.fileThere(d.workFile("f", "chained.txt")) {
		t.Error("allowed/rejected commands were not applied as decided")
	}
}

// Scenario 15: a daemon restart marks in-flight runs `interrupted`; a pending
// proposal approval survives and can still be approved (the session is loaded
// by a new agent process); a pending per-tool approval expires with the process.
func TestScenario15RestartInterruptsRunsKeepsProposals(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fakeb", proposalScript(true))
	d.Runtime("fakec", perToolScript())
	d.Flow("fa", `id: fa
enabled: true
subscribe: [src]
nodes:
  - id: sleeper
    type: command
    lang: bash
    timeout: 5m
    script: |
      echo $$ > `+d.Path("sleeper.pid")+`
      sleep 300
`)
	d.Flow("fb", agentFlowRT("fb", "src", "fakeb", "approve", "    approve: proposal\n"))
	d.Flow("fc", agentFlowRT("fc", "src", "fakec", "approve", "    approve: per_tool\n"))
	d.Start()

	res := d.Inject("src", map[string]any{"id": "1"})
	runs := map[string]string{}
	for _, fr := range res.FlowRuns {
		runs[fr.FlowID] = fr.ID
	}
	if len(runs) != 3 {
		t.Fatalf("fan-out: %+v", res.FlowRuns)
	}
	d.WaitRun(runs["fb"], "awaiting_approval")
	d.WaitRun(runs["fc"], "awaiting_approval")
	d.Eventually("the sleeper to run", func() bool {
		return d.GetRun(runs["fa"], 0).FlowRun.Status == "running" && d.fileThere(d.Path("sleeper.pid"))
	})
	proposal := d.WaitApproval("proposal")
	toolCall := d.WaitApproval("tool_call")

	d.Restart()

	if fa := d.GetRun(runs["fa"], 0); fa.FlowRun.Status != "interrupted" || d.Node(fa, "sleeper").Status != "interrupted" {
		t.Errorf("command run after restart:\n%s", describeRun(fa))
	}
	if fc := d.GetRun(runs["fc"], 0); fc.FlowRun.Status != "interrupted" || d.Node(fc, "work").Status != "interrupted" {
		t.Errorf("per-tool run after restart:\n%s", describeRun(fc))
	}
	var got Approval
	d.Get("/api/approvals/" + toolCall.ID).Want(200).JSON(&got)
	if got.Status != "expired" {
		t.Errorf("per-tool approval after restart: %+v", got)
	}
	if r := d.Post("/api/approvals/"+toolCall.ID+"/approve", nil).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("approving an expired approval: %s", r)
	}
	if fb := d.GetRun(runs["fb"], 0); fb.FlowRun.Status != "awaiting_approval" {
		t.Errorf("proposal run after restart:\n%s", describeRun(fb))
	}
	d.Get("/api/approvals/" + proposal.ID).Want(200).JSON(&got)
	if got.Status != "pending" {
		t.Fatalf("proposal approval after restart: %+v", got)
	}

	d.Decide(proposal.ID, true, map[string]any{"comment": "after restart"})
	fb := d.WaitRun(runs["fb"], "succeeded")
	ars := d.AgentRuns(d.Node(fb, "work").ID)
	if len(ars) != 2 || ars[1].ResumedVia != "load" || ars[1].SessionID != ars[0].SessionID {
		t.Errorf("resume after restart: %+v", ars)
	}
	if b, err := os.ReadFile(d.workFile("fb", "hello.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("approved plan not executed after restart: %q %v", b, err)
	}
	d.EventuallyWithin(10*time.Second, "no running work", func() bool {
		st := d.GetStatus()
		return st.RunningFlowRuns == 0 && st.RunningAgentRuns == 0
	})
}
