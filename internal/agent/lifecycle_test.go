package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
)

// A run that exceeds its timeout is cancelled over ACP, then the whole
// process group (agent and its child) is killed even if the agent ignores both
// session/cancel and SIGTERM.
func TestTimeoutCancelsAndKillsProcessGroup(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		{Op: fakeagent.OpIgnoreSIGTRM},
		{Op: fakeagent.OpSpawnChild, Cmd: []string{"sleep", "300"}, PidFile: "child.pid"},
		{Op: fakeagent.OpHang, IgnoreCancel: true},
	}}}})
	req := e.req(agent.ModeAuto, "")
	req.Timeout = 20 * time.Minute
	ch, _ := e.start(req)

	var childPID int
	waitFor(t, "child pid file", func() bool {
		b, err := os.ReadFile(filepath.Join(e.work, "child.pid"))
		if err != nil {
			return false
		}
		childPID, err = strconv.Atoi(string(b))
		return err == nil
	})
	if !e.clock.WaitTimers(1, 5*time.Second) {
		t.Fatal("run timeout timer not registered")
	}
	e.clock.Advance(21 * time.Minute)
	rec := e.wait(ch)

	if rec.Status != agent.StatusTimeout || rec.FailureReason != agent.ReasonTimeout {
		t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	if len(e.callsOf("session/cancel")) != 1 {
		t.Error("agent was not sent session/cancel before being signalled")
	}
	if rec.ExitCode == nil || *rec.ExitCode != -1 {
		t.Errorf("agent ignoring SIGTERM must end by SIGKILL (exit -1), got %v", exitOf(rec))
	}
	waitFor(t, "child process to die", func() bool { return !pidAlive(childPID) })
}

// An agent that honours session/cancel answers stopReason cancelled; a pending
// per-tool approval is answered `cancelled` and its wait released.
func TestTimeoutAnswersPendingApprovalCancelled(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		editStep("e1", "a.txt", "a"),
		msg("must not be reached"),
	}}}})
	req := e.req(agent.ModeApprove, agent.ApprovePerTool)
	req.Timeout = 10 * time.Minute
	ch, _ := e.start(req)
	if _, ok := e.broker.Next(10 * time.Second); !ok {
		t.Fatal("approval not queued")
	}
	if !e.clock.WaitTimers(2, 5*time.Second) {
		t.Fatal("timers not registered")
	}
	e.clock.Advance(11 * time.Minute) // fires the run timeout, not the 1h approval timeout
	rec := e.wait(ch)

	if rec.Status != agent.StatusTimeout {
		t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	p := e.perm(rec, "e1")
	if p.Decision != agent.DecisionCancelled || p.DecidedBy != agent.DecidedByPolicy {
		t.Errorf("pending request: %+v", p)
	}
	if len(e.broker.Expired()) != 1 {
		t.Error("the broker's wait was not released")
	}
	if _, ok := e.file("a.txt"); ok {
		t.Error("cancelled edit was applied")
	}
	if strings.Contains(rec.FullMessage, "must not be reached") {
		t.Error("agent continued after cancel")
	}
}

func TestKillSwitchCauseMapsToKilledAndInterrupted(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		status agent.Status
		reason string
	}{
		{agent.ErrKilled, agent.StatusKilled, agent.ReasonKilled},
		{agent.ErrInterrupted, agent.StatusInterrupted, agent.ReasonInterrupted},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
				{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "wait"}}},
				{Op: fakeagent.OpHang},
			}}}})
			req := e.req(agent.ModeAuto, "")
			req.RunID = "kill-run"
			ch, cancel := e.start(req)
			waitFor(t, "plan event", func() bool {
				for _, ev := range e.rec.Events("kill-run") {
					if ev.Kind == "plan" {
						return true
					}
				}
				return false
			})
			cancel(tc.cause)
			rec := e.wait(ch)
			if rec.Status != tc.status || rec.FailureReason != tc.reason {
				t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
			}
			if len(e.callsOf("session/cancel")) != 1 {
				t.Error("agent was not sent session/cancel")
			}
		})
	}
}

