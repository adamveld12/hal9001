package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// A continuation run is stored with its link and its seed node runs in one step; it is listed by original run,
// by conversation and by kind, and it is not a test run, so it counts like a live one.
func TestContinuationRunIsStoredWithItsSeedAndLink(t *testing.T) {
	s, _ := newTestStore(t)
	orig := mkFlowRun(t, s, "f", "", FlowRunSucceeded)
	if err := s.CreateFlowRun(ctx(), &FlowRun{FlowID: "f", FlowHash: "h1", Kind: KindContinuation}); !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateFlowRun of a continuation run: %v, want ErrInvalid", err)
	}
	if err := s.CreateContinuationRun(ctx(), &FlowRun{FlowID: "f", FlowHash: "h1"}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("continuation run without a link: %v, want ErrInvalid", err)
	}

	fr := &FlowRun{
		FlowID: "f", FlowHash: "h1", Continuation: &Continuation{OfRunID: orig.ID, ConversationID: "chat1", NodeID: "work", Port: "ship", ApprovalID: "ap1"},
	}
	parent := &NodeRun{ID: s.NewID(), NodeID: "prep", NodeType: NodeCommand, Status: NodeDone, Decision: DecisionEmitted, Stdout: Inline("{}")}
	child := &NodeRun{NodeID: "work", NodeType: NodeAgent, ParentNodeRunID: parent.ID, Status: NodeDone, Decision: DecisionEmitted}
	must(t, s.CreateContinuationRun(ctx(), fr, []*NodeRun{parent, child}))

	got, err := s.GetFlowRun(ctx(), fr.ID)
	must(t, err)
	if got.Kind != KindContinuation || got.Status != FlowRunQueued || got.Continuation == nil || *got.Continuation != *fr.Continuation {
		t.Fatalf("stored run: %+v", got)
	}
	if live, _ := s.GetFlowRun(ctx(), orig.ID); live.Continuation != nil {
		t.Errorf("a live run has a continuation link: %+v", live.Continuation)
	}
	nodes, err := s.ListNodeRuns(ctx(), NodeRunFilter{FlowRunID: fr.ID, ListOpts: ListOpts{Oldest: true}})
	must(t, err)
	if len(nodes.Items) != 2 || nodes.Items[0].ID != parent.ID || nodes.Items[1].ParentNodeRunID != parent.ID {
		t.Errorf("seed node runs: %+v", nodes.Items)
	}

	other := mkFlowRun(t, s, "f", "", FlowRunQueued)
	for _, tc := range []struct {
		name string
		f    FlowRunFilter
		want []string
	}{
		{"default", FlowRunFilter{}, []string{other.ID, fr.ID, orig.ID}},
		{"kind", FlowRunFilter{Kinds: []FlowRunKind{KindContinuation}}, []string{fr.ID}},
		{"of", FlowRunFilter{ContinuationOf: orig.ID}, []string{fr.ID}},
		{"of nothing", FlowRunFilter{ContinuationOf: other.ID}, nil},
		{"conversation", FlowRunFilter{Conversation: "chat1"}, []string{fr.ID}},
		{"other conversation", FlowRunFilter{Conversation: "chat2"}, nil},
	} {
		tc.f.Limit = 10
		page, err := s.ListFlowRuns(ctx(), tc.f)
		must(t, err)
		var ids []string
		for _, r := range page.Items {
			ids = append(ids, r.ID)
		}
		if len(ids) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, ids, tc.want)
			continue
		}
		for i := range ids {
			if ids[i] != tc.want[i] {
				t.Errorf("%s: got %v, want %v", tc.name, ids, tc.want)
			}
		}
	}
}

