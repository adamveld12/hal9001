package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/agent/agenttest"
	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
)

// memLog is the RunLog of one sandbox session in tests.
type memLog struct {
	mu           sync.Mutex
	conversation string // RunSpec.Conversation.ConversationID, empty for a run's session
	agentRun     string
	entries      []sandbox.LogEntry
	updates      []sandbox.SessionUpdate
}

func (l *memLog) Add(e sandbox.LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, e)
}

func (l *memLog) Update(u sandbox.SessionUpdate) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updates = append(l.updates, u)
}

func (l *memLog) has(titlePart string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if strings.Contains(e.Title, titlePart) {
			return true
		}
	}
	return false
}

func (l *memLog) closed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, u := range l.updates {
		if u.Status == sandbox.StatusClosed {
			return true
		}
	}
	return false
}

type keepEnv struct {
	t      *testing.T
	home   string
	root   string
	clock  *agenttest.ManualClock
	keeper *sandbox.Keeper
	prov   Provider

	mu   sync.Mutex
	logs []*memLog
}

func newKeepEnv(t *testing.T, keepAlive string) *keepEnv {
	t.Helper()
	e := &keepEnv{t: t, home: t.TempDir(), root: filepath.Join(t.TempDir(), "sbx"), clock: agenttest.NewManualClock()}
	cfg := "enabled: true\nroot: " + e.root + "\nlifecycle: per_run\napproval: auto\n"
	if keepAlive != "" {
		cfg += "keep_alive: " + keepAlive + "\n"
	}
	if _, err := spec.WriteFile(e.home, spec.KindSandbox, "local", []byte(cfg), ""); err != nil {
		t.Fatal(err)
	}
	m := sandbox.NewManager(sandbox.ManagerOptions{Home: e.home})
	t.Cleanup(m.Close)
	e.keeper = sandbox.NewKeeper(sandbox.KeeperOptions{Clock: e.clock})
	t.Cleanup(e.keeper.CloseAll)
	e.prov = SandboxProvider(m, SandboxOptions{
		Keeper: e.keeper,
		Log: func(_ context.Context, rs RunSpec, _ RunInfo) sandbox.RunLog {
			l := &memLog{agentRun: rs.RunID}
			if rs.Conversation != nil {
				l.conversation = rs.Conversation.ConversationID
			}
			e.mu.Lock()
			e.logs = append(e.logs, l)
			e.mu.Unlock()
			return l
		},
	})
	return e
}

// runLog is the log of run ar1's own sandbox session (not a conversation's).
func (e *keepEnv) runLog() *memLog {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, l := range e.logs {
		if l.conversation == "" && l.agentRun == "ar1" {
			return l
		}
	}
	e.t.Fatal("no sandbox log for run ar1")
	return nil
}

// convLogs are the log sessions of conversation conv1, oldest first.
func (e *keepEnv) convLogs() []*memLog {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*memLog
	for _, l := range e.logs {
		if l.conversation == "conv1" {
			out = append(out, l)
		}
	}
	return out
}

const (
	testImage = "ubuntu:24.04"
	testRepo  = "https://example.com/o/r.git"
)

