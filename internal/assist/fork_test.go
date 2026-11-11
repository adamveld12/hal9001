package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/agenttest"
	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

var fakeBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fake-acp-agent")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeBin, err = agenttest.BuildFakeAgent(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// forkHost is the engine as the manager sees it, with the fake ACP agent as the only runtime. The tool hub is real; the
// providers a test adds stand in for the sandbox and custom tool providers of the daemon.
type forkHost struct {
	t        *testing.T
	runtime  agent.RuntimeConfig
	hub      *tools.Hub
	fork     core.ForkNode
	forkErr  error
	emit     []tools.Tool
	spawned  sync.WaitGroup
	recorder *agenttest.Recorder
}

func (h *forkHost) Context() context.Context { return h.t.Context() }
func (h *forkHost) Spawn(fn func(ctx context.Context)) bool {
	h.spawned.Add(1)
	go func() {
		defer h.spawned.Done()
		fn(h.t.Context())
	}()
	return true
}
func (h *forkHost) Track(string, context.CancelCauseFunc) func() { return func() {} }
func (h *forkHost) Runtime(name string) (agent.RuntimeConfig, bool) {
	return h.runtime, name == "fake"
}
func (h *forkHost) Env(string) (map[string]string, map[string]string, *runner.Redactor) {
	return map[string]string{}, map[string]string{}, runner.NewRedactor()
}
func (h *forkHost) ToolEnv(string) (map[string]string, map[string]string, *runner.Redactor) {
	return map[string]string{}, map[string]string{}, runner.NewRedactor()
}
func (h *forkHost) BasePATH() string                       { return os.Getenv("PATH") }
func (h *forkHost) Recorder(string, string) agent.Recorder { return h.recorder }
func (h *forkHost) Broker() agent.ApprovalBroker           { return agenttest.NewBroker() }
func (h *forkHost) Tools() *tools.Hub                      { return h.hub }
func (h *forkHost) Clock() agent.Clock                     { return agenttest.NewManualClockAt(time.Now()) }
func (h *forkHost) TestFlow(context.Context, core.FlowTestRequest) (core.AssistFlowTestResult, error) {
	return core.AssistFlowTestResult{}, errors.New("not used")
}
func (h *forkHost) ForkNode(_ context.Context, id string) (core.ForkNode, error) {
	if h.forkErr != nil {
		return core.ForkNode{}, h.forkErr
	}
	f := h.fork
	if id != f.AgentRunID {
		return core.ForkNode{}, fmt.Errorf("%w: agent run %s", store.ErrNotFound, id)
	}
	return f, nil
}
func (h *forkHost) EmitTools(context.Context, core.ForkNode, string) ([]tools.Tool, error) {
	return h.emit, nil
}

type hubHost struct{}

func (hubHost) Approve(context.Context, tools.ApprovalRequest) (tools.Decision, error) {
	return tools.Decision{Allowed: true}, nil
}
func (hubHost) Record(context.Context, tools.CallRecord) (string, error) { return "rec", nil }
func (hubHost) Finish(context.Context, string, string)                   {}

type tokens struct{}

func (tokens) CreateToken(context.Context, string, string, string) error { return nil }
func (tokens) RevokeToken(context.Context, string) error                 { return nil }

type forkEnv struct {
	t    *testing.T
	st   *store.Store
	m    *Manager
	host *forkHost
	run  *store.AgentRun
	log  string
}