func TestAgentCrashFailsRunWithExitCodeAndStderr(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		msg("starting"),
		{Op: fakeagent.OpStderr, Text: "boom: out of memory"},
		{Op: fakeagent.OpExit, Code: 3},
	}}}})
	rec := e.run(e.req(agent.ModeAuto, ""))
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonAgentExit {
		t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 3 {
		t.Errorf("exit code = %v", exitOf(rec))
	}
	if !strings.Contains(rec.Stderr, "boom: out of memory") {
		t.Errorf("stderr tail = %q", rec.Stderr)
	}
	if rec.FullMessage != "starting" {
		t.Errorf("partial output lost: %q", rec.FullMessage)
	}
}

func TestStartupCrashAndMissingBinary(t *testing.T) {
	e := newEnv(t, fakeagent.Script{
		Startup: []fakeagent.Step{{Op: fakeagent.OpStderr, Text: "cannot start"}, {Op: fakeagent.OpExit, Code: 7}},
		Turns:   []fakeagent.Turn{{Steps: []fakeagent.Step{msg("never")}}},
	})
	rec := e.run(e.req(agent.ModeAuto, ""))
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonAgentExit || *rec.ExitCode != 7 || !strings.Contains(rec.Stderr, "cannot start") {
		t.Errorf("startup crash: status=%s reason=%s exit=%v stderr=%q", rec.Status, rec.FailureReason, rec.ExitCode, rec.Stderr)
	}

	req := e.req(agent.ModeAuto, "")
	req.Config.Command = []string{filepath.Join(t.TempDir(), "no-such-agent")}
	rec = e.run(req)
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonSpawn {
		t.Errorf("missing binary: status=%s reason=%s", rec.Status, rec.FailureReason)
	}
}

func TestSessionSetupCapabilitiesMCPAndModel(t *testing.T) {
	e := newEnv(t, fakeagent.Script{
		AgentInfo: &fakeagent.Info{Name: "fake", Version: "9.9"},
		Models:    []string{"m1", "m2"},
		Turns:     []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}},
	})
	e.cfg.SessionMeta = map[string]any{"claudeCode": map[string]any{"options": map[string]any{"settingSources": []string{"project"}}}}
	req := e.req(agent.ModeReadonly, "")
	req.Model = "m2"
	req.MCPServers = []agent.MCPServer{
		{Name: "git", Command: "git-mcp", Args: []string{"serve"}, Env: map[string]string{"K": "V"}},
		{Name: "remote", Type: "http", URL: "https://mcp.example/x", Headers: map[string]string{"Authorization": "Bearer t"}},
	}
	rec := e.run(req)
	if rec.Status != agent.StatusSucceeded {
		t.Fatalf("status=%s err=%q", rec.Status, rec.Error)
	}
	if rec.AgentInfo == nil || rec.AgentInfo.Name != "fake" || rec.AgentInfo.Version != "9.9" {
		t.Errorf("agent info = %+v", rec.AgentInfo)
	}
	var caps map[string]any
	if err := json.Unmarshal(rec.Capabilities, &caps); err != nil {
		t.Fatalf("capabilities not recorded: %v", err)
	}

	init := e.callsOf("initialize")[0]
	cc, _ := init["client_capabilities"].(map[string]any)
	if fs, _ := cc["fs"].(map[string]any); fs["readTextFile"] == true || fs["writeTextFile"] == true {
		t.Errorf("client advertised fs capability: %v", cc)
	}
	if _, has := cc["terminal"]; has {
		t.Errorf("client advertised terminal capability: %v", cc)
	}
	if _, has := cc["elicitation"]; has {
		t.Errorf("elicitation must be off unless the runtime asks for it: %v", cc)
	}

	sn := e.callsOf("session/new")[0]
	if sn["cwd"] != e.work {
		t.Errorf("session cwd = %v", sn["cwd"])
	}
	servers, _ := sn["mcp_servers"].([]any)
	if len(servers) != 2 {
		t.Fatalf("mcp_servers = %v", sn["mcp_servers"])
	}
	if b, _ := json.Marshal(sn["meta"]); !strings.Contains(string(b), "settingSources") {
		t.Errorf("session _meta not passed: %s", b)
	}
	if cfg := e.callsOf("session/set_config_option"); len(cfg) != 1 || cfg[0]["config_id"] != "model" || cfg[0]["value"] != "m2" {
		t.Errorf("model not set: %v", cfg)
	}
}

