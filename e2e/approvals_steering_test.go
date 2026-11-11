package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// Steering: the comment of a denial is the user's steering text. A denied tool call or request_approval returns it to
// the agent verbatim and the agent continues; a proposal denied with it is revised and proposed again; a gate or a
// Request approval node continues on denied with it in the payload. After max_steers_per_run steered denials in one
// conversation the next gated action is held: its denial tells the agent to stop and wait and does not forward the text.

const (
	revisedPlan   = "Plan v2: create hello.txt and a README"
	steerHeldText = "Denied by the user. The steering limit for this conversation was reached; stop and wait for the user instead of trying other actions."
)

// steeredText is what a tool result says after a denial with steering.
func steeredText(comment string) string {
	return `Denied by the user: "` + comment + `". Do not retry the same action unchanged; do what the user asked instead.`
}

// inTranscript is s as it appears inside the JSON of a transcript event.
func inTranscript(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

// postCall is a script step that asks hal's gh_api to POST a comment, which hal holds for approval.
func postCall(id, body string) step {
	return step{Op: fakeagent.OpMCPCall, ID: id, Tool: "gh_api", Args: map[string]any{
		"method": "POST", "path": "/repos/o/r/issues/1/comments", "params": map[string]any{"body": body},
	}}
}

// steerDaemon starts a daemon whose agent (approve per_tool) plays steps through hal's MCP tools. config is extra
// config.yaml text.
func steerDaemon(t *testing.T, config string, steps ...step) (*Daemon, string) {
	t.Helper()
	d := newDaemon(t)
	ghLog := filepath.Join(d.Bin, "gh.log")
	d.Script("gh", `printf '%s\n' "$*" >> `+shq(ghLog)+`
case "$*" in *--input*) cat >> `+shq(ghLog)+`; echo >> `+shq(ghLog)+` ;; esac
echo '{"gh":"ok"}'`)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: append(steps, say("finished"))}}})
	d.Flow("f", approvalFlow("per_tool", ""))
	if config != "" {
		d.Config(config)
	}
	d.Start()
	return d, ghLog
}

// nextPending waits for a pending approval of the type that is none of seen.
func (d *Daemon) nextPending(typ string, seen ...string) Approval {
	d.t.Helper()
	var got Approval
	d.Eventually("a new pending "+typ+" approval", func() bool {
		for _, a := range d.Approvals("pending") {
			if a.Type != typ {
				continue
			}
			known := false
			for _, id := range seen {
				known = known || a.ID == id
			}
			if !known {
				got = a
				return true
			}
		}
		return false
	})
	return got
}

// heldView is what the approval API says about the hold of an approval.
type heldView struct {
	Status   string `json:"status"`
	Comment  string `json:"comment"`
	Priority string `json:"priority"`
	Held     bool   `json:"held"`
	HeldNote string `json:"held_note"`
}

func (d *Daemon) heldView(id string) heldView {
	d.t.Helper()
	var v heldView
	d.Get("/api/approvals/" + id).Want(200).JSON(&v)
	return v
}

func (d *Daemon) agentTranscript(run FlowRunDetail) string {
	d.t.Helper()
	return d.Transcript(d.AgentRuns(d.Node(run, "work").ID)[0].ID)
}

// A tool call denied with steering returns the user's text to the agent as the tool error, verbatim inside the
// steering sentence, and the agent continues. The preamble told the agent what such a denial means.
func TestSteeredToolCallDenialReachesTheAgent(t *testing.T) {
	d, ghLog := steerDaemon(t, "", postCall("c1", "hi"), step{Op: fakeagent.OpMCPCall, ID: "c2", Tool: "run_context", Args: map[string]any{}})
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("tool_call")
	if v := d.heldView(ap.ID); v.Held {
		t.Fatalf("the first approval of a conversation is held: %+v", v)
	}
	decided := d.Decide(ap.ID, false, map[string]any{"comment": "  use the staging repo  "})
	if decided.Comment != "use the staging repo" {
		t.Errorf("recorded comment %q", decided.Comment)
	}
	done := d.WaitRun(runID, "succeeded")
	tr := d.agentTranscript(done)
	if !strings.Contains(tr, "mcp[c1] is_error=true") || !strings.Contains(tr, inTranscript(steeredText("use the staging repo"))) {
		t.Errorf("the steering did not reach the agent as the tool error:\n%s", tr)
	}
	if !strings.Contains(tr, "mcp[c2] is_error=false") {
		t.Errorf("the agent did not continue after the denial:\n%s", tr)
	}
	if strings.Contains(readLog(t, ghLog), "POST") {
		t.Error("the denied POST ran")
	}
	prompt := d.AgentRuns(d.Node(done, "work").ID)[0].RenderedPrompt
	for _, want := range []string{`Denied by the user: "..."`, "do not retry the same action unchanged", "stop and wait", "request_approval needs a description"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the agent's instructions lack %q:\n%s", want, prompt)
		}
	}
}