// newForkEnv has a finished agent run of node `work` of flow `f` in the store (prompt, a message, a tool call) and a
// manager whose runtime is the fake agent playing script s.
func newForkEnv(t *testing.T, s fakeagent.Script, mod func(*forkEnv)) *forkEnv {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e := &forkEnv{t: t, st: st, log: filepath.Join(home, "calls.jsonl")}

	ctx := context.Background()
	fr := &store.FlowRun{FlowID: "f", FlowHash: "h1", FlowYAML: "id: f", Kind: store.KindLive, Status: store.FlowRunSucceeded}
	must(t, st.CreateFlowRun(ctx, fr))
	nr := &store.NodeRun{FlowRunID: fr.ID, NodeID: "work", NodeType: store.NodeAgent, Status: store.NodeDone}
	must(t, st.CreateNodeRun(ctx, nr))
	end := time.Now()
	e.run = &store.AgentRun{
		NodeRunID: nr.ID, Runtime: "fake", Mode: store.ModeAuto, Policy: store.PolicyAuto, Phase: store.PhaseExecute, Status: store.AgentSucceeded,
		Prompt: store.Inline("Triage item 7"), Result: store.Inline(`{"final_message":"Looks fine."}`), SessionID: "orig-session", Cwd: filepath.Join(home, "orig-cwd"),
		Capabilities: json.RawMessage(`{"loadSession":true}`), StartedAt: &end, EndedAt: &end,
	}
	must(t, st.CreateAgentRun(ctx, e.run))
	must(t, st.AppendAgentEvent(ctx, &store.AgentEvent{AgentRunID: e.run.ID, Kind: "message", Payload: store.Inline(`{"content":{"type":"text","text":"Looks fine."}}`)}))

	if s.Modes == nil {
		s.Modes = []fakeagent.Mode{{ID: "default"}, {ID: "bypass"}}
	}
	s.SessionsDir, s.LogPath = filepath.Join(home, "sessions"), e.log
	script := filepath.Join(home, "script.json")
	must(t, s.Write(script))
	hub, err := tools.NewHub(tools.Options{Store: tokens{}, Host: hubHost{}})
	must(t, err)
	e.host = &forkHost{
		t: t, hub: hub, recorder: agenttest.NewRecorder(),
		runtime: agent.RuntimeConfig{Command: []string{fakeBin, "--script", script}, Modes: agent.ModeNames{Ask: "default", Full: "bypass"}},
		fork: core.ForkNode{
			AgentRunID: e.run.ID, NodeRunID: nr.ID, FlowRunID: fr.ID, FlowID: "f", NodeID: "work", FlowHash: "h1", Runtime: "fake",
			Cwd: e.run.Cwd, SessionID: "orig-session", LoadSession: true,
			Ports: []core.ForkPort{{Name: "out", Next: []string{`inbox note "done"`}}},
		},
	}
	if mod != nil {
		mod(e)
	}
	e.m, err = New(Options{Store: st, Home: home, Host: e.host, Executable: "/bin/true", URL: "http://127.0.0.1:1"})
	must(t, err)
	t.Cleanup(func() {
		e.m.Shutdown()
		e.host.spawned.Wait()
	})
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *forkEnv) wait(id string) *store.AssistSession {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		a, err := e.st.GetAssistSession(context.Background(), id)
		must(e.t, err)
		if a.Status == store.AssistIdle || a.Status == store.AssistFailed {
			return a
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("session %s still %s", id, a.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *forkEnv) messages(id string) []store.AssistMessage {
	e.t.Helper()
	msgs, err := e.st.ListAssistMessages(context.Background(), id)
	must(e.t, err)
	return msgs
}

func ofKind(msgs []store.AssistMessage, kind string) []string {
	var out []string
	for _, m := range msgs {
		if m.Kind == kind && m.Text != nil {
			out = append(out, *m.Text)
		}
	}
	return out
}

func say(text string) fakeagent.Script {
	return fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{{Op: fakeagent.OpMessage, Text: text}}}}}
}

