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
// rejection is continued with a follow-up prompt in the same session and fails
// with its own reason only if the agent keeps ending its turns that way.
func TestRejectThatEndsTheTurnIsClassified(t *testing.T) {
	// turn builds the agent's n-th answer in the session (0-based).
	turn := func(n int, steps ...fakeagent.Step) fakeagent.Turn {
		return fakeagent.Turn{When: &fakeagent.When{SessionTurn: &n}, Steps: steps}
	}
	cancelling := func(n int) fakeagent.Turn {
		step := editStep("e1", "a.txt", "a")
		step.CancelOnReject = true
		return turn(n, step, msg("never reached"))
	}

	t.Run("policy rejection is continued in the same session", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{cancelling(0), turn(1, msg("plan: create a.txt"))}})
		rec := e.run(e.req(agent.ModeReadonly, ""))
		if rec.Status != agent.StatusSucceeded {
			t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
		}
		if rec.FinalMessage != "plan: create a.txt" {
			t.Errorf("final message = %q, want the answer to the follow-up", rec.FinalMessage)
		}
		prompts := e.callsOf("session/prompt")
		if len(prompts) != 2 || !strings.Contains(prompts[1]["text"].(string), "Edit a.txt") {
			t.Fatalf("prompts = %v, want the original and a follow-up naming the rejected call", prompts)
		}
		if _, ok := e.file("a.txt"); ok {
			t.Error("the rejected edit was applied")
		}
	})

	t.Run("an agent that keeps trying fails with the policy reason", func(t *testing.T) {
		e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{cancelling(0), cancelling(1), cancelling(2)}})
		rec := e.run(e.req(agent.ModeReadonly, ""))
		if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonPolicyEndedTurn {
			t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
		}
		if !strings.Contains(rec.Error, "Edit a.txt") {
			t.Errorf("error should name the rejected call: %q", rec.Error)
		}
		if n := len(e.callsOf("session/prompt")); n != 3 {
			t.Errorf("prompts = %d, want the original plus 2 follow-ups", n)
		}
	})
}

// An agent that dies before the handshake says why on stderr; the run error
// must carry it, so a broken adapter install is diagnosable from the run page.
func TestAgentDyingAtStartupReportsItsStderr(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("x")}}}})
	e.cfg.Command = []string{"sh", "-c", "echo 'No version is set for shim' >&2; exit 1"}
	rec := e.run(e.req(agent.ModeReadonly, ""))
	if rec.Status != agent.StatusFailed || rec.FailureReason != agent.ReasonAgentExit {
		t.Fatalf("status=%s reason=%s err=%q", rec.Status, rec.FailureReason, rec.Error)
	}
	if !strings.Contains(rec.Error, "stderr: No version is set for shim") {
		t.Errorf("error lacks the agent's stderr: %q", rec.Error)
	}
}