func TestModelRequestedButAgentHasNoModelOption(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}}})
	req := e.req(agent.ModeAuto, "")
	req.Model = "sonnet"
	rec := e.run(req)
	if rec.Status != agent.StatusSucceeded {
		t.Fatalf("status=%s err=%q", rec.Status, rec.Error)
	}
	if len(e.callsOf("session/set_config_option")) != 0 {
		t.Error("tried to set a model the agent does not offer")
	}
	var noted bool
	for _, ev := range e.rec.Events(rec.ID) {
		noted = noted || (ev.Kind == "harness" && strings.Contains(string(ev.Payload), "set_model"))
	}
	if !noted {
		t.Error("skipping the model must be visible in the transcript")
	}
}

func TestModelRejectedByAgentFailsRun(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Models: []string{"m1"}, Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}}})
	req := e.req(agent.ModeAuto, "")
	req.Model = "nope"
	rec := e.run(req)
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonModelRejected {
		t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	if len(e.callsOf("session/prompt")) != 0 {
		t.Error("prompt must not be sent after a failed setup")
	}
}

// A runtime whose ask mode the agent does not offer must fail before any
// prompt, never run unrestricted.
func TestUnknownSessionModeFailsBeforePrompt(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}}})
	e.cfg.Modes.Ask = "readonly-typo"
	rec := e.run(e.req(agent.ModeReadonly, ""))
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonModeUnavailable {
		t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	if !strings.Contains(rec.Error, "readonly-typo") || !strings.Contains(rec.Error, "default") {
		t.Errorf("error should name the bad and the offered modes: %q", rec.Error)
	}
	if len(e.callsOf("session/prompt")) != 0 {
		t.Error("prompt sent without the policy's session mode")
	}
}

// Runtimes restricted by a process flag instead of a session mode: empty mode
// name skips session/set_mode, and the policy's spawn args/env are applied.
func TestPerPolicySpawnArgsAndEnv(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}}})
	askLog := filepath.Join(t.TempDir(), "ask.jsonl")
	fullLog := filepath.Join(t.TempDir(), "full.jsonl")
	e.cfg.Modes = agent.ModeNames{}
	e.cfg.Ask = agent.PolicySpawn{Args: []string{"--log", askLog}}
	e.cfg.Full = agent.PolicySpawn{Args: []string{"--log", fullLog}}

	rec := e.run(e.req(agent.ModeReadonly, ""))
	if rec.Status != agent.StatusSucceeded {
		t.Fatalf("readonly: %s %q", rec.Status, rec.Error)
	}
	if _, err := os.Stat(askLog); err != nil {
		t.Errorf("readonly run did not get the ask args: %v", err)
	}
	if _, err := os.Stat(fullLog); err == nil {
		t.Error("readonly run got the full args")
	}
	if rec.Argv[len(rec.Argv)-1] != askLog {
		t.Errorf("argv = %v", rec.Argv)
	}

	rec = e.run(e.req(agent.ModeAuto, ""))
	if rec.Status != agent.StatusSucceeded {
		t.Fatalf("auto: %s %q", rec.Status, rec.Error)
	}
	if _, err := os.Stat(fullLog); err != nil {
		t.Errorf("auto run did not get the full args: %v", err)
	}
	if len(e.callsOf("session/set_mode")) != 0 {
		t.Error("empty mode names must not call session/set_mode")
	}
}