// The tool session of a fork has the node's tools and sandbox settings (the providers see them in the run spec), the
// emit tools of the engine and the chat's read tools; the hooks of the conversation sandbox run before and after every
// turn and what they report becomes notice messages.
func TestForkedChatGetsTheNodesToolsAndRunsTheTurnHooks(t *testing.T) {
	var spec0 atomic.Pointer[tools.RunSpec]
	var before, after atomic.Int32
	e := newForkEnv(t, say("Hello from the chat."), func(e *forkEnv) {
		e.host.fork.Sandbox, e.host.fork.Image, e.host.fork.Repo, e.host.fork.Ref = "docker", "alpine", "acme/app", "main"
		e.host.fork.ToolIDs = []string{"shout"}
		e.host.fork.ToolApprove = map[string]spec.ToolApprove{"shout": {Mode: "never"}}
		e.host.fork.Input = json.RawMessage(`{"id":7}`)
		e.host.emit = []tools.Tool{{Name: "emit_out", Description: "Send the result on.", InputSchema: map[string]any{"type": "object"},
			Run: func(context.Context, tools.Call) (tools.Result, error) { return tools.Result{Text: "ok"}, nil }}}
		e.host.hub.AddProvider(func(_ context.Context, rs tools.RunSpec) (tools.Extension, error) {
			if rs.Conversation == nil {
				return tools.Extension{}, nil
			}
			spec0.Store(&rs)
			return tools.Extension{
				Tools: []tools.Tool{{Name: "shell", Description: "Run a command.", InputSchema: map[string]any{"type": "object"},
					Run: func(context.Context, tools.Call) (tools.Result, error) { return tools.Result{Text: "ok"}, nil }}},
				BeforeTurn: func(context.Context) ([]string, error) {
					return []string{fmt.Sprintf("Sandbox notice %d.", before.Add(1))}, nil
				},
				AfterTurn: func() { after.Add(1) },
			}, nil
		})
	})

	sess, err := e.m.Fork(context.Background(), e.run.ID)
	must(t, err)
	if sess.TargetKind != store.AssistTargetGeneral || sess.ForkedFromAgentRunID != e.run.ID || sess.Runtime != "fake" || sess.Status != store.AssistIdle || sess.ForkContext != "" {
		t.Fatalf("fork = %+v", sess)
	}
	// A sandboxed node's sandbox is taken over when the chat is created: the hook ran once and its notice is in the chat.
	if before.Load() != 1 {
		t.Errorf("before-turn hooks at creation = %d, want 1", before.Load())
	}
	msgs := e.messages(sess.ID)
	if got := ofKind(msgs, store.MessageDivider); len(got) != 1 || got[0] != "Continued from run "+e.run.ID {
		t.Errorf("dividers = %v", got)
	}
	if got := ofKind(msgs, store.MessageNotice); !slices.Equal(got, []string{"Sandbox notice 1."}) {
		t.Errorf("notices at creation = %v", got)
	}
	rs := spec0.Load()
	if rs == nil || rs.Kind != tools.KindAssist || rs.Sandbox != "docker" || rs.Image != "alpine" || rs.Repo != "acme/app" || rs.Ref != "main" || !slices.Equal(rs.Tools, []string{"shout"}) ||
		rs.ToolApprove["shout"].Mode != "never" || rs.Conversation == nil || rs.Conversation.ConversationID != sess.ID || rs.Conversation.FromAgentRunID != e.run.ID ||
		rs.Conversation.NodeRunID != e.host.fork.NodeRunID || string(rs.Conversation.Input) != `{"id":7}` || len(rs.ExtraTools) != 1 || rs.ExtraTools[0].Name != "emit_out" {
		t.Fatalf("run spec of the forked session = %+v", rs)
	}

	if _, err := e.m.SendMessage(context.Background(), sess.ID, "hello?", nil, nil); err != nil {
		t.Fatal(err)
	}
	e.wait(sess.ID)
	msgs = e.messages(sess.ID)
	if got := ofKind(msgs, store.MessageNotice); !slices.Equal(got, []string{"Sandbox notice 1.", "Sandbox notice 2."}) {
		t.Errorf("notices after the first turn = %v", got)
	}
	if after.Load() != 1 {
		t.Errorf("after-turn hooks = %d, want 1", after.Load())
	}
	// The notice is in the conversation before the agent's answer.
	var order []string
	for _, m := range msgs {
		switch {
		case m.Kind == store.MessageNotice && *m.Text == "Sandbox notice 2.":
			order = append(order, "notice")
		case m.Role == "assistant":
			order = append(order, "assistant")
		}
	}
	if !slices.Equal(order, []string{"notice", "assistant"}) {
		t.Errorf("order = %v", order)
	}

	// The tool set: the node's tools, the emit tools and the read tools of a general chat, but no draft tools.
	e.m.mu.Lock()
	s := e.m.sessions[sess.ID]
	e.m.mu.Unlock()
	s.mu.Lock()
	infos := s.halTools.Tools()
	s.mu.Unlock()
	var names []string
	for _, ti := range infos {
		names = append(names, ti.Name)
	}
	for _, want := range []string{"shell", "emit_out", "flows_list", "agent_run_events", "conversation_messages", "navigate_ui", "start_draft", "github_issue_link"} {
		if !slices.Contains(names, want) {
			t.Errorf("the forked chat has no tool %s: %v", want, names)
		}
	}
	for _, not := range []string{"draft_read", "draft_write", "draft_test", "validate"} {
		if slices.Contains(names, not) {
			t.Errorf("a forked chat has the draft tool %s", not)
		}
	}
	// The prompt names the run, the node's tools and the emit rule.
	var prompt string
	for _, c := range e.calls() {
		if c["method"] == "session/prompt" {
			prompt, _ = c["text"].(string)
		}
	}
	for _, want := range []string{"continuation of agent run " + e.run.ID, "sandbox `docker` (image alpine, repo acme/app at main)", "Custom tools: shout", "- emit_out:", `Leads to: inbox note "done"`, "explicitly asks"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func (e *forkEnv) calls() []map[string]any {
	e.t.Helper()
	b, err := os.ReadFile(e.log)
	if os.IsNotExist(err) {
		return nil
	}
	must(e.t, err)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var c map[string]any
		if json.Unmarshal([]byte(line), &c) == nil {
			out = append(out, c)
		}
	}
	return out
}

// A fork resumes only when the run's working directory still exists; otherwise it replays, whatever the runtime can do.
func TestForkReplaysWhenTheRunsWorkingDirectoryIsGone(t *testing.T) {
	e := newForkEnv(t, say("Hello."), nil) // the run's cwd was never created
	sess, err := e.m.Fork(context.Background(), e.run.ID)
	must(t, err)
	if _, err := e.m.SendMessage(context.Background(), sess.ID, "go on", nil, nil); err != nil {
		t.Fatal(err)
	}
	a := e.wait(sess.ID)
	if a.ForkContext != store.ForkReplayed || a.Status != store.AssistIdle {
		t.Fatalf("session = %+v", a)
	}
	for _, c := range e.calls() {
		if c["method"] == "session/load" {
			t.Fatalf("session/load although the working directory is gone: %v", c)
		}
	}
	if got := ofKind(e.messages(sess.ID), store.MessageDivider); len(got) != 1 || got[0] != "Continued from run "+e.run.ID+"; earlier context replayed" {
		t.Errorf("divider = %v", got)
	}
}

func TestForkRefusesWhatCannotBeContinued(t *testing.T) {
	e := newForkEnv(t, say("x"), nil)
	ctx := context.Background()
	if _, err := e.m.Fork(ctx, "not-an-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a malformed id: %v, want ErrNotFound", err)
	}
	if _, err := e.m.Fork(ctx, "01JC7Z3K9Q8V5T2M4N6P8R0S1T"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown run: %v, want ErrNotFound", err)
	}

	// An assist turn is an agent run without a node: the host reports it as not found.
	sess := &store.AssistSession{TargetKind: store.AssistTargetGeneral, Runtime: "fake", DraftHash: "h", DraftUpdatedBy: store.DraftByUser}
	must(t, e.st.CreateAssistSession(ctx, sess))
	turn := &store.AgentRun{AssistSessionID: sess.ID, Runtime: "fake", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated, Phase: store.PhaseExecute, Status: store.AgentSucceeded}
	must(t, e.st.CreateAgentRun(ctx, turn))
	if _, err := e.m.Fork(ctx, turn.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an assist turn: %v, want ErrNotFound", err)
	}

	// A run that has not ended.
	e.run.Status = store.AgentRunning
	e.run.EndedAt = nil
	must(t, e.st.UpdateAgentRun(ctx, e.run))
	if _, err := e.m.Fork(ctx, e.run.ID); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("a running run: %v, want ErrInvalidState", err)
	}
	e.run.Status = store.AgentAwaitingApproval
	must(t, e.st.UpdateAgentRun(ctx, e.run))
	if _, err := e.m.Fork(ctx, e.run.ID); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("a run awaiting approval: %v, want ErrInvalidState", err)
	}

	// A node whose runtime is not configured any more.
	e.run.Status = store.AgentSucceeded
	must(t, e.st.UpdateAgentRun(ctx, e.run))
	e.host.fork.Runtime = "gone"
	var verr *core.ValidationError
	if _, err := e.m.Fork(ctx, e.run.ID); !errors.As(err, &verr) {
		t.Errorf("a runtime that is gone: %v, want a validation error", err)
	}
	if page, err := e.st.ListAssistSessions(ctx, store.AssistSessionFilter{}); err != nil || len(page.Items) != 1 {
		t.Errorf("refused forks left chats behind: %d, %v", len(page.Items), err)
	}
}
