package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// A database from before the conversations schema keeps its chats and messages: they become ordinary entries
// (kind "", not a fork).
func TestMigrationToConversationsKeepsChatsAndMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hal.db")
	raw, err := sql.Open("sqlite", dsn(path, true))
	must(t, err)
	migs, err := loadMigrations()
	must(t, err)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	for _, m := range migs {
		if m.version >= 17 {
			continue
		}
		exec(m.sql)
		exec(`INSERT INTO schema_migrations VALUES (?, ?, ?)`, m.version, m.name, "2026-11-05T12:00:00.000000000Z")
	}
	const now = "2026-11-05T12:00:00.000000000Z"
	exec(`INSERT INTO assist_sessions (id, target_kind, runtime, status, draft_hash, draft_updated_at, draft_updated_by, created_at, updated_at, title)
		VALUES ('S1', 'general', 'claude', 'idle', 'h', ?, 'user', ?, ?, 'hello')`, now, now, now)
	exec(`INSERT INTO assist_messages (id, session_id, role, text, created_at) VALUES ('M1', 'S1', 'user', 'hello', ?)`, now)
	must(t, raw.Close())

	s, err := Open(path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.GetAssistSession(ctx(), "S1")
	must(t, err)
	if a.ForkedFromAgentRunID != "" || a.ForkContext != "" || a.Title != "hello" {
		t.Errorf("session after migration = %+v", a)
	}
	msgs, err := s.ListAssistMessages(ctx(), "S1")
	must(t, err)
	if len(msgs) != 1 || msgs[0].Kind != "" {
		t.Errorf("messages after migration = %+v", msgs)
	}
	page, err := s.ListConversations(ctx(), ConversationFilter{})
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != "S1" || page.Items[0].Source != ConversationChat || page.Items[0].Status != "idle" {
		t.Errorf("conversations after migration = %+v", page.Items)
	}
}

func TestForkFieldsAndMessageKindsAreValidatedAndStored(t *testing.T) {
	s, _ := newTestStore(t)
	fork := &AssistSession{TargetKind: AssistTargetGeneral, Runtime: "claude", DraftHash: "h", DraftUpdatedBy: DraftByUser, ForkedFromAgentRunID: "RUN1"}
	must(t, s.CreateAssistSession(ctx(), fork))

	bad := *fork
	bad.ID, bad.ForkContext = "", "guessed"
	if err := s.CreateAssistSession(ctx(), &bad); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown fork context: %v, want ErrInvalid", err)
	}
	orphan := &AssistSession{TargetKind: AssistTargetGeneral, Runtime: "claude", DraftHash: "h", DraftUpdatedBy: DraftByUser, ForkContext: ForkReplayed}
	if err := s.CreateAssistSession(ctx(), orphan); !errors.Is(err, ErrInvalid) {
		t.Errorf("a fork context on a chat that is not a fork: %v, want ErrInvalid", err)
	}

	fork.ForkContext = ForkResumed
	must(t, s.UpdateAssistSession(ctx(), fork))
	got, err := s.GetAssistSession(ctx(), fork.ID)
	must(t, err)
	if got.ForkedFromAgentRunID != "RUN1" || got.ForkContext != ForkResumed {
		t.Errorf("fork after update = %+v", got)
	}

	text := "Continued from run RUN1"
	div := &AssistMessage{SessionID: fork.ID, Role: "system", Kind: MessageDivider, Text: &text}
	must(t, s.CreateAssistMessage(ctx(), div))
	text2 := "Continued from run RUN1; session resumed"
	div.Text = &text2
	must(t, s.UpdateAssistMessage(ctx(), div))
	m, err := s.GetAssistMessage(ctx(), div.ID)
	must(t, err)
	if m.Kind != MessageDivider || m.Text == nil || *m.Text != text2 {
		t.Errorf("divider = %+v", m)
	}
	if err := s.CreateAssistMessage(ctx(), &AssistMessage{SessionID: fork.ID, Role: "system", Kind: "banner", Text: &text}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unknown message kind: %v, want ErrInvalid", err)
	}
}

func TestAgentRunChainFollowsPhasesBothWays(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "demo", "", FlowRunSucceeded)
	node := mkNodeRun(t, s, fr.ID, "review", NodeAgent, NodeDone)
	mk := func(prev string) *AgentRun {
		a := &AgentRun{NodeRunID: node.ID, PrevAgentRunID: prev, Runtime: "claude", Mode: ModeAuto, Policy: PolicyAuto, Phase: PhaseExecute, Status: AgentSucceeded}
		must(t, s.CreateAgentRun(ctx(), a))
		return a
	}
	p1 := mk("")
	p2 := mk(p1.ID)
	other := mk("")
	for _, id := range []string{p1.ID, p2.ID} {
		chain, err := s.AgentRunChain(ctx(), id)
		must(t, err)
		if len(chain) != 2 || chain[0] != p1.ID || chain[1] != p2.ID {
			t.Errorf("chain of %s = %v, want [%s %s]", id, chain, p1.ID, p2.ID)
		}
	}
	if chain, err := s.AgentRunChain(ctx(), other.ID); err != nil || len(chain) != 1 || chain[0] != other.ID {
		t.Errorf("chain of a single run = %v, %v", chain, err)
	}
	if _, err := s.AgentRunChain(ctx(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown run: %v, want ErrNotFound", err)
	}
	// Only the last phase is a conversation.
	page, err := s.ListConversations(ctx(), ConversationFilter{Source: ConversationFlow})
	must(t, err)
	var ids []string
	for _, c := range page.Items {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || !contains(ids, p2.ID) || !contains(ids, other.ID) {
		t.Errorf("flow conversations = %v, want %s and %s", ids, p2.ID, other.ID)
	}
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