// A denial without a comment keeps the plain tool error.
func TestDeniedToolCallWithoutSteeringIsPlain(t *testing.T) {
	d, _ := steerDaemon(t, "", postCall("c1", "hi"))
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	d.Decide(d.WaitApproval("tool_call").ID, false, nil)
	tr := d.agentTranscript(d.WaitRun(runID, "succeeded"))
	if !strings.Contains(tr, "mcp[c1] is_error=true") || strings.Contains(tr, "Denied by the user") || !strings.Contains(tr, "was denied by the user") {
		t.Errorf("transcript:\n%s", tr)
	}
}

// Deny + stop ends the run: the agent never reaches its next call, and the node leaves on denied.
func TestDenyAndStopStillStopsTheRun(t *testing.T) {
	d, ghLog := steerDaemon(t, "", postCall("c1", "hi"), step{Op: fakeagent.OpMCPCall, ID: "c2", Tool: "run_context", Args: map[string]any{}})
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("tool_call")
	decided := d.Decide(ap.ID, false, map[string]any{"cancel_run": true, "comment": "stop it"})
	if decided.Status != "denied" || decided.CancelRun == nil || !*decided.CancelRun {
		t.Fatalf("decision: %+v", decided)
	}
	done := d.WaitRun(runID, "succeeded")
	if n := d.Node(done, "work"); len(n.Ports) != 1 || n.Ports[0] != "denied" {
		t.Fatalf("node after deny + stop:\n%s", describeRun(done))
	}
	if tr := d.agentTranscript(done); strings.Contains(tr, "mcp[c2]") {
		t.Errorf("the agent went on after deny + stop:\n%s", tr)
	}
	if strings.Contains(readLog(t, ghLog), "POST") {
		t.Error("the denied POST ran")
	}
}

// request_approval denied with steering: the result says denied, carries the comment and a message with the steering
// sentence, and the agent continues.
func TestSteeredAgentRequestDenialReachesTheAgent(t *testing.T) {
	d, _ := steerDaemon(t, "", step{Op: fakeagent.OpMCPCall, ID: "a1", Tool: "request_approval", Args: map[string]any{
		"title": "Deploy now?", "description": "Deploy build 42 to staging because the tests passed.", "wait_seconds": 60,
	}}, step{Op: fakeagent.OpMCPCall, ID: "a2", Tool: "run_context", Args: map[string]any{}})
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("request")
	d.Decide(ap.ID, false, map[string]any{"comment": "wait for the freeze to end"})
	tr := d.agentTranscript(d.WaitRun(runID, "succeeded"))
	// The result is JSON text inside the transcript event's JSON: quotes are escaped twice.
	quoted, _ := json.Marshal(steeredText("wait for the freeze to end"))
	message := inTranscript(`"message":` + string(quoted))
	for _, want := range []string{"mcp[a1] is_error=false", inTranscript(`"decision":"denied"`), message, "mcp[a2] is_error=false"} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}
}

