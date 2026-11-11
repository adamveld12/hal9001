package engine

import (
	"path/filepath"
	"testing"

	"hal9001/internal/sandbox"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// The sandbox of a chat continued from an agent run is logged on the chat: the session carries the assist session id
// and the original node run (so it is pruned with it), and it stays out of the run's own sandbox log.
func TestSandboxRunLogRecordsConversationSessions(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "data", "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e, err := New(Options{Store: st, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	fr := &store.FlowRun{FlowID: "f", FlowHash: "h", FlowYAML: "id: f\n", Status: store.FlowRunSucceeded}
	if err := st.CreateFlowRun(ctx, fr); err != nil {
		t.Fatal(err)
	}
	nr := &store.NodeRun{FlowRunID: fr.ID, NodeID: "n", NodeType: store.NodeAgent, Status: store.NodeDone}
	if err := st.CreateNodeRun(ctx, nr); err != nil {
		t.Fatal(err)
	}

	// A run's sandbox, as before.
	runLog := e.SandboxRunLog(ctx, tools.RunSpec{RunID: "run-1", Sandbox: "local"}, tools.RunInfo{NodeRunID: nr.ID, AgentRunID: "run-1"})
	if runLog == nil {
		t.Fatal("no log for the run's sandbox")
	}
	// A chat's sandbox: an assist turn has no node run of its own, so the log gets it from the conversation.
	chatSpec := tools.RunSpec{
		RunID: "chat-1", Sandbox: "local", Image: "ubuntu:24.04", Repo: "https://x:secret@example.com/o/r.git", Ref: "main",
		Conversation: &tools.ConversationSandbox{ConversationID: "chat-1", FromAgentRunID: "run-1", NodeRunID: nr.ID},
	}
	chatLog := e.SandboxRunLog(ctx, chatSpec, tools.RunInfo{AssistSessionID: "chat-1"})
	if chatLog == nil {
		t.Fatal("no log for the conversation's sandbox")
	}
	chatLog.Update(sandbox.SessionUpdate{Status: sandbox.StatusReady, Info: sandbox.SessionInfo{Workdir: "/w"}})
	chatLog.Add(sandbox.LogEntry{Kind: sandbox.KindLifecycle, Title: "Reattached sandbox of run run-1"})

	chat, err := st.ListSandboxSessions(ctx, store.SandboxFilter{AssistSessionID: "chat-1"})
	if err != nil || len(chat) != 1 {
		t.Fatalf("conversation sessions = %+v, %v", chat, err)
	}
	s := chat[0]
	if s.NodeRunID != nr.ID || s.AssistSessionID != "chat-1" || s.AgentRunID != "chat-1" || s.Status != store.SandboxReady || s.Workdir != "/w" || s.EntryCount != 1 {
		t.Errorf("conversation session = %+v", s)
	}
	if s.Repo != "https://example.com/o/r.git" {
		t.Errorf("repo credentials reached the log: %q", s.Repo)
	}

	byNode, err := st.ListSandboxSessions(ctx, store.SandboxFilter{NodeRunID: nr.ID})
	if err != nil || len(byNode) != 1 || byNode[0].AssistSessionID != "" {
		t.Errorf("node run sessions = %+v, %v", byNode, err)
	}
	entries, _, err := st.ListSandboxLogEntries(ctx, store.SandboxEntryFilter{SandboxFilter: store.SandboxFilter{AssistSessionID: "chat-1"}})
	if err != nil || len(entries) != 1 || entries[0].Title != "Reattached sandbox of run run-1" {
		t.Errorf("conversation entries = %+v, %v", entries, err)
	}
	if entries, _, _ := st.ListSandboxLogEntries(ctx, store.SandboxEntryFilter{SandboxFilter: store.SandboxFilter{NodeRunID: nr.ID}}); len(entries) != 0 {
		t.Errorf("the run's log has the chat's entries: %+v", entries)
	}

	// A conversation without a node run cannot be recorded (the row needs one): no log, not a crash.
	bad := chatSpec
	bad.Conversation = &tools.ConversationSandbox{ConversationID: "chat-2"}
	if l := e.SandboxRunLog(ctx, bad, tools.RunInfo{}); l != nil {
		t.Error("a log was opened for a conversation without a node run")
	}
}
