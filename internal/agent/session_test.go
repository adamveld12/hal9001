package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
)

// forPrompt makes a scripted turn for prompts that contain text.
func forPrompt(text string, steps ...fakeagent.Step) fakeagent.Turn {
	return fakeagent.Turn{When: &fakeagent.When{PromptMatches: text}, Steps: steps}
}

func (e *env) newSession(mod ...func(*agent.SessionRequest)) *agent.Session {
	e.t.Helper()
	req := agent.SessionRequest{Runtime: "fake", Config: e.cfg, Cwd: e.work}
	for _, m := range mod {
		m(&req)
	}
	s, err := e.exec.NewSession(req)
	if err != nil {
		e.t.Fatalf("NewSession: %v", err)
	}
	e.t.Cleanup(s.Close)
	return s
}

func turnReq(prompt string, mod ...func(*agent.TurnRequest)) agent.TurnRequest {
	req := agent.TurnRequest{Prompt: prompt, Timeout: 24 * time.Hour, ApprovalTimeout: time.Hour}
	for _, m := range mod {
		m(&req)
	}
	return req
}

func withAllow(a agent.AllowRules) func(*agent.TurnRequest) {
	return func(r *agent.TurnRequest) { r.Allow = a }
}

// turn runs one turn to completion.
func (e *env) turn(s *agent.Session, prompt string, mod ...func(*agent.TurnRequest)) agent.RunRecord {
	e.t.Helper()
	rec, err := s.Prompt(e.t.Context(), turnReq(prompt, mod...))
	if err != nil {
		e.t.Fatalf("Prompt(%q): %v", prompt, err)
	}
	return rec
}

type turnResult struct {
	rec agent.RunRecord
	err error
}

// startTurn runs a turn in the background (for tests that interact mid-turn).
func (e *env) startTurn(s *agent.Session, prompt string, mod ...func(*agent.TurnRequest)) (chan turnResult, func(cause error)) {
	ctx, cancel := withCause(e.t)
	ch := make(chan turnResult, 1)
	go func() {
		rec, err := s.Prompt(ctx, turnReq(prompt, mod...))
		ch <- turnResult{rec, err}
	}()
	return ch, cancel
}

func (e *env) waitTurn(ch chan turnResult) agent.RunRecord {
	e.t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			e.t.Fatalf("Prompt: %v", r.err)
		}
		return r.rec
	case <-time.After(30 * time.Second):
		e.t.Fatal("turn did not finish")
		return agent.RunRecord{}
	}
}

// waitPlan waits until the turn has streamed its plan update, which the
// scripts emit right before they block.
func (e *env) waitPlan(runID string) {
	e.t.Helper()
	waitFor(e.t, "plan event", func() bool {
		for _, ev := range e.rec.Events(runID) {
			if ev.Kind == "plan" {
				return true
			}
		}
		return false
	})
}

func (e *env) wantSucceeded(rec agent.RunRecord) {
	e.t.Helper()
	if rec.Status != agent.StatusSucceeded {
		e.t.Fatalf("turn %s: status=%s reason=%s err=%q", rec.ID, rec.Status, rec.FailureReason, rec.Error)
	}
}