// A database written before the continuation and conversation schemas (v16) keeps its flow runs (v18 rebuilds the table
// for the new kind), their node runs, replay links, agent runs and sandbox logs. The foreign keys still work afterwards,
// and a continuation run can be stored next to the old runs.
func TestMigrationFromV16KeepsFlowRunsAndTheirChildren(t *testing.T) {
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
		if m.version > 16 {
			continue
		}
		exec(m.sql)
		exec(`INSERT INTO schema_migrations VALUES (?, ?, ?)`, m.version, m.name, "2026-11-05T12:00:00.000000000Z")
	}
	const now = "2026-11-05T12:00:00.000000000Z"
	exec(`INSERT INTO flow_versions VALUES ('F', 'h1', 'id: F', ?)`, now)
	exec(`INSERT INTO flow_runs (id, flow_id, flow_hash, kind, status, created_at) VALUES ('FR1', 'F', 'h1', 'live', 'succeeded', ?)`, now)
	exec(`INSERT INTO flow_runs (id, flow_id, flow_hash, kind, replay_of, status, created_at, cancelled_by) VALUES ('FR2', 'F', 'h1', 'replay', 'FR1', 'cancelled', ?, 'someone')`, now)
	exec(`INSERT INTO node_runs (id, flow_run_id, node_id, node_type, status, queued_at) VALUES ('NR1', 'FR1', 'agent', 'agent', 'done', ?)`, now)
	exec(`INSERT INTO agent_runs (id, node_run_id, runtime, mode, approve_style, policy, phase, model, status, prompt_text, created_at)
		VALUES ('AR1', 'NR1', 'claude', 'approve', 'per_tool', 'gated', 'execute', 'sonnet', 'succeeded', 'the prompt', ?)`, now)
	exec(`INSERT INTO sandbox_sessions (id, node_run_id, agent_run_id, backend, status, started_at) VALUES ('SS1', 'NR1', 'AR1', 'local', 'closed', ?)`, now)
	exec(`INSERT INTO sandbox_log_entries (session_id, ts, kind, level, title) VALUES ('SS1', ?, 'lifecycle', 'info', 'Sandbox ready')`, now)
	must(t, raw.Close())

	s, err := Open(path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	if v, err := s.SchemaVersion(ctx()); err != nil || v < 19 {
		t.Fatalf("schema version = %d, %v; want at least 19", v, err)
	}

	fr1, err := s.GetFlowRun(ctx(), "FR1")
	must(t, err)
	fr2, err := s.GetFlowRun(ctx(), "FR2")
	must(t, err)
	if fr1.Kind != KindLive || fr1.Continuation != nil || fr1.Status != FlowRunSucceeded || fr2.Kind != KindReplay || fr2.ReplayOf != "FR1" || fr2.Status != FlowRunCancelled || fr2.CancelledBy != "someone" {
		t.Errorf("flow runs after migration: %+v %+v", fr1, fr2)
	}
	nodes, err := s.ListNodeRuns(ctx(), NodeRunFilter{FlowRunID: "FR1", ListOpts: ListOpts{Limit: 10}})
	must(t, err)
	if len(nodes.Items) != 1 || nodes.Items[0].ID != "NR1" {
		t.Errorf("node runs after migration: %+v", nodes.Items)
	}
	if ar, err := s.GetAgentRun(ctx(), "AR1"); err != nil || ar.NodeRunID != "NR1" {
		t.Errorf("agent run after migration: %+v %v", ar, err)
	}
	sessions, err := s.ListSandboxSessions(ctx(), SandboxFilter{AgentRunID: "AR1"})
	must(t, err)
	if len(sessions) != 1 || sessions[0].ID != "SS1" || sessions[0].AssistSessionID != "" {
		t.Errorf("sandbox sessions after migration: %+v", sessions)
	}
	violations := func() (n int) {
		rows, err := s.rdb.Query(`PRAGMA foreign_key_check`)
		must(t, err)
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			n++
		}
		return n
	}()
	if violations != 0 {
		t.Errorf("foreign_key_check reports %d violations", violations)
	}

	// A continuation run is stored next to the old runs and lists by its link.
	cont := &FlowRun{FlowID: "F", FlowHash: "h1", Continuation: &Continuation{OfRunID: "FR1", ConversationID: "chat1", NodeID: "agent", Port: "out"}}
	must(t, s.CreateContinuationRun(ctx(), cont, []*NodeRun{{NodeID: "agent", NodeType: NodeAgent, Status: NodeDone, Decision: DecisionEmitted}}))
	page, err := s.ListFlowRuns(ctx(), FlowRunFilter{ContinuationOf: "FR1", ListOpts: ListOpts{Limit: 10}})
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != cont.ID {
		t.Errorf("continuation runs of FR1: %+v", page.Items)
	}

	// The children reference the rebuilt table: pruning FR1 removes its node runs, agent runs and sandbox log, and
	// detaches the replay.
	if _, err := s.wdb.Exec(`DELETE FROM flow_runs WHERE id = 'FR1'`); err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{"node_runs WHERE id = 'NR1'": 0, "agent_runs WHERE id = 'AR1'": 0, "sandbox_sessions WHERE id = 'SS1'": 0, "sandbox_log_entries WHERE session_id = 'SS1'": 0} {
		var n int
		must(t, s.rdb.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&n))
		if n != want {
			t.Errorf("rows of %s after pruning the run = %d, want %d", table, n, want)
		}
	}
	if fr2, err := s.GetFlowRun(ctx(), "FR2"); err != nil || fr2.ReplayOf != "" {
		t.Errorf("replay after pruning its original: %+v %v", fr2, err)
	}
}