func TestRecorderSeesOrderedEventsAndRawFrames(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		{Op: fakeagent.OpThought, Text: "thinking"},
		{Op: fakeagent.OpPlan, Entries: []fakeagent.PlanEntry{{Content: "step one"}}},
		{Op: fakeagent.OpToolCall, ID: "t1", Title: "Read a.txt", Kind: "read", RawInput: map[string]any{"path": "a.txt"}},
		{Op: fakeagent.OpToolUpdate, ID: "t1", Status: "completed", Content: []fakeagent.Content{{Type: "text", Text: "contents"}}},
		{Op: fakeagent.OpMessage, Chunks: []string{"all ", "done"}},
	}}}})
	rec := e.run(e.req(agent.ModeAuto, ""))
	if rec.Status != agent.StatusSucceeded {
		t.Fatalf("status=%s err=%q", rec.Status, rec.Error)
	}
	var kinds []string
	for i, ev := range e.rec.Events(rec.ID) {
		if ev.Seq != i+1 {
			t.Errorf("event %d has seq %d", i, ev.Seq)
		}
		kinds = append(kinds, ev.Kind)
		if ev.Kind == "tool_call" || ev.Kind == "tool_call_update" {
			if ev.ToolCallID != "t1" {
				t.Errorf("%s tool_call_id = %q", ev.Kind, ev.ToolCallID)
			}
		}
		if !json.Valid(ev.Payload) {
			t.Errorf("event %d payload is not JSON: %s", i, ev.Payload)
		}
	}
	got := strings.Join(kinds, ",")
	// set_mode adds a harness event first; message chunks follow the tool call.
	if want := "harness,thought,plan,tool_call,tool_call_update,message,message"; got != want {
		t.Errorf("event kinds = %s, want %s", got, want)
	}
	if rec.FinalMessage != "all done" {
		t.Errorf("final message = %q", rec.FinalMessage)
	}

	var sawPromptSent, sawUpdateRecv bool
	for _, f := range e.rec.RawFrames(rec.ID) {
		line := string(f.Line)
		sawPromptSent = sawPromptSent || (f.Dir == "send" && strings.Contains(line, `"session/prompt"`))
		sawUpdateRecv = sawUpdateRecv || (f.Dir == "recv" && strings.Contains(line, `"session/update"`))
		if !json.Valid(f.Line) {
			t.Errorf("raw frame is not one JSON value: %q", line)
		}
	}
	if !sawPromptSent || !sawUpdateRecv {
		t.Errorf("raw frames incomplete: prompt sent=%v update received=%v", sawPromptSent, sawUpdateRecv)
	}
}

// Final answer split around tool calls: the note is read from the last text
// run; a malformed note keeps the raw message and reports the parse error.
func TestStructuredNoteFromFinalMessage(t *testing.T) {
	note := "Here is my triage.\n\n```json\n{\"title\":\"Review PR 12\",\"priority\":\"high\",\"summary_md\":\"Looks risky.\",\"links\":[{\"label\":\"PR\",\"url\":\"https://example.test/pr/12\"}]}\n```\n"
	t.Run("fenced json", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
			msg("let me look"),
			{Op: fakeagent.OpToolCall, ID: "t", Title: "Read", Kind: "read"},
			{Op: fakeagent.OpToolUpdate, ID: "t"},
			msg(note),
		}}}})
		req := e.req(agent.ModeReadonly, "")
		req.ExpectNote = true
		rec := e.run(req)
		if rec.Note == nil || rec.NoteParseError != "" {
			t.Fatalf("note=%v parse error=%q final=%q", rec.Note, rec.NoteParseError, rec.FinalMessage)
		}
		if rec.Note.Title != "Review PR 12" || rec.Note.Priority != "high" || len(rec.Note.Links) != 1 {
			t.Errorf("note = %+v", rec.Note)
		}
	})
	t.Run("parse failure falls back to the raw message", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("I could not produce JSON, sorry.")}}}})
		req := e.req(agent.ModeReadonly, "")
		req.ExpectNote = true
		rec := e.run(req)
		if rec.Status != agent.StatusSucceeded {
			t.Fatalf("a parse failure must not fail the run: %s", rec.Status)
		}
		if rec.Note != nil || rec.NoteParseError == "" {
			t.Errorf("note=%v parse error=%q", rec.Note, rec.NoteParseError)
		}
		if rec.FinalMessage != "I could not produce JSON, sorry." {
			t.Errorf("raw final message = %q", rec.FinalMessage)
		}
	})
	t.Run("no note expected means no parsing", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("plain text")}}}})
		rec := e.run(e.req(agent.ModeReadonly, ""))
		if rec.Note != nil || rec.NoteParseError != "" {
			t.Errorf("note=%v parse error=%q", rec.Note, rec.NoteParseError)
		}
	})
}

