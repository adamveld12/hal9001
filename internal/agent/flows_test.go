package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
)

// helloAllow lets the scripted edit of hello.txt through the lockdown policy (allow.tools matches the tool title); the
// sneaky.txt edit of phase 1 matches nothing and is rejected, as every agent-side tool call is.
var helloAllow = agent.AllowRules{Tools: []string{`hello\.txt`}}

// proposalScript: phase 1 (ask mode) tries to edit, which the policy must reject, then proposes; phase 2 (full mode)
// performs the allowed edit.
func proposalScript(load bool) fakeagent.Script {
	return fakeagent.Script{
		LoadSession: load,
		Turns: []fakeagent.Turn{
			{When: &fakeagent.When{Mode: "default"}, Steps: []fakeagent.Step{
				editStep("try-1", "sneaky.txt", "sneaky\n"),
				msg("Plan: create hello.txt with a greeting."),
			}},
			{When: &fakeagent.When{Mode: "bypass"}, Steps: []fakeagent.Step{
				editStep("do-1", "hello.txt", "hi\n"),
				msg("Done: hello.txt created."),
			}},
		},
	}
}

// §12.7: proposal phase is read-only, the approved plan resumes the same
// session under the auto policy and executes.
func TestProposalApproveResumesSameSession(t *testing.T) {
	e := newEnv(t, proposalScript(true))
	req := e.req(agent.ModeApprove, agent.ApproveProposal)
	req.Allow = helloAllow

	p1 := e.run(req)
	if p1.Status != agent.StatusAwaitingApproval || p1.Phase != agent.PhasePropose || p1.Policy != agent.PolicyReadonly {
		t.Fatalf("phase 1: status=%s phase=%s policy=%s err=%q", p1.Status, p1.Phase, p1.Policy, p1.Error)
	}
	if p1.Proposal != "Plan: create hello.txt with a greeting." {
		t.Errorf("proposal = %q", p1.Proposal)
	}
	if p1.ApprovalID != "approval-1" {
		t.Errorf("approval id = %q", p1.ApprovalID)
	}
	if props := e.broker.Proposals(); len(props) != 1 || props[0].SessionID != p1.SessionID || props[0].RunID != p1.ID {
		t.Errorf("broker proposals = %+v", props)
	}
	if _, ok := e.file("sneaky.txt"); ok {
		t.Fatal("phase 1 changed the working tree")
	}
	pr := e.perm(p1, "try-1")
	if pr.Decision != agent.DecisionReject || pr.DecidedBy != agent.DecidedByPolicy || pr.MatchedRule != "lockdown" || pr.Reason == "" {
		t.Errorf("phase 1 edit request: %+v", pr)
	}
	if p1.ExitCode == nil || p1.EndedAt == nil {
		t.Error("phase 1 agent process should have exited and the run ended")
	}
	prompts := e.callsOf("session/prompt")
	if got := prompts[0]["text"].(string); got != req.Prompt+agent.ProposalSuffix {
		t.Errorf("phase 1 prompt = %q", got)
	}
	if modes := e.callsOf("session/set_mode"); len(modes) != 1 || modes[0]["mode"] != "default" {
		t.Errorf("phase 1 must use the ask mode, got %v", modes)
	}

	p2, err := e.exec.Resume(t.Context(), agent.ResumeRequest{
		RunRequest: req, PrevRunID: p1.ID, SessionID: p1.SessionID, Proposal: p1.Proposal, Comment: "keep it short",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Status != agent.StatusSucceeded || p2.Phase != agent.PhaseExecute || p2.Policy != agent.PolicyAuto {
		t.Fatalf("phase 2: status=%s phase=%s policy=%s err=%q", p2.Status, p2.Phase, p2.Policy, p2.Error)
	}
	if p2.ResumedVia != agent.ResumedLoad || p2.SessionID != p1.SessionID || p2.ParentRunID != p1.ID {
		t.Errorf("resume: via=%q session=%q parent=%q", p2.ResumedVia, p2.SessionID, p2.ParentRunID)
	}
	if got, _ := e.file("hello.txt"); got != "hi\n" {
		t.Errorf("hello.txt = %q, want the approved edit", got)
	}
	if pr := e.perm(p2, "do-1"); pr.Decision != agent.DecisionAllow || !strings.HasPrefix(pr.MatchedRule, "allow.tools[0]") {
		t.Errorf("phase 2 edit request: %+v", pr)
	}
	prompts = e.callsOf("session/prompt")
	if got := prompts[1]["text"].(string); got != "Approved. Execute the plan.\n\nkeep it short" {
		t.Errorf("phase 2 prompt = %q", got)
	}
	if prompts[1]["session_turn"].(float64) != 1 {
		t.Error("phase 2 did not continue the original session")
	}
	if loads := e.callsOf("session/load"); len(loads) != 1 || loads[0]["session_id"] != p1.SessionID {
		t.Errorf("session/load calls = %v", loads)
	}
	if modes := e.callsOf("session/set_mode"); len(modes) != 2 || modes[1]["mode"] != "bypass" {
		t.Errorf("phase 2 must use the full mode, got %v", modes)
	}
	for _, ev := range e.rec.Events(p2.ID) {
		if ev.Kind == "user_message" {
			t.Error("session/load replay leaked into the execute run's events")
		}
	}
}

// §12.7: without loadSession the proposal is carried into a new session and
// the fallback is recorded.
func TestProposalResumeFallsBackToNewSession(t *testing.T) {
	e := newEnv(t, proposalScript(false))
	req := e.req(agent.ModeApprove, agent.ApproveProposal)
	req.Allow = helloAllow
	p1 := e.run(req)
	if p1.Status != agent.StatusAwaitingApproval {
		t.Fatalf("phase 1: %s %q", p1.Status, p1.Error)
	}

	p2, err := e.exec.Resume(t.Context(), agent.ResumeRequest{
		RunRequest: req, PrevRunID: p1.ID, SessionID: p1.SessionID, Proposal: p1.Proposal, Comment: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Status != agent.StatusSucceeded {
		t.Fatalf("phase 2: %s %q", p2.Status, p2.Error)
	}
	if p2.ResumedVia != agent.ResumedNewSession || p2.ResumeFallbackReason == "" {
		t.Errorf("fallback not recorded: via=%q reason=%q", p2.ResumedVia, p2.ResumeFallbackReason)
	}
	if p2.SessionID == p1.SessionID {
		t.Error("fallback must use a new session")
	}
	if len(e.callsOf("session/load")) != 0 {
		t.Error("session/load must not be called when loadSession is not advertised")
	}
	got := e.callsOf("session/prompt")[1]["text"].(string)
	for _, want := range []string{req.Prompt, p1.Proposal, "Approved. Execute the plan.", "go"} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback prompt lacks %q:\n%s", want, got)
		}
	}
	if p2.Prompt != got {
		t.Error("run record must hold the prompt actually sent")
	}
	if v, _ := e.file("hello.txt"); v != "hi\n" {
		t.Errorf("hello.txt = %q", v)
	}
}

func TestDenyProposalRecordsDenied(t *testing.T) {
	e := newEnv(t, proposalScript(true))
	p1 := e.run(e.req(agent.ModeApprove, agent.ApproveProposal))
	denied := e.exec.DenyProposal(p1)
	if denied.Status != agent.StatusDenied || denied.FailureReason != agent.ReasonDenied {
		t.Errorf("denied record: %+v", denied)
	}
	hist := e.rec.RunHistory(p1.ID)
	if last := hist[len(hist)-1]; last.Status != agent.StatusDenied {
		t.Errorf("recorder saw final status %s", last.Status)
	}
	if len(e.callsOf("session/load")) != 0 {
		t.Error("deny must not start the agent again")
	}
}

// §12.8: an auto-mode agent in a worktree changes the worktree, not the
// source repo; success removes the worktree, failure keeps it.
func TestAutoModeWorktreeIsolation(t *testing.T) {
	repo := newRepo(t)
	wt := &agent.Worktrees{Dir: filepath.Join(t.TempDir(), "worktrees")}
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		editStep("e1", "feature.txt", "new\n"), msg("changed"),
	}}}})

	cwd, err := wt.ResolveCwd(t.Context(), agent.CwdSpec{Mode: "worktree", Repo: repo, Ref: "HEAD"}, "run-1", "")
	if err != nil {
		t.Fatal(err)
	}
	req := e.req(agent.ModeAuto, "")
	req.Allow = agent.AllowRules{Tools: []string{`feature\.txt`}}
	req.Cwd = cwd
	rec := e.run(req)
	if rec.Status != agent.StatusSucceeded || rec.Policy != agent.PolicyAuto {
		t.Fatalf("status=%s err=%q", rec.Status, rec.Error)
	}
	if got, err := os.ReadFile(filepath.Join(cwd, "feature.txt")); err != nil || string(got) != "new\n" {
		t.Fatalf("worktree change missing: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err == nil {
		t.Fatal("source repo was modified")
	}
	if out := git(t, repo, "status", "--porcelain"); out != "" {
		t.Errorf("source repo is dirty: %q", out)
	}
	if m := e.callsOf("session/set_mode"); len(m) != 1 || m[0]["mode"] != "bypass" {
		t.Errorf("auto policy must use the full mode, got %v", m)
	}

	if err := wt.Release(t.Context(), "run-1", agent.StatusFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cwd); err != nil {
		t.Error("a failed run's worktree must be kept")
	}
	if err := wt.Release(t.Context(), "run-1", rec.Status); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cwd); err == nil {
		t.Error("a succeeded run's worktree must be removed")
	}
	if out := git(t, repo, "worktree", "list", "--porcelain"); strings.Contains(out, "run-1") {
		t.Errorf("repo still lists the removed worktree:\n%s", out)
	}
}
