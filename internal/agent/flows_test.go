package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
)

// proposalScript: phase 1 (ask mode) tries to edit, which the read-only
// policy must reject, then proposes; phase 2 (full mode) performs the edit.
func proposalScript(load bool) fakeagent.Script {
	return fakeagent.Script{
		LoadSession: load,
		Turns: []fakeagent.Turn{
			{When: &fakeagent.When{Mode: "default"}, Steps: []fakeagent.Step{
				editStep("try-1", "hello.txt", "sneaky\n"),
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
	if _, ok := e.file("hello.txt"); ok {
		t.Fatal("phase 1 changed the working tree")
	}
	pr := e.perm(p1, "try-1")
	if pr.Decision != agent.DecisionReject || pr.DecidedBy != agent.DecidedByPolicy || pr.MatchedRule != "policy:readonly" || pr.Reason == "" {
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
	if pr := e.perm(p2, "do-1"); pr.Decision != agent.DecisionAllow || pr.MatchedRule != "policy:auto" {
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

// §12.7a: reads pass by policy, edits go to the approval queue with their
// diff, allow applies, deny lets the agent continue, deny+cancel ends the run.
func TestPerToolApprovals(t *testing.T) {
	script := func() fakeagent.Script {
		return fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
			{Op: fakeagent.OpPermission, ID: "r1", Kind: "read", Title: "Read notes.txt", RawInput: map[string]any{"path": "notes.txt"}},
			editStep("e1", "a.txt", "alpha\n"),
			msg("after the edit"),
		}}}}
	}

	// next returns the queued approval and checks the read was not queued.
	next := func(t *testing.T, e *env) agent.ToolApproval {
		t.Helper()
		a, ok := e.broker.Next(10 * time.Second)
		if !ok {
			t.Fatal("no approval was queued")
		}
		if a.ToolCallID != "e1" || a.ToolKind != "edit" {
			t.Fatalf("first queued approval is %q (%s); the read must not be queued", a.ToolCallID, a.ToolKind)
		}
		if !strings.Contains(string(a.Content), `"diff"`) || !strings.Contains(string(a.Content), "a.txt") {
			t.Errorf("approval does not show the diff: %s", a.Content)
		}
		return a
	}

	t.Run("allow applies the edit", func(t *testing.T) {
		e := newEnv(t, script())
		ch, _ := e.start(e.req(agent.ModeApprove, agent.ApprovePerTool))
		a := next(t, e)
		e.broker.Decide(a.PermissionRequestID, agent.ToolDecision{Verdict: agent.VerdictAllow})
		rec := e.wait(ch)
		if rec.Status != agent.StatusSucceeded || rec.Policy != agent.PolicyGated || rec.Phase != agent.PhaseExecute {
			t.Fatalf("status=%s policy=%s err=%q", rec.Status, rec.Policy, rec.Error)
		}
		if got, _ := e.file("a.txt"); got != "alpha\n" {
			t.Errorf("a.txt = %q", got)
		}
		if r := e.perm(rec, "r1"); r.Decision != agent.DecisionAllow || r.DecidedBy != agent.DecidedByPolicy || r.MatchedRule != "kind:read" {
			t.Errorf("read request: %+v", r)
		}
		if p := e.perm(rec, "e1"); p.Decision != agent.DecisionAllow || p.DecidedBy != agent.DecidedByUser || p.ID != a.PermissionRequestID {
			t.Errorf("edit request: %+v", p)
		}
		if m := e.callsOf("session/set_mode"); len(m) != 1 || m[0]["mode"] != "default" {
			t.Errorf("gated runs use the ask mode, got %v", m)
		}
	})

	t.Run("deny rejects that call and the agent continues", func(t *testing.T) {
		e := newEnv(t, script())
		ch, _ := e.start(e.req(agent.ModeApprove, agent.ApprovePerTool))
		a := next(t, e)
		e.broker.Decide(a.PermissionRequestID, agent.ToolDecision{Verdict: agent.VerdictDeny, Comment: "not that file"})
		rec := e.wait(ch)
		if rec.Status != agent.StatusSucceeded || rec.FinalMessage != "after the edit" {
			t.Fatalf("agent should have continued: status=%s final=%q err=%q", rec.Status, rec.FinalMessage, rec.Error)
		}
		if _, ok := e.file("a.txt"); ok {
			t.Error("denied edit was applied")
		}
		p := e.perm(rec, "e1")
		if p.Decision != agent.DecisionReject || p.DecidedBy != agent.DecidedByUser || !strings.Contains(p.Reason, "not that file") {
			t.Errorf("edit request: %+v", p)
		}
		if out := e.callsOf("permission_outcome"); len(out) != 2 || out[1]["allowed"] != false {
			t.Errorf("agent did not see a rejection: %v", out)
		}
	})

	t.Run("deny and cancel ends the run as denied", func(t *testing.T) {
		e := newEnv(t, script())
		ch, _ := e.start(e.req(agent.ModeApprove, agent.ApprovePerTool))
		a := next(t, e)
		e.broker.Decide(a.PermissionRequestID, agent.ToolDecision{Verdict: agent.VerdictDenyCancel})
		rec := e.wait(ch)
		if rec.Status != agent.StatusDenied || rec.FailureReason != agent.ReasonDenied {
			t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
		}
		if strings.Contains(rec.FullMessage, "after the edit") {
			t.Error("agent kept running after deny+cancel")
		}
		if _, ok := e.file("a.txt"); ok {
			t.Error("denied edit was applied")
		}
		if p := e.perm(rec, "e1"); p.Decision != agent.DecisionCancelled || p.DecidedBy != agent.DecidedByUser {
			t.Errorf("edit request: %+v", p)
		}
	})

	t.Run("approval timeout rejects and fails the run", func(t *testing.T) {
		e := newEnv(t, script())
		ch, _ := e.start(e.req(agent.ModeApprove, agent.ApprovePerTool))
		a := next(t, e)
		if want := e.clock.Now().Add(time.Hour); !a.ExpiresAt.Equal(want) {
			t.Errorf("ExpiresAt = %v, want %v", a.ExpiresAt, want)
		}
		if !e.clock.WaitTimers(2, 5*time.Second) {
			t.Fatal("approval timer not registered")
		}
		e.clock.Advance(time.Hour + time.Second)
		rec := e.wait(ch)
		if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonApprovalTimeout {
			t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
		}
		p := e.perm(rec, "e1")
		if p.Decision != agent.DecisionExpired || p.DecidedBy != agent.DecidedByTimeout {
			t.Errorf("edit request: %+v", p)
		}
		if exp := e.broker.Expired(); len(exp) != 1 || exp[0] != p.ID {
			t.Errorf("broker expired = %v", exp)
		}
		if _, ok := e.file("a.txt"); ok {
			t.Error("timed-out edit was applied")
		}
	})
}

// §12.7b: a read-only agent cannot edit or call MCP tools; allow rules open
// exactly what they name.
func TestReadonlyPolicyAndAllowRules(t *testing.T) {
	script := fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		editStep("edit", "x.txt", "x\n"),
		{Op: fakeagent.OpPermission, ID: "mcp", Kind: "other", Title: "mcp__slack__post_message",
			RawInput: map[string]any{"text": "hello"},
			OnAllow:  []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "posted.txt", Data: "p"}}},
		{Op: fakeagent.OpPermission, ID: "view", Kind: "execute", Title: "gh pr view 12",
			RawInput: map[string]any{"command": "gh pr view 12 --json title"},
			OnAllow:  []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "view.txt", Data: "v"}}},
		{Op: fakeagent.OpPermission, ID: "comment", Kind: "execute", Title: "gh pr view 12",
			RawInput: map[string]any{"command": "gh pr comment 12 --body hi"},
			OnAllow:  []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "comment.txt", Data: "c"}}},
		{Op: fakeagent.OpPermission, ID: "chain", Kind: "execute", Title: "gh pr view 12",
			RawInput: map[string]any{"command": "gh pr view 12 && gh pr comment 12 --body hi"},
			OnAllow:  []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "chain.txt", Data: "c"}}},
		{Op: fakeagent.OpPermission, ID: "read", Kind: "search", Title: "grep TODO", RawInput: map[string]any{"pattern": "TODO"}},
		msg("finished"),
	}}}}

	t.Run("without tool allow rules", func(t *testing.T) {
		e := newEnv(t, script)
		req := e.req(agent.ModeReadonly, "")
		req.Allow.Commands = []string{`^gh pr view\b`}
		rec := e.run(req)
		if rec.Status != agent.StatusSucceeded || rec.Policy != agent.PolicyReadonly {
			t.Fatalf("status=%s err=%q", rec.Status, rec.Error)
		}
		for _, f := range []string{"x.txt", "posted.txt", "comment.txt", "chain.txt"} {
			if _, ok := e.file(f); ok {
				t.Errorf("%s was written despite the read-only policy", f)
			}
		}
		if got, ok := e.file("view.txt"); !ok || got != "v" {
			t.Errorf("gh pr view must be allowed by allow.commands (got %q, %v)", got, ok)
		}
		want := map[string]struct{ decision, ruleHas string }{
			"edit":    {agent.DecisionReject, "policy:readonly"},
			"mcp":     {agent.DecisionReject, "policy:readonly"},
			"view":    {agent.DecisionAllow, "allow.commands[0]"},
			"comment": {agent.DecisionReject, "policy:readonly"},
			"chain":   {agent.DecisionReject, "policy:readonly"},
			"read":    {agent.DecisionAllow, "kind:search"},
		}
		for id, w := range want {
			p := e.perm(rec, id)
			if p.Decision != w.decision || !strings.Contains(p.MatchedRule, w.ruleHas) || p.DecidedBy != agent.DecidedByPolicy || p.Reason == "" {
				t.Errorf("%s: %+v, want %s via %s", id, p, w.decision, w.ruleHas)
			}
		}
		if r := e.perm(rec, "mcp").Reason; !strings.Contains(r, "allow.tools") {
			t.Errorf("MCP rejection reason should point at allow.tools: %q", r)
		}
		if r := e.perm(rec, "chain").Reason; !strings.Contains(r, "shell operators") {
			t.Errorf("chained command reason: %q", r)
		}
		if len(e.broker.Pending()) != 0 {
			t.Error("readonly must never queue approvals")
		}
	})

	t.Run("allow.tools opens the named MCP tool", func(t *testing.T) {
		e := newEnv(t, script)
		req := e.req(agent.ModeReadonly, "")
		req.Allow.Tools = []string{`^mcp__slack__post_`}
		rec := e.run(req)
		if got, _ := e.file("posted.txt"); got != "p" {
			t.Error("allow.tools did not permit the MCP tool")
		}
		if p := e.perm(rec, "mcp"); p.Decision != agent.DecisionAllow || !strings.HasPrefix(p.MatchedRule, "allow.tools[0]") {
			t.Errorf("mcp request: %+v", p)
		}
		if _, ok := e.file("x.txt"); ok {
			t.Error("edit still must be rejected")
		}
	})
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