// openRun opens the sandbox of a flow agent run of node run nr1.
func (e *keepEnv) openRun(agentRun string) Extension {
	e.t.Helper()
	ext, err := e.prov(context.Background(), RunSpec{
		Kind: KindFlow, RunID: agentRun, Sandbox: "local",
		Info: func() RunInfo {
			return RunInfo{AgentRunID: agentRun, NodeRunID: "nr1", Input: json.RawMessage(`{"id":"7"}`)}
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if ext.Sandbox == nil || ext.CloseWith == nil {
		e.t.Fatalf("extension = %+v", ext)
	}
	return ext
}

// openConversation opens the sandbox lease of chat conv1, continued from agentRun (a run of node run nr1).
func (e *keepEnv) openConversation(agentRun string) Extension {
	e.t.Helper()
	ext, err := e.prov(context.Background(), RunSpec{
		Kind: KindAssist, RunID: "conv1", Sandbox: "local", Image: testImage,
		Info: func() RunInfo { return RunInfo{AssistSessionID: "conv1"} },
		Conversation: &ConversationSandbox{
			ConversationID: "conv1", FromAgentRunID: agentRun, NodeRunID: "nr1", Input: json.RawMessage(`{"id":"7"}`),
		},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if ext.BeforeTurn == nil || ext.AfterTurn == nil {
		e.t.Fatalf("a conversation's extension needs turn hooks: %+v", ext)
	}
	return ext
}

func (e *keepEnv) runDir(key string) string { return filepath.Join(e.root, "runs", key) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func waitGone(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for exists(p) {
		if time.Now().After(deadline) {
			t.Fatalf("%s still exists", p)
		}
		time.Sleep(time.Millisecond)
	}
}

// settle gives the Keeper's goroutine time to act on a clock advance that must not close anything.
func settle() { time.Sleep(30 * time.Millisecond) }

func TestRunSandboxIsKeptForKeepAlive(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "") // the default is 1m
	ext := e.openRun("ar1")
	if err := ext.Sandbox.WriteFile(ctx, "out.txt", []byte("done")); err != nil {
		t.Fatal(err)
	}
	dir := e.runDir("nr1")
	ext.CloseWith(true)

	if !exists(dir) {
		t.Fatal("the workspace was removed when the run ended")
	}
	if b, err := ext.Sandbox.ReadFile(ctx, "out.txt"); err != nil || string(b) != "done" {
		t.Fatalf("the sandbox is not usable after the run ended: %q %v", b, err)
	}
	lg := e.runLog()
	if !lg.has("Run ended; sandbox kept for 1m") || lg.has("Teardown") {
		t.Errorf("log after the run ended: %+v", lg.entries)
	}

	e.clock.Advance(59 * time.Second)
	settle()
	if !exists(dir) {
		t.Fatal("the sandbox was removed before keep_alive passed")
	}
	e.clock.Advance(2 * time.Second)
	waitGone(t, dir)
	deadline := time.Now().Add(5 * time.Second)
	for !lg.has("Teardown: run succeeded") || !lg.closed() {
		if time.Now().After(deadline) {
			t.Fatalf("no teardown in the log: %+v", lg.entries)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunSandboxWithZeroKeepAliveIsRemovedAtOnce(t *testing.T) {
	e := newKeepEnv(t, "0")
	ext := e.openRun("ar1")
	dir := e.runDir("nr1")
	if !exists(dir) {
		t.Fatal("no workspace")
	}
	ext.CloseWith(true)
	if exists(dir) {
		t.Fatal("keep_alive 0 must remove the workspace when the run ends")
	}
	if lg := e.runLog(); !lg.has("Teardown: run succeeded") || lg.has("kept for") {
		t.Errorf("log: %+v", lg.entries)
	}
}

// The propose and execute phases of one node run share a workspace: opening the second closes the held first.
func TestRunSandboxHeldFromAnEarlierPhaseIsClosedFirst(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "5m")
	propose := e.openRun("ar1")
	if err := propose.Sandbox.WriteFile(ctx, "plan.txt", []byte("plan")); err != nil {
		t.Fatal(err)
	}
	propose.CloseWith(false) // a parked proposal keeps its workspace
	if !e.keeper.Holding("ar1") {
		t.Fatal("the propose phase's sandbox is not held")
	}

	execute := e.openRun("ar2")
	if e.keeper.Holding("ar1") {
		t.Error("the held sandbox of the earlier phase was not closed")
	}
	if lg := e.runLog(); !lg.has("Teardown") || !lg.closed() {
		t.Errorf("the earlier phase's log has no teardown: %+v", lg.entries)
	}
	if b, err := execute.Sandbox.ReadFile(ctx, "plan.txt"); err != nil || string(b) != "plan" {
		t.Errorf("the execute phase does not see the propose phase's file: %q %v", b, err)
	}
	execute.CloseWith(true)
	e.keeper.CloseAll()
	if exists(e.runDir("nr1")) {
		t.Error("workspace kept after the node run succeeded")
	}
}

func TestConversationReattachesTheRunsSandbox(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "1m")
	run := e.openRun("ar1")
	if err := run.Sandbox.WriteFile(ctx, "work/result.txt", []byte("from the run")); err != nil {
		t.Fatal(err)
	}
	run.CloseWith(true)

	chat := e.openConversation("ar1")
	notices, err := chat.BeforeTurn(ctx)
	if err != nil || len(notices) != 0 {
		t.Fatalf("BeforeTurn = %v, %v", notices, err)
	}
	if b, err := chat.Sandbox.ReadFile(ctx, "work/result.txt"); err != nil || string(b) != "from the run" {
		t.Fatalf("the chat does not see the run's files: %q %v", b, err)
	}
	if e.keeper.Holding("ar1") {
		t.Error("the run's sandbox is still held for the run")
	}
	// The chat works in the same workdir; its own writes show up in it.
	if err := chat.Sandbox.WriteFile(ctx, "chat.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(e.runDir("nr1"), "chat.txt")) {
		t.Error("the chat did not write into the run's workdir")
	}

	logs := e.convLogs()
	if len(logs) != 1 || !logs[0].has("Reattached sandbox of run ar1") {
		t.Fatalf("conversation logs = %+v", logs)
	}
	if !logs[0].has("write chat.txt") {
		t.Errorf("the chat's operations are not logged on the conversation: %+v", logs[0].entries)
	}
	old := e.runLog()
	if !old.has("Sandbox handed over to conversation conv1") || !old.closed() {
		t.Errorf("the run's log was not ended with a hand-over: %+v", old.entries)
	}
	if old.has("chat.txt") {
		t.Error("the chat's operations leaked into the run's log")
	}
	if !exists(e.runDir("nr1")) {
		t.Error("the hand-over removed the workspace")
	}
}

func TestConversationKeepAliveRestartsAfterEveryTurn(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "1m")
	run := e.openRun("ar1")
	if err := run.Sandbox.WriteFile(ctx, "result.txt", []byte("r")); err != nil {
		t.Fatal(err)
	}
	run.CloseWith(true)
	dir := e.runDir("nr1")

	chat := e.openConversation("ar1")
	if _, err := chat.BeforeTurn(ctx); err != nil {
		t.Fatal(err)
	}
	// No timer runs during a turn, however long it takes.
	e.clock.Advance(10 * time.Minute)
	settle()
	if !exists(dir) {
		t.Fatal("the sandbox was removed during a turn")
	}

	chat.AfterTurn()
	e.clock.Advance(54 * time.Second) // 0.9 * keep_alive
	settle()
	if !exists(dir) {
		t.Fatal("the sandbox was removed before keep_alive passed")
	}
	// The next turn takes it back and the timer starts again when that turn ends.
	if notices, err := chat.BeforeTurn(ctx); err != nil || len(notices) != 0 {
		t.Fatalf("second BeforeTurn = %v, %v", notices, err)
	}
	chat.AfterTurn()
	e.clock.Advance(54 * time.Second) // 108s after the first AfterTurn, 54s after the last
	settle()
	if !exists(dir) {
		t.Fatal("the timer was not restarted by the last AfterTurn")
	}
	e.clock.Advance(7 * time.Second)
	waitGone(t, dir)
	if len(e.convLogs()) != 1 || !e.convLogs()[0].has("Teardown") {
		t.Errorf("the conversation's log has no teardown: %+v", e.convLogs()[0].entries)
	}
}

func TestConversationRecreatesTheSandboxAfterItExpired(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "1m")
	run := e.openRun("ar1")
	if err := run.Sandbox.WriteFile(ctx, "result.txt", []byte("r")); err != nil {
		t.Fatal(err)
	}
	run.CloseWith(true)

	chat := e.openConversation("ar1")
	if _, err := chat.BeforeTurn(ctx); err != nil {
		t.Fatal(err)
	}
	chat.AfterTurn()
	e.clock.Advance(2 * time.Minute)
	waitGone(t, e.runDir("nr1"))

	notices, err := chat.BeforeTurn(ctx)
	if err != nil || len(notices) != 1 {
		t.Fatalf("BeforeTurn = %v, %v", notices, err)
	}
	for _, want := range []string{"removed after 1m", testImage, "Changes made by the earlier run are gone"} {
		if !strings.Contains(notices[0], want) {
			t.Errorf("notice lacks %q: %s", want, notices[0])
		}
	}
	if _, err := chat.Sandbox.ReadFile(ctx, "result.txt"); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("the new sandbox has the earlier run's file: %v", err)
	}
	// input.json is written like in the original sandbox.
	if b, err := chat.Sandbox.ReadFile(ctx, "input.json"); err != nil || !strings.Contains(string(b), `"7"`) {
		t.Errorf("input.json = %q %v", b, err)
	}
	if err := chat.Sandbox.WriteFile(ctx, "again.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}

	logs := e.convLogs()
	if len(logs) != 2 {
		t.Fatalf("the conversation has %d log sessions, want the reattached one and the new one", len(logs))
	}
	if !logs[0].has("Teardown") || !logs[0].closed() {
		t.Errorf("first conversation log: %+v", logs[0].entries)
	}
	if !logs[1].has("removed after 1m") || !logs[1].has("write again.txt") {
		t.Errorf("second conversation log: %+v", logs[1].entries)
	}

	// Closing the chat closes the sandbox right away.
	chat.AfterTurn()
	chat.CloseWith(true)
	if e.keeper.Holding("conv1") {
		t.Error("the closed chat's sandbox is still held")
	}
	waitGone(t, e.runDir("conv1"))
}

func TestConversationWithoutAHeldSandboxCreatesOne(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "1m")
	chat := e.openConversation("gone-run")
	notices, err := chat.BeforeTurn(ctx)
	if err != nil || len(notices) != 1 || !strings.Contains(notices[0], "a new one was created") {
		t.Fatalf("BeforeTurn = %v, %v", notices, err)
	}
	if err := chat.Sandbox.WriteFile(ctx, "a.txt", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(e.runDir("conv1"), "a.txt")) {
		t.Error("the sandbox was not created under the conversation's key")
	}
	chat.CloseWith(true)
	if exists(e.runDir("conv1")) {
		t.Error("closing the conversation left the sandbox behind")
	}
	if _, err := chat.BeforeTurn(ctx); err == nil {
		t.Error("a closed conversation sandbox was recreated")
	}
}

// With keep_alive 0 the chat's sandbox is removed after each reply, like a run's at its end.
func TestConversationWithZeroKeepAlive(t *testing.T) {
	ctx := context.Background()
	e := newKeepEnv(t, "0")
	chat := e.openConversation("")
	if _, err := chat.BeforeTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(e.runDir("conv1")) {
		t.Fatal("no sandbox during the turn")
	}
	chat.AfterTurn()
	if exists(e.runDir("conv1")) {
		t.Fatal("keep_alive 0 must remove the sandbox when the reply ends")
	}
	notices, err := chat.BeforeTurn(ctx)
	if err != nil || len(notices) != 1 || !strings.Contains(notices[0], "keep_alive is 0") {
		t.Fatalf("BeforeTurn = %v, %v", notices, err)
	}
}