func (e *env) wantSeqFromOne(rec agent.RunRecord) {
	e.t.Helper()
	events := e.rec.Events(rec.ID)
	if len(events) == 0 {
		e.t.Fatalf("turn %s recorded no events", rec.ID)
	}
	for i, ev := range events {
		if ev.Seq != i+1 {
			e.t.Fatalf("turn %s event %d has seq %d, want %d", rec.ID, i, ev.Seq, i+1)
		}
	}
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func rawMethods(e *env, runID string) []string {
	var out []string
	for _, f := range e.rec.RawFrames(runID) {
		if f.Dir == "send" {
			for _, m := range []string{"initialize", "session/new", "session/load", "session/prompt"} {
				if strings.Contains(string(f.Line), `"method":"`+m+`"`) {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func TestSessionTurnsShareOneProcess(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{
		forPrompt("first", msg("one")),
		forPrompt("second", msg("two")),
	}})
	s := e.newSession()
	if s.Alive() || s.ACPSessionID() != "" {
		t.Fatal("a new session must not have started a process")
	}

	r1 := e.turn(s, "first", func(r *agent.TurnRequest) { r.RunID = "turn-1" })
	r2 := e.turn(s, "second")

	e.wantSucceeded(r1)
	e.wantSucceeded(r2)
	if r1.ID != "turn-1" || r2.ID == "" || r1.ID == r2.ID {
		t.Errorf("run ids = %q, %q", r1.ID, r2.ID)
	}
	if r1.FinalMessage != "one" || r2.FinalMessage != "two" {
		t.Errorf("final messages = %q, %q", r1.FinalMessage, r2.FinalMessage)
	}
	e.wantSeqFromOne(r1)
	e.wantSeqFromOne(r2)
	for _, r := range []agent.RunRecord{r1, r2} {
		if r.Mode != agent.ModeApprove || r.Approve != agent.ApprovePerTool || r.Phase != agent.PhaseExecute || r.Policy != agent.PolicyGated || r.NodeRunID != "" {
			t.Errorf("turn %s mode/policy = %s/%s/%s/%s node=%q", r.ID, r.Mode, r.Approve, r.Phase, r.Policy, r.NodeRunID)
		}
		if r.Cwd != e.work || r.Runtime != "fake" || r.EndedAt == nil {
			t.Errorf("turn %s record = %+v", r.ID, r)
		}
	}
	if r1.Prompt != "first" || r2.Prompt != "second" {
		t.Errorf("prompts = %q, %q", r1.Prompt, r2.Prompt)
	}

	if n := len(e.callsOf("initialize")); n != 1 {
		t.Errorf("%d initialize calls, want one process", n)
	}
	if n := len(e.callsOf("session/new")); n != 1 {
		t.Errorf("%d session/new calls, want 1", n)
	}
	if n := len(e.callsOf("session/prompt")); n != 2 {
		t.Errorf("%d session/prompt calls, want 2", n)
	}
	if r1.SessionID == "" || r1.SessionID != r2.SessionID || s.ACPSessionID() != r1.SessionID {
		t.Errorf("session ids = %q, %q, %q", r1.SessionID, r2.SessionID, s.ACPSessionID())
	}
	if r1.ResumedVia != "" || r2.ResumedVia != "" {
		t.Errorf("no turn was a resume: %q, %q", r1.ResumedVia, r2.ResumedVia)
	}
	if len(r2.Argv) == 0 || r2.AgentInfo == nil || len(r2.Capabilities) == 0 {
		t.Errorf("a turn on the running process lacks argv/agent info/capabilities: %+v", r2)
	}
	if !s.Alive() {
		t.Error("process should stay up between turns")
	}
	// Raw frames are attributed to the turn that caused them.
	if got := rawMethods(e, r1.ID); !slices.Equal(got, []string{"initialize", "session/new", "session/prompt"}) {
		t.Errorf("turn 1 sent %v", got)
	}
	if got := rawMethods(e, r2.ID); !slices.Equal(got, []string{"session/prompt"}) {
		t.Errorf("turn 2 sent %v", got)
	}
}

func TestSessionSuspendResumesWithLoad(t *testing.T) {
	e := newEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{
		forPrompt("first", msg("one")),
		forPrompt("second", msg("two")),
	}})
	s := e.newSession()
	r1 := e.turn(s, "first")
	e.wantSucceeded(r1)
	done := s.Done()

	s.Suspend()
	if s.Alive() || !isClosed(done) {
		t.Fatalf("Suspend left the process running: alive=%v done closed=%v", s.Alive(), isClosed(done))
	}
	if s.ACPSessionID() != r1.SessionID {
		t.Fatal("Suspend lost the session id")
	}

	r2 := e.turn(s, "second", func(r *agent.TurnRequest) { r.FallbackPrompt = "recap, then second" })
	e.wantSucceeded(r2)
	if r2.ResumedVia != agent.ResumedLoad || r2.SessionID != r1.SessionID || r2.Prompt != "second" {
		t.Errorf("restart: via=%q session=%q (was %q) prompt=%q", r2.ResumedVia, r2.SessionID, r1.SessionID, r2.Prompt)
	}
	if r2.FinalMessage != "two" {
		t.Errorf("final message = %q", r2.FinalMessage)
	}
	e.wantSeqFromOne(r2) // the session/load replay is not recorded as turn events
	for _, ev := range e.rec.Events(r2.ID) {
		if ev.Kind == "user_message" || strings.Contains(string(ev.Payload), `"one"`) {
			t.Errorf("replayed history leaked into the turn's events: %s", ev.Payload)
		}
	}
	if n, l, p := len(e.callsOf("initialize")), len(e.callsOf("session/load")), len(e.callsOf("session/new")); n != 2 || l != 1 || p != 1 {
		t.Errorf("initialize=%d load=%d new=%d, want 2/1/1", n, l, p)
	}
	if !s.Alive() || isClosed(s.Done()) {
		t.Error("restarted process should be alive with an open Done channel")
	}
}

func TestSessionSuspendWithoutLoadSessionStartsNewSessionWithRecap(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{
		forPrompt("first", msg("one")),
		forPrompt("second", msg("two")),
	}})
	s := e.newSession()
	r1 := e.turn(s, "first")
	e.wantSucceeded(r1)
	s.Suspend()

	const recap = "Earlier: first/one. Now: second"
	r2 := e.turn(s, "second", func(r *agent.TurnRequest) { r.FallbackPrompt = recap })
	e.wantSucceeded(r2)
	if r2.ResumedVia != agent.ResumedNewSession || r2.ResumeFallbackReason == "" {
		t.Errorf("via=%q reason=%q", r2.ResumedVia, r2.ResumeFallbackReason)
	}
	if r2.Prompt != recap {
		t.Errorf("recorded prompt = %q, want the fallback", r2.Prompt)
	}
	if r2.SessionID == "" || r2.SessionID == r1.SessionID || s.ACPSessionID() != r2.SessionID {
		t.Errorf("session ids: %q then %q (current %q)", r1.SessionID, r2.SessionID, s.ACPSessionID())
	}
	prompts := e.callsOf("session/prompt")
	if len(prompts) != 2 || prompts[1]["text"] != recap {
		t.Errorf("agent received %v", prompts)
	}
}

