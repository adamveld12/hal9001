package agent_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/agent/agenttest"
	"hal9001/internal/agent/fakeagent"
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

// env is one test's fake agent, executor and in-memory ports.
type env struct {
	t      *testing.T
	work   string // agent cwd
	script string
	log    string
	rec    *agenttest.Recorder
	broker *agenttest.Broker
	clock  *agenttest.ManualClock
	exec   *agent.Executor
	cfg    agent.RuntimeConfig
}

var twoModes = []fakeagent.Mode{{ID: "default"}, {ID: "bypass"}}

func newEnv(t *testing.T, s fakeagent.Script) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{
		t:      t,
		work:   filepath.Join(dir, "work"),
		script: filepath.Join(dir, "script.json"),
		log:    filepath.Join(dir, "calls.jsonl"),
		rec:    agenttest.NewRecorder(),
		broker: agenttest.NewBroker(),
		clock:  agenttest.NewManualClock(),
	}
	if err := os.MkdirAll(e.work, 0o750); err != nil {
		t.Fatal(err)
	}
	if s.Modes == nil {
		s.Modes = twoModes
	}
	s.SessionsDir = filepath.Join(dir, "sessions")
	s.LogPath = e.log
	if err := s.Write(e.script); err != nil {
		t.Fatal(err)
	}
	e.cfg = agent.RuntimeConfig{
		Command: []string{fakeBin, "--script", e.script},
		Modes:   agent.ModeNames{Ask: "default", Full: "bypass"},
	}
	e.exec = &agent.Executor{
		Recorder:      e.rec,
		Broker:        e.broker,
		Clock:         e.clock,
		Environ:       os.Environ(),
		KillGrace:     300 * time.Millisecond,
		CancelGrace:   500 * time.Millisecond,
		ShutdownGrace: 3 * time.Second,
	}
	return e
}

func (e *env) req(mode agent.Mode, style agent.ApproveStyle) agent.RunRequest {
	return agent.RunRequest{
		NodeRunID: "node-run-1",
		Runtime:   "fake",
		Config:    e.cfg,
		Mode:      mode,
		Approve:   style,
		Prompt:    "Review the change.",
		Cwd:       e.work,
		// Only tests that advance the clock care about these.
		Timeout:         24 * time.Hour,
		ApprovalTimeout: time.Hour,
	}
}

func (e *env) run(req agent.RunRequest) agent.RunRecord {
	e.t.Helper()
	rec, err := e.exec.Run(e.t.Context(), req)
	if err != nil {
		e.t.Fatalf("Run: %v", err)
	}
	return rec
}

type result struct {
	rec agent.RunRecord
	err error
}

// start runs req in the background (for tests that interact mid-run).
func (e *env) start(req agent.RunRequest) (chan result, func(cause error)) {
	ctx, cancel := withCause(e.t)
	ch := make(chan result, 1)
	go func() {
		rec, err := e.exec.Run(ctx, req)
		ch <- result{rec, err}
	}()
	return ch, cancel
}

func (e *env) wait(ch chan result) agent.RunRecord {
	e.t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			e.t.Fatalf("Run: %v", r.err)
		}
		return r.rec
	case <-time.After(30 * time.Second):
		e.t.Fatal("run did not finish")
		return agent.RunRecord{}
	}
}

// calls reads the fake agent's call log.
func (e *env) calls() []map[string]any {
	e.t.Helper()
	f, err := os.Open(e.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			e.t.Fatalf("bad call log line %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func (e *env) callsOf(method string) []map[string]any {
	var out []map[string]any
	for _, c := range e.calls() {
		if c["method"] == method {
			out = append(out, c)
		}
	}
	return out
}

func (e *env) file(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.work, name)) //nolint:gosec // test temp dir
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (e *env) perm(rec agent.RunRecord, toolCallID string) agent.PermissionRecord {
	e.t.Helper()
	for _, p := range e.rec.Permissions(rec.ID) {
		if p.ToolCallID == toolCallID {
			return p
		}
	}
	e.t.Fatalf("no permission request recorded for tool call %q (have %+v)", toolCallID, e.rec.Permissions(rec.ID))
	return agent.PermissionRecord{}
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// editStep is a permission-gated file write.
func editStep(id, file, content string) fakeagent.Step {
	return fakeagent.Step{
		Op: fakeagent.OpPermission, ID: id, Kind: "edit", Title: "Edit " + file,
		RawInput: map[string]any{"path": file, "content": content},
		Content:  []fakeagent.Content{{Type: "diff", Path: file, NewText: content}},
		OnAllow:  []fakeagent.Step{{Op: fakeagent.OpWriteFile, Path: file, Data: content}},
	}
}

func msg(text string) fakeagent.Step { return fakeagent.Step{Op: fakeagent.OpMessage, Text: text} }

func withCause(t *testing.T) (context.Context, func(cause error)) {
	ctx, cancel := context.WithCancelCause(t.Context())
	t.Cleanup(func() { cancel(nil) })
	return ctx, cancel
}

func exitOf(r agent.RunRecord) any {
	if r.ExitCode == nil {
		return nil
	}
	return *r.ExitCode
}
