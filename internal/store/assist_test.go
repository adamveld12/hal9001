package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A database written by the previous schema (v2) keeps its flow-run agent
// runs, with their transcript, permission requests and approvals, when v3
// rebuilds agent_runs, and the foreign keys keep working afterwards.
func TestMigrationToAssistKeepsAgentRunsAndTheirChildren(t *testing.T) {
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
		if m.version > 2 {
			continue
		}
		exec(m.sql)
		exec(`INSERT INTO schema_migrations VALUES (?, ?, ?)`, m.version, m.name, "2026-11-05T12:00:00.000000000Z")
	}
	const now = "2026-11-05T12:00:00.000000000Z"
	exec(`INSERT INTO flow_versions VALUES ('F', 'h1', 'id: F', ?)`, now)
	exec(`INSERT INTO flow_runs (id, flow_id, flow_hash, kind, status, created_at) VALUES ('FR', 'F', 'h1', 'live', 'succeeded', ?)`, now)
	exec(`INSERT INTO node_runs (id, flow_run_id, node_id, node_type, status, queued_at) VALUES ('NR', 'FR', 'agent', 'agent', 'done', ?)`, now)
	exec(`INSERT INTO agent_runs (id, node_run_id, runtime, mode, approve_style, policy, phase, model, status, prompt_text, created_at)
		VALUES ('AR', 'NR', 'claude', 'approve', 'per_tool', 'gated', 'execute', 'sonnet', 'succeeded', 'the prompt', ?)`, now)
	exec(`INSERT INTO agent_events (agent_run_id, seq, ts, kind, payload_text) VALUES ('AR', 1, ?, 'message', '{}')`, now)
	exec(`INSERT INTO permission_requests (id, agent_run_id, tool_kind, title, requested_at) VALUES ('PR', 'AR', 'edit', 'Edit', ?)`, now)
	exec(`INSERT INTO approvals (id, agent_run_id, type, permission_request_id, status, created_at) VALUES ('AP', 'AR', 'tool_call', 'PR', 'pending', ?)`, now)
	must(t, raw.Close())

	s, err := Open(path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })

	v, err := s.SchemaVersion(ctx())
	must(t, err)
	if v < 3 {
		t.Fatalf("schema version = %d, want at least 3", v)
	}
	ar, err := s.GetAgentRun(ctx(), "AR")
	must(t, err)
	if ar.NodeRunID != "NR" || ar.AssistSessionID != "" || ar.Model != "sonnet" || ar.Prompt.Text != "the prompt" || ar.ApproveStyle != StylePerTool {
		t.Errorf("agent run after migration = %+v", ar)
	}
	evs, err := s.ListAgentEvents(ctx(), AgentEventFilter{AgentRunID: "AR"})
	must(t, err)
	if len(evs) != 1 {
		t.Errorf("agent events after migration = %d, want 1", len(evs))
	}
	if ap, err := s.GetApproval(ctx(), "AP"); err != nil || ap.PermissionRequestID != "PR" || ap.Status != ApprovalPending {
		t.Errorf("approval after migration = %+v, %v", ap, err)
	}

	var fk int
	must(t, s.wdb.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	if fk != 1 {
		t.Errorf("foreign_keys = %d after migrations, want 1", fk)
	}
	// The children still reference the rebuilt table: deleting the flow run cascades through it.
	if _, err := s.wdb.Exec(`DELETE FROM flow_runs WHERE id = 'FR'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_runs", "agent_events", "permission_requests", "approvals"} {
		var n int
		must(t, s.rdb.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n)) //nolint:gosec // constant table names
		if n != 0 {
			t.Errorf("%s has %d rows after the flow run was deleted, want 0 (cascade)", table, n)
		}
	}
}

func mkAssistSession(t *testing.T, s *Store, target string) *AssistSession {
	t.Helper()
	a := &AssistSession{
		TargetKind: AssistTargetSensor, TargetID: target, Runtime: "claude", Model: "sonnet",
		DraftYAML: "id: x\n", DraftHash: "h0", DraftUpdatedBy: DraftByUser,
	}
	must(t, s.CreateAssistSession(ctx(), a))
	return a
}

func mkAssistRun(t *testing.T, s *Store, sessionID string, status AgentStatus) *AgentRun {
	t.Helper()
	a := &AgentRun{AssistSessionID: sessionID, Runtime: "claude", Mode: ModeApprove, ApproveStyle: StylePerTool, Policy: PolicyGated, Phase: PhaseExecute, Status: status}
	must(t, s.CreateAgentRun(ctx(), a))
	return a
}

func TestAgentRunBelongsToExactlyOneOwner(t *testing.T) {
	s, _ := newTestStore(t)
	sess := mkAssistSession(t, s, "")
	base := AgentRun{Runtime: "claude", Mode: ModeApprove, ApproveStyle: StylePerTool, Policy: PolicyGated, Phase: PhaseExecute}

	none := base
	if err := s.CreateAgentRun(ctx(), &none); !errors.Is(err, ErrInvalid) {
		t.Errorf("run with neither a node run nor a session: %v, want ErrInvalid", err)
	}
	both := base
	both.NodeRunID, both.AssistSessionID = "NR", sess.ID
	if err := s.CreateAgentRun(ctx(), &both); !errors.Is(err, ErrInvalid) {
		t.Errorf("run with both a node run and a session: %v, want ErrInvalid", err)
	}

	turn := mkAssistRun(t, s, sess.ID, AgentRunning)
	got, err := s.GetAgentRun(ctx(), turn.ID)
	must(t, err)
	if got.AssistSessionID != sess.ID || got.NodeRunID != "" {
		t.Errorf("assist turn = %+v", got)
	}
	page, err := s.ListAgentRuns(ctx(), AgentRunFilter{AssistSessionID: sess.ID})
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != turn.ID {
		t.Errorf("runs of the session = %+v", page.Items)
	}
}

func TestAssistSessionAndMessages(t *testing.T) {
	s, clk := newTestStore(t)
	var changes []Change
	s.SetNotifier(func(c Change) { changes = append(changes, c) })

	a := mkAssistSession(t, s, "")
	b := mkAssistSession(t, s, "github-notifications")
	clk.Advance(time.Second)
	c := mkAssistSession(t, s, "github-notifications")

	list, err := s.ListAssistSessions(ctx(), AssistSessionFilter{TargetKind: AssistTargetSensor, TargetID: "github-notifications"})
	must(t, err)
	if len(list.Items) != 2 || list.Items[0].ID != c.ID || list.Items[1].ID != b.ID {
		t.Errorf("sessions of one target, newest first = %v", assistIDs(list.Items))
	}

	a.Status, a.DraftYAML, a.DraftHash, a.DraftUpdatedBy, a.ACPSessionID = AssistRunning, "id: y\n", "h1", DraftByAgent, "acp-1"
	a.ApprovedScript, a.ApprovedScriptHash = "lang: bash\nscript:\necho hi\n", "sh1"
	a.DraftDiagnostics = []byte(`[{"severity":"error","code":"required","message":"m"}]`)
	must(t, s.UpdateAssistSession(ctx(), a))
	got, err := s.GetAssistSession(ctx(), a.ID)
	must(t, err)
	if got.Status != AssistRunning || got.DraftYAML != "id: y\n" || got.DraftUpdatedBy != DraftByAgent || got.ACPSessionID != "acp-1" || got.ApprovedScript != a.ApprovedScript || got.ApprovedScriptHash != "sh1" || string(got.DraftDiagnostics) != string(a.DraftDiagnostics) {
		t.Errorf("session after update = %+v", got)
	}

	closed := mkAssistSession(t, s, "")
	closed.Status = AssistClosed
	must(t, s.UpdateAssistSession(ctx(), closed))
	open, err := s.ListAssistSessions(ctx(), AssistSessionFilter{})
	must(t, err)
	for _, it := range open.Items {
		if it.ID == closed.ID {
			t.Errorf("a closed session is listed by default")
		}
	}
	all, err := s.ListAssistSessions(ctx(), AssistSessionFilter{IncludeClosed: true})
	must(t, err)
	if len(all.Items) != len(open.Items)+1 {
		t.Errorf("IncludeClosed lists %d, default lists %d", len(all.Items), len(open.Items))
	}

	text := "hello"
	user := &AssistMessage{SessionID: a.ID, Role: "user", Text: &text}
	tool := &AssistMessage{SessionID: a.ID, Role: "tool", ToolCallID: "t1", AgentRunID: "AR", ToolCall: []byte(`{"title":"Edit","status":"pending"}`)}
	must(t, s.CreateAssistMessage(ctx(), user))
	must(t, s.CreateAssistMessage(ctx(), tool))
	tool.ToolCall, tool.ApprovalID = []byte(`{"title":"Edit","status":"completed"}`), "AP"
	must(t, s.UpdateAssistMessage(ctx(), tool))
	msgs, err := s.ListAssistMessages(ctx(), a.ID)
	must(t, err)
	if len(msgs) != 2 || msgs[0].ID != user.ID || msgs[1].ID != tool.ID {
		t.Fatalf("messages = %+v", msgs)
	}
	if msgs[0].Text == nil || *msgs[0].Text != "hello" || msgs[0].ToolCall != nil {
		t.Errorf("text message = %+v", msgs[0])
	}
	if msgs[1].Text != nil || string(msgs[1].ToolCall) != `{"title":"Edit","status":"completed"}` || msgs[1].ApprovalID != "AP" {
		t.Errorf("tool message = %+v", msgs[1])
	}
	if err := s.CreateAssistMessage(ctx(), &AssistMessage{SessionID: a.ID, Role: "robot"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad role: %v, want ErrInvalid", err)
	}

	var sawSession, sawMessage bool
	for _, ch := range changes {
		sawSession = sawSession || (ch.Resource == ResAssistSession && ch.ID == a.ID && ch.Op == OpUpdate)
		sawMessage = sawMessage || (ch.Resource == ResAssistMessage && ch.ID == tool.ID && ch.Op == OpUpdate)
	}
	if !sawSession || !sawMessage {
		t.Errorf("store did not announce the updates: %+v", changes)
	}
}

func assistIDs(items []AssistSession) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestMarkInterruptedFailsRunningAssistSessions(t *testing.T) {
	s, _ := newTestStore(t)
	running := mkAssistSession(t, s, "")
	waiting := mkAssistSession(t, s, "")
	idle := mkAssistSession(t, s, "")
	for _, a := range []*AssistSession{running, waiting} {
		a.Status = AssistRunning
	}
	waiting.Status = AssistAwaitingApproval
	must(t, s.UpdateAssistSession(ctx(), running))
	must(t, s.UpdateAssistSession(ctx(), waiting))

	turn := mkAssistRun(t, s, waiting.ID, AgentAwaitingApproval)
	pr := &PermissionRequest{AgentRunID: turn.ID, ToolKind: "execute", Title: "curl"}
	must(t, s.CreatePermissionRequest(ctx(), pr))
	ap := &Approval{AgentRunID: turn.ID, Type: ApprovalToolCall, PermissionRequestID: pr.ID}
	must(t, s.CreateApproval(ctx(), ap))

	res, err := s.MarkInterrupted(ctx())
	must(t, err)
	if res.AssistSessions != 2 || res.AgentRuns != 1 || res.ExpiredApprovals != 1 {
		t.Errorf("result = %+v", res)
	}
	for _, a := range []*AssistSession{running, waiting} {
		got, err := s.GetAssistSession(ctx(), a.ID)
		must(t, err)
		if got.Status != AssistFailed || got.Error == "" {
			t.Errorf("session %s after restart: status %q error %q, want failed with a reason", a.ID, got.Status, got.Error)
		}
	}
	if got, _ := s.GetAssistSession(ctx(), idle.ID); got.Status != AssistIdle {
		t.Errorf("idle session after restart = %q", got.Status)
	}
	if got, _ := s.GetAgentRun(ctx(), turn.ID); got.Status != AgentInterrupted {
		t.Errorf("turn after restart = %q", got.Status)
	}
}

func TestPruneDeletesOldAssistSessionsWithTheirTurns(t *testing.T) {
	s, clk := newTestStore(t)
	old := mkAssistSession(t, s, "")
	oldTurn := mkAssistRun(t, s, old.ID, AgentSucceeded)
	big := bigText(SpillThreshold + 5)
	oldTurn.Result = Inline(big)
	must(t, s.UpdateAgentRun(ctx(), oldTurn))
	must(t, s.AppendAgentEvent(ctx(), &AgentEvent{AgentRunID: oldTurn.ID, Kind: "message", Payload: Inline("{}")}))
	must(t, s.CreateAssistMessage(ctx(), &AssistMessage{SessionID: old.ID, Role: "system"}))

	pending := mkAssistSession(t, s, "")
	pendingTurn := mkAssistRun(t, s, pending.ID, AgentAwaitingApproval)
	pr := &PermissionRequest{AgentRunID: pendingTurn.ID, ToolKind: "execute", Title: "curl"}
	must(t, s.CreatePermissionRequest(ctx(), pr))
	must(t, s.CreateApproval(ctx(), &Approval{AgentRunID: pendingTurn.ID, Type: ApprovalToolCall, PermissionRequestID: pr.ID}))

	running := mkAssistSession(t, s, "")
	running.Status = AssistRunning
	must(t, s.UpdateAssistSession(ctx(), running))

	clk.Advance(40 * 24 * time.Hour)
	fresh := mkAssistSession(t, s, "")

	res, err := s.Prune(ctx(), clk.Now(), 30, 180)
	must(t, err)
	if res.AssistSessions != 1 || len(res.AssistSessionIDs) != 1 || res.AssistSessionIDs[0] != old.ID {
		t.Errorf("result = %+v, want only the old idle session pruned", res)
	}
	if _, err := s.GetAssistSession(ctx(), old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old session survived: %v", err)
	}
	if _, err := s.GetAgentRun(ctx(), oldTurn.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old turn survived: %v", err)
	}
	if msgs, _ := s.ListAssistMessages(ctx(), old.ID); len(msgs) != 0 {
		t.Errorf("old messages survived: %d", len(msgs))
	}
	if len(res.OrphanBlobs) != 1 {
		t.Errorf("orphan blobs = %v, want the old turn's result blob", res.OrphanBlobs)
	}
	for name, a := range map[string]*AssistSession{"pending": pending, "running": running, "fresh": fresh} {
		if _, err := s.GetAssistSession(ctx(), a.ID); err != nil {
			t.Errorf("%s session was pruned: %v", name, err)
		}
	}
}