func TestSessionFirstStartLoadsKnownSession(t *testing.T) {
	e := newEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{
		forPrompt("first", msg("one")),
		forPrompt("again", msg("back")),
	}})
	first := e.newSession()
	r1 := e.turn(first, "first")
	e.wantSucceeded(r1)
	first.Close()

	s := e.newSession(func(r *agent.SessionRequest) { r.LoadSessionID = r1.SessionID })
	if s.ACPSessionID() != "" {
		t.Error("ACPSessionID is set before the first start")
	}
	r2 := e.turn(s, "again", func(r *agent.TurnRequest) { r.FallbackPrompt = "recap again" })
	e.wantSucceeded(r2)
	if r2.ResumedVia != agent.ResumedLoad || r2.SessionID != r1.SessionID {
		t.Errorf("via=%q session=%q, want load of %q", r2.ResumedVia, r2.SessionID, r1.SessionID)
	}

	// An id the agent does not know falls back to a new session and the recap.
	s = e.newSession(func(r *agent.SessionRequest) { r.LoadSessionID = "no-such-session" })
	r3 := e.turn(s, "again", func(r *agent.TurnRequest) { r.FallbackPrompt = "recap again" })
	e.wantSucceeded(r3)
	if r3.ResumedVia != agent.ResumedNewSession || !strings.Contains(r3.ResumeFallbackReason, "session/load failed") || r3.Prompt != "recap again" {
		t.Errorf("unknown session: via=%q reason=%q prompt=%q", r3.ResumedVia, r3.ResumeFallbackReason, r3.Prompt)
	}
}