// With max_steers_per_run = 2, the third gated action of a conversation is held after two steered denials: the card
// offers no steering, and a denial of it tells the agent to stop and wait without the user's text, while the comment
// is still recorded.
func TestSteerCapHoldsTheNextToolCall(t *testing.T) {
	d, ghLog := steerDaemon(t, "max_steers_per_run: 2\n", postCall("c1", "one"), postCall("c2", "two"), postCall("c3", "three"),
		step{Op: fakeagent.OpMCPCall, ID: "c4", Tool: "run_context", Args: map[string]any{}})
	runID := d.InjectRun("src", map[string]any{"id": "7"})

	ap1 := d.WaitApproval("tool_call")
	d.Decide(ap1.ID, false, map[string]any{"comment": "steer one"})
	ap2 := d.nextPending("tool_call", ap1.ID)
	if v := d.heldView(ap2.ID); v.Held || v.HeldNote != "" {
		t.Fatalf("the second approval is held below the cap: %+v", v)
	}
	d.Decide(ap2.ID, false, map[string]any{"comment": "steer two"})

	ap3 := d.nextPending("tool_call", ap1.ID, ap2.ID)
	v := d.heldView(ap3.ID)
	wantNote := "Steering limit reached: 2 steered denials in this conversation. This action is held for your review; the agent was told to stop and wait."
	if !v.Held || v.HeldNote != wantNote || v.Priority != "high" {
		t.Fatalf("third approval: %+v", v)
	}
	if ap3.Conversation == nil || ap1.Conversation == nil || ap3.Conversation.ID != ap1.Conversation.ID {
		t.Errorf("the conversation changed between approvals: %+v %+v", ap1.Conversation, ap3.Conversation)
	}
	if ap3.Title != ap1.Title {
		t.Errorf("a held approval keeps its title: %q vs %q", ap3.Title, ap1.Title)
	}
	d.Eventually("the held notification", func() bool {
		for _, n := range d.Notifications() {
			if strings.HasSuffix(n["open"], "/inbox/approval/"+ap3.ID) {
				return strings.Contains(n["message"], "Held for your review") && strings.Contains(n["message"], ap3.Description)
			}
		}
		return false
	})

	decided := d.Decide(ap3.ID, false, map[string]any{"comment": "steer three must stay private"})
	if decided.Comment != "steer three must stay private" {
		t.Errorf("a held denial still records its comment: %+v", decided)
	}
	done := d.WaitRun(runID, "succeeded")
	tr := d.agentTranscript(done)
	if !strings.Contains(tr, inTranscript(steerHeldText)) || strings.Contains(tr, "steer three") {
		t.Errorf("a held denial must tell the agent to stop and wait, without the user's text:\n%s", tr)
	}
	if !strings.Contains(tr, inTranscript(steeredText("steer two"))) {
		t.Errorf("the second denial was steered:\n%s", tr)
	}
	if strings.Contains(readLog(t, ghLog), "POST") {
		t.Error("a denied POST ran")
	}
}

// Approving a held approval behaves normally.
func TestApprovingAHeldToolCallRuns(t *testing.T) {
	d, ghLog := steerDaemon(t, "max_steers_per_run: 1\n", postCall("c1", "one"), postCall("c2", "two"))
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap1 := d.WaitApproval("tool_call")
	d.Decide(ap1.ID, false, map[string]any{"comment": "no, differently"})
	ap2 := d.nextPending("tool_call", ap1.ID)
	if !d.heldView(ap2.ID).Held {
		t.Fatalf("the approval after the cap is not held: %+v", d.heldView(ap2.ID))
	}
	d.Decide(ap2.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	if log := readLog(t, ghLog); !strings.Contains(log, `{"body":"two"}`) || strings.Contains(log, `"one"`) {
		t.Errorf("gh log %q", log)
	}
	if tr := d.agentTranscript(done); !strings.Contains(tr, "mcp[c2] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
}

// A request_approval of the agent is held at the cap too: its denial result tells the agent to stop and wait.
func TestSteerCapHoldsAgentRequests(t *testing.T) {
	d, _ := steerDaemon(t, "max_steers_per_run: 1\n", postCall("c1", "one"), step{Op: fakeagent.OpMCPCall, ID: "a1", Tool: "request_approval", Args: map[string]any{
		"title": "Try again?", "description": "Retry the post another way.", "wait_seconds": 60,
	}})
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap1 := d.WaitApproval("tool_call")
	d.Decide(ap1.ID, false, map[string]any{"comment": "no posting"})
	ask := d.WaitApproval("request")
	if v := d.heldView(ask.ID); !v.Held || !strings.HasPrefix(v.HeldNote, "Steering limit reached: 1 steered denials") {
		t.Fatalf("agent request after the cap: %+v", v)
	}
	d.Decide(ask.ID, false, map[string]any{"comment": "stay private"})
	tr := d.agentTranscript(d.WaitRun(runID, "succeeded"))
	if !strings.Contains(tr, "mcp[a1] is_error=false") || !strings.Contains(tr, inTranscript(steerHeldText)) || strings.Contains(tr, "stay private") {
		t.Errorf("transcript:\n%s", tr)
	}
}

// ---- proposals ----

// reviseScript plans in ask mode, writes a revised plan when the user's denial arrives as the prompt, and executes
// in full mode.
func reviseScript(load bool) script {
	return script{LoadSession: load, Turns: []turn{
		{When: when("The user denied your proposal"), Steps: []step{say(revisedPlan)}},
		{When: inMode("default"), Steps: []step{say(proposalPlan)}},
		{When: inMode("bypass"), Steps: []step{
			{Op: fakeagent.OpWriteFile, Path: "hello.txt", Data: "hi\n"},
			say("executed"),
		}},
	}}
}

func revisePrompt(comment string) string {
	return `The user denied your proposal: "` + comment + `". Revise the proposal accordingly and propose again.`
}

func reviseDaemon(t *testing.T, load bool, config string) *Daemon {
	t.Helper()
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", reviseScript(load))
	d.Flow("f", approvalFlow("proposal", ""))
	if config != "" {
		d.Config(config)
	}
	d.Start()
	return d
}

func (d *Daemon) promptsSent(containing string) []string {
	var out []string
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/prompt" {
			if text, _ := c["text"].(string); strings.Contains(text, containing) {
				out = append(out, text)
			}
		}
	}
	return out
}