// omp asks for non-execute tools through elicitation/create: the same policy
// and approval queue apply, and it is advertised only when configured.
func TestElicitationPermissions(t *testing.T) {
	script := func() fakeagent.Script {
		return fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
			{Op: fakeagent.OpToolCall, ID: "w1", Kind: "edit", Title: "Write note.txt", RawInput: map[string]any{"path": "note.txt", "content": "probe"}},
			{Op: fakeagent.OpPermission, ID: "w1", Kind: "edit", Title: "Write note.txt", NoToolCall: true, Via: "elicitation", Name: "write", Text: "note.txt",
				OnAllow: []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "note.txt", Data: "probe"}}},
			msg("done"),
		}}}}
	}

	t.Run("gated: queued, allowed, applied", func(t *testing.T) {
		e := newEnv(t, script())
		e.cfg.Elicitation = true
		ch, _ := e.start(e.req(agent.ModeApprove, agent.ApprovePerTool))
		a, ok := e.broker.Next(10 * time.Second)
		if !ok {
			t.Fatal("elicitation was not queued for approval")
		}
		if a.ToolKind != "edit" || a.ToolCallID != "w1" || !strings.Contains(a.Title, "note.txt") {
			t.Errorf("approval = %+v", a)
		}
		e.broker.Decide(a.PermissionRequestID, agent.ToolDecision{Verdict: agent.VerdictAllow})
		rec := e.wait(ch)
		if got, _ := e.file("note.txt"); got != "probe" || rec.Status != agent.StatusSucceeded {
			t.Errorf("file=%q status=%s err=%q", got, rec.Status, rec.Error)
		}
		if p := e.perm(rec, "w1"); p.Decision != agent.DecisionAllow || p.DecidedBy != agent.DecidedByUser {
			t.Errorf("permission: %+v", p)
		}
	})

	t.Run("readonly: declined by policy", func(t *testing.T) {
		e := newEnv(t, script())
		e.cfg.Elicitation = true
		rec := e.run(e.req(agent.ModeReadonly, ""))
		if _, ok := e.file("note.txt"); ok {
			t.Error("read-only agent wrote a file through elicitation")
		}
		if p := e.perm(rec, "w1"); p.Decision != agent.DecisionReject || p.MatchedRule != "policy:readonly" {
			t.Errorf("permission: %+v", p)
		}
	})

	t.Run("not advertised: the agent refuses by itself", func(t *testing.T) {
		e := newEnv(t, script())
		rec := e.run(e.req(agent.ModeApprove, agent.ApprovePerTool))
		if _, ok := e.file("note.txt"); ok {
			t.Error("file written although elicitation was not advertised")
		}
		if len(e.rec.Permissions(rec.ID)) != 0 {
			t.Error("no permission request should reach the harness")
		}
	})
}