// killAgentSoon makes the agent die shortly after answering the turn: a child
// signals its parent, the agent.
func killAgentSoon() fakeagent.Step {
	return fakeagent.Step{Op: fakeagent.OpSpawnChild, Cmd: []string{"sh", "-c", "sleep 0.3; kill -9 $PPID"}}
}

func TestSessionProcessDiesBetweenTurns(t *testing.T) {
	e := newEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{
		forPrompt("crash", msg("answered"), killAgentSoon()),
		forPrompt("after", msg("back")),
	}})
	s := e.newSession()
	r1 := e.turn(s, "crash")
	e.wantSucceeded(r1)
	done := s.Done()

	waitFor(t, "session to notice the exit", func() bool { return !s.Alive() })
	waitFor(t, "Done to close", func() bool { return isClosed(done) })

	r2 := e.turn(s, "after")
	e.wantSucceeded(r2)
	if r2.ResumedVia != agent.ResumedLoad || r2.SessionID != r1.SessionID {
		t.Errorf("restart: via=%q session=%q (was %q)", r2.ResumedVia, r2.SessionID, r1.SessionID)
	}
	if n := len(e.callsOf("initialize")); n != 2 {
		t.Errorf("%d initialize calls, want a restart", n)
	}
	if !s.Alive() {
		t.Error("restarted process is not alive")
	}
}

func TestSessionProcessDiesDuringTurnThenRestarts(t *testing.T) {
	e := newEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{
		forPrompt("boom", msg("starting"), fakeagent.Step{Op: fakeagent.OpStderr, Text: "out of memory"}, fakeagent.Step{Op: fakeagent.OpExit, Code: 3}),
		forPrompt("again", msg("recovered")),
	}})
	s := e.newSession()
	r1 := e.turn(s, "boom")
	if r1.Status != agent.StatusFailed || r1.FailureReason != agent.ReasonAgentExit || r1.ExitCode == nil || *r1.ExitCode != 3 || !strings.Contains(r1.Stderr, "out of memory") {
		t.Fatalf("crash turn: status=%s reason=%s exit=%v stderr=%q", r1.Status, r1.FailureReason, exitOf(r1), r1.Stderr)
	}
	if s.Alive() || !isClosed(s.Done()) {
		t.Error("a crashed process must not count as alive")
	}

	r2 := e.turn(s, "again")
	e.wantSucceeded(r2)
	if r2.ResumedVia != agent.ResumedLoad || r2.FinalMessage != "recovered" {
		t.Errorf("restart: via=%q message=%q", r2.ResumedVia, r2.FinalMessage)
	}
}

func TestSessionCancelKeepsProcessForNextTurn(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{
		forPrompt("slow", fakeagent.Step{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "work"}}}, fakeagent.Step{Op: fakeagent.OpHang}),
		forPrompt("next", msg("fine")),
	}})
	s := e.newSession()
	ch, cancel := e.startTurn(s, "slow", func(r *agent.TurnRequest) { r.RunID = "slow-turn" })
	e.waitPlan("slow-turn")
	cancel(agent.ErrCancelled)
	r1 := e.waitTurn(ch)

	if r1.Status != agent.StatusKilled || r1.FailureReason != agent.ReasonCancelled || r1.Error != "turn cancelled" {
		t.Fatalf("cancelled turn: status=%s reason=%s err=%q", r1.Status, r1.FailureReason, r1.Error)
	}
	if n := len(e.callsOf("session/cancel")); n != 1 {
		t.Errorf("%d session/cancel calls, want 1", n)
	}
	if !s.Alive() {
		t.Fatal("the agent answered session/cancel, so its process should keep running")
	}

	r2 := e.turn(s, "next")
	e.wantSucceeded(r2)
	if r2.SessionID != r1.SessionID || r2.ResumedVia != "" || r2.FinalMessage != "fine" {
		t.Errorf("next turn: session=%q (was %q) via=%q message=%q", r2.SessionID, r1.SessionID, r2.ResumedVia, r2.FinalMessage)
	}
	if n := len(e.callsOf("initialize")); n != 1 {
		t.Errorf("%d initialize calls: the cancel restarted the process", n)
	}
}

