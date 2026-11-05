package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/store"
)

// Retention prunes old runs, events and their blobs and worktrees, but never
// what a starred inbox item or a pending approval still needs; dedup keys live
// longer so pruned items do not re-fire.
func TestRetentionPrunesOldRunsButKeepsStarredAndPending(t *testing.T) {
	home := t.TempDir()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	st, err := store.Open(filepath.Join(home, "data", "hal.db"), store.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	e, err := New(Options{Store: st, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	big := strings.Repeat("x", 100<<10) // spills to a blob

	type fixture struct {
		run      *store.FlowRun
		worktree string
		blob     string
	}
	make1 := func(key string, status store.FlowRunStatus) fixture {
		ev := &store.Event{SensorID: "s", DedupKey: key, Payload: store.Inline(key + big)}
		if created, err := st.IngestEvent(ctx, ev); err != nil || !created {
			t.Fatalf("ingest %s: %v", key, err)
		}
		fr := &store.FlowRun{FlowID: "f", FlowHash: "h", FlowYAML: "id: f\n", EventID: ev.ID, Status: status}
		if err := st.CreateFlowRun(ctx, fr); err != nil {
			t.Fatal(err)
		}
		nr := &store.NodeRun{FlowRunID: fr.ID, NodeID: "n", NodeType: store.NodeAgent, Status: store.NodeDone}
		if err := st.CreateNodeRun(ctx, nr); err != nil {
			t.Fatal(err)
		}
		wt := filepath.Join(e.wt.Dir, nr.ID)
		if err := os.MkdirAll(wt, 0o750); err != nil {
			t.Fatal(err)
		}
		ar := &store.AgentRun{NodeRunID: nr.ID, Runtime: "r", Mode: store.ModeAuto, Policy: store.PolicyAuto, Phase: store.PhaseExecute, Status: store.AgentSucceeded, WorktreePath: wt}
		if err := st.CreateAgentRun(ctx, ar); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetEvent(ctx, ev.ID)
		if err != nil {
			t.Fatal(err)
		}
		return fixture{run: fr, worktree: wt, blob: got.Payload.Ref}
	}
	old := make1("old", store.FlowRunSucceeded)
	starred := make1("starred", store.FlowRunSucceeded)
	pending := make1("pending", store.FlowRunSucceeded)
	for _, f := range []fixture{old, starred, pending} {
		if f.blob == "" {
			t.Fatal("test payload did not spill to a blob")
		}
	}
	// pending: an agent run with an undecided proposal.
	nodes, _ := st.ListNodeRuns(ctx, store.NodeRunFilter{FlowRunID: pending.run.ID})
	ars, _ := st.ListAgentRuns(ctx, store.AgentRunFilter{NodeRunID: nodes.Items[0].ID})
	if err := st.CreateApproval(ctx, &store.Approval{AgentRunID: ars.Items[0].ID, Type: store.ApprovalProposal, Proposal: store.Inline("plan")}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInboxItem(ctx, &store.InboxItem{SourceType: store.SourceNodeRun, NodeRunID: nodeIDOf(t, st, starred.run.ID), Title: "keep me", Starred: true}); err != nil {
		t.Fatal(err)
	}

	res, err := e.pruneAt(ctx, base.AddDate(0, 0, 31))
	if err != nil {
		t.Fatal(err)
	}
	if res.FlowRuns != 1 || res.Events != 1 {
		t.Fatalf("prune result: %+v", res)
	}
	if _, err := st.GetFlowRun(ctx, old.run.ID); err == nil {
		t.Error("old flow run survived")
	}
	for name, f := range map[string]fixture{"starred": starred, "pending": pending} {
		if _, err := st.GetFlowRun(ctx, f.run.ID); err != nil {
			t.Errorf("%s flow run was pruned: %v", name, err)
		}
		if _, err := os.Stat(f.worktree); err != nil {
			t.Errorf("%s worktree was removed: %v", name, err)
		}
		if _, err := st.ReadContent(store.Content{Ref: f.blob}); err != nil {
			t.Errorf("%s blob was removed: %v", name, err)
		}
	}
	if _, err := os.Stat(old.worktree); err == nil {
		t.Error("worktree of the pruned run was not removed")
	}
	if _, err := st.ReadContent(store.Content{Ref: old.blob}); err == nil {
		t.Error("blob of the pruned run was not removed")
	}
	// The dedup key outlives the event: the item does not fire again.
	if created, err := st.ClaimEvent(ctx, "s", "old"); err != nil || created {
		t.Errorf("pruned item's dedup key was forgotten: created=%v err=%v", created, err)
	}
}

func nodeIDOf(t *testing.T, st *store.Store, flowRunID string) string {
	t.Helper()
	page, err := st.ListNodeRuns(t.Context(), store.NodeRunFilter{FlowRunID: flowRunID})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("node runs of %s: %v", flowRunID, err)
	}
	return page.Items[0].ID
}