func TestInvalidRequestsAreRejectedBeforeSpawning(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("ok")}}}})
	good := e.req(agent.ModeAuto, "")
	for name, mut := range map[string]func(*agent.RunRequest){
		"bad mode":     func(r *agent.RunRequest) { r.Mode = "yolo" },
		"bad style":    func(r *agent.RunRequest) { r.Mode, r.Approve = agent.ModeApprove, "sometimes" },
		"no command":   func(r *agent.RunRequest) { r.Config.Command = nil },
		"no cwd":       func(r *agent.RunRequest) { r.Cwd = "" },
		"missing cwd":  func(r *agent.RunRequest) { r.Cwd = filepath.Join(r.Cwd, "nope") },
		"empty prompt": func(r *agent.RunRequest) { r.Prompt = " " },
		"bad allow re": func(r *agent.RunRequest) { r.Allow.Commands = []string{"("} },
		"bad tools re": func(r *agent.RunRequest) { r.Allow.Tools = []string{"[a"} },
	} {
		req := good
		mut(&req)
		if _, err := e.exec.Run(t.Context(), req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(e.calls()) != 0 {
		t.Error("an invalid request started the agent")
	}
	noBroker := *e.exec
	noBroker.Broker = nil
	if _, err := noBroker.Run(t.Context(), e.req(agent.ModeApprove, agent.ApprovePerTool)); err == nil {
		t.Error("approve mode without a broker accepted")
	}
}

// codex-acp's only reject option cancels the whole turn. The run must not be
// reported as a mysterious cancelled stop: a user deny is `denied`, a policy
// rejection fails with its own reason.
func TestRejectThatEndsTheTurnIsClassified(t *testing.T) {
	script := func() fakeagent.Script {
		step := editStep("e1", "a.txt", "a")
		step.CancelOnReject = true
		return fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{step, msg("never reached")}}}}
	}

	t.Run("user deny", func(t *testing.T) {
		e := newEnv(t, script())
		ch, _ := e.start(e.req(agent.ModeApprove, agent.ApprovePerTool))
		a, ok := e.broker.Next(10 * time.Second)
		if !ok {
			t.Fatal("approval not queued")
		}
		e.broker.Decide(a.PermissionRequestID, agent.ToolDecision{Verdict: agent.VerdictDeny})
		rec := e.wait(ch)
		if rec.Status != agent.StatusDenied || rec.FailureReason != agent.ReasonDenied {
			t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
		}
	})

	t.Run("policy rejection", func(t *testing.T) {
		e := newEnv(t, script())
		rec := e.run(e.req(agent.ModeReadonly, ""))
		if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonPolicyEndedTurn {
			t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
		}
		if !strings.Contains(rec.Error, "Edit a.txt") {
			t.Errorf("error should name the rejected call: %q", rec.Error)
		}
	})
}

// Agents that offer persistent allow options (codex execpolicy amendment,
// claude allow_always) must only ever get a one-shot allow.
func TestAllowNeverSelectsPersistentOption(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
		{Op: fakeagent.OpPermission, ID: "x1", Kind: "execute", Title: "touch f", RawInput: map[string]any{"command": "touch f"},
			Options: []fakeagent.Option{
				{ID: "amend", Kind: "allow_always"},
				{ID: "once", Kind: "allow_once"},
				{ID: "cancel", Kind: "reject_once"},
			},
			OnAllow: []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "f", Data: "x"}}},
		{Op: fakeagent.OpPermission, ID: "x2", Kind: "execute", Title: "touch g", RawInput: map[string]any{"command": "touch g"},
			Options: []fakeagent.Option{{ID: "amend", Kind: "allow_always"}, {ID: "cancel", Kind: "reject_once"}},
			OnAllow: []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: "g", Data: "x"}}},
		msg("done"),
	}}}})
	rec := e.run(e.req(agent.ModeAuto, ""))
	if _, ok := e.file("f"); !ok {
		t.Error("one-shot allow was not chosen")
	}
	if _, ok := e.file("g"); ok {
		t.Error("persistent allow option was chosen when no one-shot existed")
	}
	if p := e.perm(rec, "x2"); p.Decision != agent.DecisionCancelled || !strings.Contains(p.Reason, "no allow option") {
		t.Errorf("x2: %+v", p)
	}
	outs := e.callsOf("permission_outcome")
	if len(outs) < 1 || outs[0]["option_id"] != "once" {
		t.Errorf("outcomes = %v", outs)
	}
}