func TestSessionCancelUnansweredRestartsProcess(t *testing.T) {
	e := newEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{
		forPrompt("stuck", fakeagent.Step{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "work"}}}, fakeagent.Step{Op: fakeagent.OpHang, IgnoreCancel: true}),
		forPrompt("next", msg("fine")),
	}})
	s := e.newSession()
	ch, cancel := e.startTurn(s, "stuck", func(r *agent.TurnRequest) { r.RunID = "stuck-turn" })
	e.waitPlan("stuck-turn")
	cancel(agent.ErrCancelled)
	r1 := e.waitTurn(ch)

	if r1.Status != agent.StatusKilled || r1.FailureReason != agent.ReasonCancelled {
		t.Fatalf("status=%s reason=%s err=%q", r1.Status, r1.FailureReason, r1.Error)
	}
	if s.Alive() {
		t.Fatal("an agent that ignored session/cancel must be stopped")
	}
	r2 := e.turn(s, "next")
	e.wantSucceeded(r2)
	if r2.ResumedVia != agent.ResumedLoad || r2.SessionID != r1.SessionID {
		t.Errorf("restart: via=%q session=%q", r2.ResumedVia, r2.SessionID)
	}
}

func TestSessionKillAndInterruptEndTurnAndStopProcess(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		status agent.Status
		reason string
	}{
		{agent.ErrKilled, agent.StatusKilled, agent.ReasonKilled},
		{agent.ErrInterrupted, agent.StatusInterrupted, agent.ReasonInterrupted},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{
				forPrompt("slow", fakeagent.Step{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "work"}}}, fakeagent.Step{Op: fakeagent.OpHang}),
				forPrompt("next", msg("fine")),
			}})
			s := e.newSession()
			ch, cancel := e.startTurn(s, "slow", func(r *agent.TurnRequest) { r.RunID = "slow-turn" })
			e.waitPlan("slow-turn")
			cancel(tc.cause)
			rec := e.waitTurn(ch)
			if rec.Status != tc.status || rec.FailureReason != tc.reason {
				t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
			}
			if s.Alive() {
				t.Error("process still running")
			}
			r2 := e.turn(s, "next")
			e.wantSucceeded(r2)
			if r2.ResumedVia != agent.ResumedLoad {
				t.Errorf("restart: via=%q", r2.ResumedVia)
			}
		})
	}
}

func TestSessionOneTurnAtATime(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{
		forPrompt("slow", fakeagent.Step{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "work"}}}, fakeagent.Step{Op: fakeagent.OpHang}),
	}})
	s := e.newSession()
	ch, cancel := e.startTurn(s, "slow", func(r *agent.TurnRequest) { r.RunID = "slow-turn" })
	e.waitPlan("slow-turn")

	if _, err := s.Prompt(t.Context(), turnReq("slow")); !errors.Is(err, agent.ErrTurnRunning) {
		t.Errorf("second concurrent Prompt: err = %v, want ErrTurnRunning", err)
	}
	s.Suspend() // no-op while a turn runs
	if !s.Alive() {
		t.Error("Suspend stopped the process under a running turn")
	}
	cancel(agent.ErrCancelled)
	e.waitTurn(ch)
}