// A proposal denied with steering resumes the agent, which revises and proposes again: the node stays parked on a new
// approval of the same conversation, the revise turn is read-only in the same session and its prompt carries the
// steering; approving the new proposal runs phase 2.
func TestProposalDeniedWithSteeringIsRevised(t *testing.T) {
	d := reviseDaemon(t, true, "")
	runID := d.InjectRun("src", map[string]any{"id": "42"})
	d.WaitRun(runID, "awaiting_approval")
	ap1 := d.WaitApproval("proposal")

	denied := d.Decide(ap1.ID, false, map[string]any{"comment": "also add a README"})
	if denied.Status != "denied" {
		t.Fatalf("decision: %+v", denied)
	}
	ap2 := d.nextPending("proposal", ap1.ID)
	parked := d.WaitRun(runID, "awaiting_approval")
	if !strings.Contains(ap2.ProposalMD, revisedPlan) || ap2.Title != revisedPlan || ap2.AgentRunID == ap1.AgentRunID {
		t.Fatalf("revised proposal: %+v", ap2)
	}
	if ap1.Conversation == nil || ap2.Conversation == nil || ap2.Conversation.ID != ap1.Conversation.ID || ap2.Conversation.Kind != "flow" {
		t.Errorf("a revision continues the conversation: %+v / %+v", ap1.Conversation, ap2.Conversation)
	}
	if ap2.Held || d.heldView(ap2.ID).Held {
		t.Errorf("a revision below the cap is held")
	}
	work := d.Node(parked, "work")
	runs := d.AgentRuns(work.ID)
	if work.Status != "awaiting_approval" || len(runs) != 2 {
		t.Fatalf("parked node %+v runs %+v\n%s", work, runs, describeRun(parked))
	}
	p1, p2 := runs[0], runs[1]
	if p1.Phase != "propose" || p1.Status != "succeeded" || p2.Phase != "propose" || p2.Status != "awaiting_approval" || p2.Policy != "readonly" ||
		p2.ResumedVia != "load" || p2.SessionID != p1.SessionID || ap2.AgentRunID != p2.ID {
		t.Errorf("phases:\n%+v\n%+v", p1, p2)
	}
	if want := revisePrompt("also add a README"); !strings.HasPrefix(p2.RenderedPrompt, want) || !strings.Contains(p2.RenderedPrompt, "propose a concrete plan") {
		t.Errorf("revise prompt: %q", p2.RenderedPrompt)
	}
	if got := d.promptsSent(revisePrompt("also add a README")); len(got) != 1 {
		t.Errorf("the agent received the steering %d times", len(got))
	}
	if d.fileThere(d.workFile("f", "hello.txt")) {
		t.Fatal("the plan was executed before approval")
	}
	if got := d.Approvals("pending"); len(got) != 1 {
		t.Errorf("pending: %+v", got)
	}

	d.Decide(ap2.ID, true, map[string]any{"comment": "ship it"})
	done := d.WaitRun(runID, "succeeded")
	runs = d.AgentRuns(d.Node(done, "work").ID)
	if len(runs) != 3 {
		t.Fatalf("agent runs: %d, want propose, revise, execute\n%s", len(runs), describeRun(done))
	}
	p3 := runs[2]
	if runs[1].Status != "succeeded" || p3.Phase != "execute" || p3.Policy != "auto" || p3.ResumedVia != "load" || p3.SessionID != p2.SessionID ||
		!strings.HasPrefix(p3.RenderedPrompt, "Approved. Execute the plan.") {
		t.Errorf("execute phase: %+v", p3)
	}
	if b, err := os.ReadFile(d.workFile("f", "hello.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("approved revision not executed: %q %v", b, err)
	}
	var detail AgentRunDetail
	d.Get("/api/agent-runs/" + p1.ID).Want(200).JSON(&detail)
	if len(detail.PhaseRunIDs) != 3 {
		t.Errorf("phase runs: %v", detail.PhaseRunIDs)
	}
	if n := d.Node(done, "work"); n.Status != "done" || len(n.Ports) != 1 || n.Ports[0] != "out" {
		t.Errorf("node after the revision was approved: %+v", n)
	}
	if _, ok := d.InboxByTitle()["done 42"]; !ok {
		t.Errorf("downstream node did not run: %+v", d.Inbox())
	}
}

// An agent that cannot load the session gets a new one whose prompt carries the original prompt, the earlier proposal
// and the steering; the later execute phase still carries the original prompt, once.
func TestProposalRevisionWithoutLoadSession(t *testing.T) {
	d := reviseDaemon(t, false, "")
	runID := d.InjectRun("src", map[string]any{"id": "42"})
	d.WaitRun(runID, "awaiting_approval")
	ap1 := d.WaitApproval("proposal")
	d.Decide(ap1.ID, false, map[string]any{"comment": "also add a README"})
	ap2 := d.nextPending("proposal", ap1.ID)
	parked := d.WaitRun(runID, "awaiting_approval")
	runs := d.AgentRuns(d.Node(parked, "work").ID)
	if len(runs) != 2 || runs[1].ResumedVia != "new_session" || runs[1].SessionID == runs[0].SessionID || runs[1].Policy != "readonly" {
		t.Fatalf("runs: %+v", runs)
	}
	for _, want := range []string{"Review 42", "Your earlier proposal for this task:", proposalPlan, revisePrompt("also add a README"), "propose a concrete plan"} {
		if !strings.Contains(runs[1].RenderedPrompt, want) {
			t.Errorf("fallback revise prompt lacks %q:\n%s", want, runs[1].RenderedPrompt)
		}
	}

	d.Decide(ap2.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	runs = d.AgentRuns(d.Node(done, "work").ID)
	if len(runs) != 3 {
		t.Fatalf("runs: %d\n%s", len(runs), describeRun(done))
	}
	p3 := runs[2].RenderedPrompt
	if runs[2].ResumedVia != "new_session" || strings.Count(p3, "Review 42") != 1 || !strings.Contains(p3, revisedPlan) || strings.Contains(p3, proposalPlan+"\n") ||
		!strings.Contains(p3, "Approved. Execute the plan.") || strings.Contains(p3, "The user denied your proposal") {
		t.Errorf("execute fallback prompt:\n%s", p3)
	}
	if !d.fileThere(d.workFile("f", "hello.txt")) {
		t.Error("the revised plan was not executed")
	}
}

// A proposal denied without steering still ends the node on denied, and no revise turn runs.
func TestProposalDeniedWithoutSteeringEnds(t *testing.T) {
	d := reviseDaemon(t, true, "")
	runID := d.InjectRun("src", map[string]any{"id": "43"})
	d.WaitRun(runID, "awaiting_approval")
	d.Decide(d.WaitApproval("proposal").ID, false, map[string]any{"comment": "   "})
	done := d.WaitRun(runID, "succeeded")
	if n := d.Node(done, "work"); len(n.Ports) != 1 || n.Ports[0] != "denied" {
		t.Fatalf("node: %+v", n)
	}
	if got := d.promptsSent("The user denied your proposal"); len(got) != 0 {
		t.Errorf("a revise turn ran: %v", got)
	}
}

// The cap counts across revisions: with max_steers_per_run = 1 the revision of the first steered denial is raised
// held, and denying it ends the node on denied (the held proposal is not revised again) with the comment in the payload.
func TestHeldProposalDenialEndsOnDenied(t *testing.T) {
	d := reviseDaemon(t, true, "max_steers_per_run: 1\n")
	runID := d.InjectRun("src", map[string]any{"id": "44"})
	d.WaitRun(runID, "awaiting_approval")
	ap1 := d.WaitApproval("proposal")
	d.Decide(ap1.ID, false, map[string]any{"comment": "also add a README"})
	ap2 := d.nextPending("proposal", ap1.ID)
	v := d.heldView(ap2.ID)
	if !v.Held || !strings.HasPrefix(v.HeldNote, "Steering limit reached: 1 steered denials in this conversation.") || v.Priority != "high" {
		t.Fatalf("revision at the cap: %+v", v)
	}
	d.WaitRun(runID, "awaiting_approval")

	d.Decide(ap2.ID, false, map[string]any{"comment": "still not right"})
	done := d.WaitRun(runID, "succeeded")
	work := d.Node(done, "work")
	if len(work.Ports) != 1 || work.Ports[0] != "denied" || len(d.AgentRuns(work.ID)) != 2 {
		t.Fatalf("node after a held denial: %+v\n%s", work, describeRun(done))
	}
	if it, ok := d.InboxByTitle()["refused 44"]; !ok || it.SummaryMD != "still not right" {
		t.Errorf("denied branch: %+v", d.Inbox())
	}
	if got := d.promptsSent("The user denied your proposal"); len(got) != 1 {
		t.Errorf("revise turns: %d, want 1", len(got))
	}
	if got := d.Approvals("pending"); len(got) != 0 {
		t.Errorf("pending: %+v", got)
	}
}

// A parked proposal and its revision survive daemon restarts: the steering is applied after a restart, and so is the
// approval of the revision.
func TestProposalRevisionSurvivesRestart(t *testing.T) {
	d := reviseDaemon(t, true, "")
	runID := d.InjectRun("src", map[string]any{"id": "45"})
	d.WaitRun(runID, "awaiting_approval")
	ap1 := d.WaitApproval("proposal")

	d.Restart()
	d.Decide(ap1.ID, false, map[string]any{"comment": "also add a README"})
	ap2 := d.nextPending("proposal", ap1.ID)
	d.WaitRun(runID, "awaiting_approval")

	d.Restart()
	if got := d.Approvals("pending"); len(got) != 1 || got[0].ID != ap2.ID {
		t.Fatalf("pending after restart: %+v", got)
	}
	d.Decide(ap2.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	if len(d.AgentRuns(d.Node(done, "work").ID)) != 3 || !d.fileThere(d.workFile("f", "hello.txt")) {
		t.Errorf("revision not executed after restarts:\n%s", describeRun(done))
	}
}

// ---- gates and Request approval nodes ----

// A gate denied with a comment continues on denied with the comment in approval.comment. It has no conversation, so
// it does not count towards the cap: with max_steers_per_run = 1 the next gate is not held.
func TestGateSteeringReachesTheDeniedPortAndIsNotCounted(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", true))
	d.Config("max_steers_per_run: 1\n")
	d.Start()

	run1 := d.InjectRun("src", map[string]any{"id": "1"})
	ap1 := d.gateApproval()
	d.Decide(ap1.ID, false, map[string]any{"comment": "use the dry-run flag"})
	d.WaitRun(run1, "succeeded")
	if it, ok := d.InboxByTitle()["refused 1"]; !ok || !strings.HasPrefix(it.SummaryMD, "denied|use the dry-run flag|"+ap1.ID+"|") {
		t.Fatalf("denied port payload: %+v", d.Inbox())
	}

	d.InjectRun("src", map[string]any{"id": "2"})
	var ap2 Approval
	d.Eventually("the second gate", func() bool {
		for _, a := range d.Approvals("pending") {
			ap2 = a
			return true
		}
		return false
	})
	if v := d.heldView(ap2.ID); v.Held || ap2.Conversation != nil {
		t.Errorf("a gate was held or has a conversation: %+v %+v", v, ap2.Conversation)
	}
}

// The same for a Request approval node.
func TestRequestNodeSteeringReachesTheDeniedPortAndIsNotCounted(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Config("max_steers_per_run: 1\n")
	d.Start()

	run1 := d.InjectRun("src", map[string]any{"id": "1", "name": "api"})
	d.WaitRun(run1, "awaiting_approval")
	d.Decide(d.requestApproval().ID, false, map[string]any{"comment": "ask me tomorrow"})
	d.WaitRun(run1, "succeeded")
	if it, ok := d.InboxByTitle()["no api"]; !ok || it.SummaryMD != "denied|ask me tomorrow" {
		t.Fatalf("denied port payload: %+v", d.Inbox())
	}

	d.InjectRun("src", map[string]any{"id": "2", "name": "web"})
	ap2 := d.WaitApproval("request")
	if v := d.heldView(ap2.ID); v.Held || ap2.Conversation != nil {
		t.Errorf("a Request approval node was held or has a conversation: %+v %+v", v, ap2.Conversation)
	}
}