func TestSessionCloseIdleStopsProcessAndRejectsPrompts(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{
		forPrompt("first", msg("one"), fakeagent.Step{Op: fakeagent.OpSpawnChild, Cmd: []string{"sleep", "300"}, PidFile: "child.pid"}),
	}})
	s := e.newSession()
	e.wantSucceeded(e.turn(s, "first"))
	b, err := os.ReadFile(filepath.Join(e.work, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	done := s.Done()

	s.Close()
	s.Close() // idempotent

	if s.Alive() || !isClosed(done) || !isClosed(s.Done()) {
		t.Errorf("after Close: alive=%v done closed=%v", s.Alive(), isClosed(done))
	}
	waitFor(t, "the agent's child process to die", func() bool { return !pidAlive(childPID) })
	if _, err := s.Prompt(t.Context(), turnReq("first")); !errors.Is(err, agent.ErrSessionClosed) {
		t.Errorf("Prompt after Close: err = %v, want ErrSessionClosed", err)
	}
	if n := len(e.callsOf("session/prompt")); n != 1 {
		t.Errorf("%d prompts reached the agent", n)
	}
}

func TestSessionCloseDuringTurnKillsTurn(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{
		forPrompt("slow", fakeagent.Step{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "work"}}}, fakeagent.Step{Op: fakeagent.OpHang}),
	}})
	s := e.newSession()
	ch, _ := e.startTurn(s, "slow", func(r *agent.TurnRequest) { r.RunID = "slow-turn" })
	e.waitPlan("slow-turn")

	s.Close()
	rec := e.waitTurn(ch)
	if rec.Status != agent.StatusKilled || rec.FailureReason != agent.ReasonKilled {
		t.Errorf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	if s.Alive() || !isClosed(s.Done()) {
		t.Error("Close left the process running")
	}
}

func TestSessionStartFailuresLandOnTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script fakeagent.Script
		mod    func(*env)
		reason string
	}{
		{
			name: "agent exits at startup",
			script: fakeagent.Script{
				Startup: []fakeagent.Step{{Op: fakeagent.OpStderr, Text: "cannot start"}, {Op: fakeagent.OpExit, Code: 7}},
				Turns:   []fakeagent.Turn{{Steps: []fakeagent.Step{msg("never")}}},
			},
			reason: agent.ReasonAgentExit,
		},
		{
			name:   "mode unavailable",
			script: fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("never")}}}},
			mod:    func(e *env) { e.cfg.Modes.Ask = "no-such-mode" },
			reason: agent.ReasonModeUnavailable,
		},
		{
			name:   "binary missing",
			script: fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("never")}}}},
			mod:    func(e *env) { e.cfg.Command = []string{filepath.Join(e.work, "no-such-agent")} },
			reason: agent.ReasonSpawn,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, tc.script)
			if tc.mod != nil {
				tc.mod(e)
			}
			s := e.newSession()
			rec := e.turn(s, "go", func(r *agent.TurnRequest) { r.RunID = "start-fail" })
			if rec.Status != agent.StatusFailed || rec.FailureReason != tc.reason {
				t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
			}
			if s.Alive() {
				t.Error("a failed start left a live process")
			}
			hist := e.rec.RunHistory("start-fail")
			if len(hist) == 0 || hist[len(hist)-1].Status != agent.StatusFailed {
				t.Errorf("recorder did not receive the failed turn: %+v", hist)
			}
			if _, err := s.Prompt(t.Context(), turnReq("go")); err != nil {
				t.Errorf("session must stay usable after a failed start: %v", err)
			}
		})
	}
}

func TestNewSessionAndPromptValidation(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}}})
	good := agent.SessionRequest{Config: e.cfg, Cwd: e.work}
	for _, tc := range []struct {
		name string
		mod  func(*agent.SessionRequest, *agent.Executor)
	}{
		{"empty command", func(r *agent.SessionRequest, _ *agent.Executor) { r.Config.Command = nil }},
		{"missing cwd", func(r *agent.SessionRequest, _ *agent.Executor) { r.Cwd = filepath.Join(e.work, "nope") }},
		{"no recorder", func(_ *agent.SessionRequest, x *agent.Executor) { x.Recorder = nil }},
		{"no broker", func(_ *agent.SessionRequest, x *agent.Executor) { x.Broker = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, x := good, *e.exec
			tc.mod(&req, &x)
			if _, err := x.NewSession(req); err == nil {
				t.Error("invalid session accepted")
			}
		})
	}

	s := e.newSession()
	if _, err := s.Prompt(t.Context(), turnReq("  ")); err == nil {
		t.Error("empty prompt accepted")
	}
	if _, err := s.Prompt(context.Background(), turnReq("go", withAllow(agent.AllowRules{Tools: []string{"("}}))); err == nil {
		t.Error("invalid allow rule accepted")
	}
	if s.Alive() {
		t.Error("an invalid request started the process")
	}
}
