package store

import (
	"errors"
	"testing"
)

// The engine decides a flow run is over from its node runs, so no commit may
// leave a finished node run without the node runs it feeds: a run that looked
// finished in between ended before its last inbox item existed.
func TestFinishNodeRunCommitsTheFollowUpsWithIt(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	parent := mkNodeRun(t, s, fr.ID, "split", NodeCommand, NodeRunning)

	// The notifier runs right after the commit, before the committing
	// goroutine can write anything else: it sees what any reader sees then.
	var changes []Change
	var seen []NodeRun
	s.SetNotifier(func(c Change) {
		changes = append(changes, c)
		if len(changes) == 1 {
			page, err := s.ListNodeRuns(ctx(), NodeRunFilter{FlowRunID: fr.ID, ListOpts: ListOpts{Oldest: true}})
			must(t, err)
			seen = page.Items
		}
	})

	parent.Status, parent.Decision = NodeDone, DecisionEmitted
	next := []*NodeRun{
		{FlowRunID: fr.ID, NodeID: "a", NodeType: NodeInbox, ParentNodeRunID: parent.ID, Stdin: Inline(`{"n":1}`)},
		{FlowRunID: fr.ID, NodeID: "b", NodeType: NodeInbox, ParentNodeRunID: parent.ID, Stdin: Inline(`{"n":2}`)},
	}
	must(t, s.FinishNodeRun(ctx(), parent, next))

	if len(seen) != 3 || seen[0].Status != NodeDone || seen[1].Status != NodeQueued || seen[2].Status != NodeQueued ||
		seen[1].ParentNodeRunID != parent.ID || seen[2].ParentNodeRunID != parent.ID {
		t.Errorf("node runs right after the commit: %+v, want the parent done and both follow-ups queued", seen)
	}
	want := []Change{{ResNodeRun, parent.ID, OpUpdate}, {ResNodeRun, next[0].ID, OpCreate}, {ResNodeRun, next[1].ID, OpCreate}}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	for i, w := range want {
		if changes[i] != w {
			t.Errorf("change[%d] = %+v, want %+v", i, changes[i], w)
		}
	}

	// Without follow-ups it is a plain update.
	next[0].Status, next[0].Decision = NodeDone, DecisionDropped
	must(t, s.FinishNodeRun(ctx(), next[0], nil))
	if got, err := s.GetNodeRun(ctx(), next[0].ID); err != nil || got.Status != NodeDone || got.Decision != DecisionDropped {
		t.Errorf("node run finished without follow-ups: %+v, %v", got, err)
	}
}

func TestFinishNodeRunAndCreateNodeRunsAreAllOrNothing(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	parent := mkNodeRun(t, s, fr.ID, "split", NodeCommand, NodeRunning)
	var changes int
	s.SetNotifier(func(Change) { changes++ })

	count := func() int {
		t.Helper()
		page, err := s.ListNodeRuns(ctx(), NodeRunFilter{FlowRunID: fr.ID})
		must(t, err)
		return len(page.Items)
	}
	// The second node run belongs to a flow run that does not exist, which the
	// foreign key rejects after the first was inserted.
	good := func() *NodeRun {
		return &NodeRun{FlowRunID: fr.ID, NodeID: "a", NodeType: NodeInbox, ParentNodeRunID: parent.ID}
	}
	orphan := func() *NodeRun { return &NodeRun{FlowRunID: "missing", NodeID: "b", NodeType: NodeInbox} }
	invalid := func() *NodeRun { return &NodeRun{FlowRunID: fr.ID, NodeID: "c", NodeType: "bogus"} }

	parent.Status, parent.Decision = NodeDone, DecisionEmitted
	for name, next := range map[string][]*NodeRun{
		"rejected by the database": {good(), orphan()},
		"invalid":                  {good(), invalid()},
	} {
		if err := s.FinishNodeRun(ctx(), parent, next); err == nil {
			t.Fatalf("FinishNodeRun accepted follow-ups that are %s", name)
		}
		if got, err := s.GetNodeRun(ctx(), parent.ID); err != nil || got.Status != NodeRunning {
			t.Errorf("after FinishNodeRun with follow-ups %s: parent %+v, %v; want it left running", name, got, err)
		}
		if err := s.CreateNodeRuns(ctx(), next); err == nil {
			t.Fatalf("CreateNodeRuns accepted node runs that are %s", name)
		}
		if n := count(); n != 1 {
			t.Errorf("with node runs %s: %d node runs exist, want only the parent", name, n)
		}
	}
	if changes != 0 {
		t.Errorf("%d changes were notified for work that was rolled back", changes)
	}

	if err := s.FinishNodeRun(ctx(), &NodeRun{ID: "missing", Status: "bogus"}, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("FinishNodeRun with an invalid status: %v", err)
	}
	if err := s.FinishNodeRun(ctx(), &NodeRun{ID: "missing", Status: NodeDone}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("FinishNodeRun of an unknown node run: %v", err)
	}

	batch := []*NodeRun{good(), {FlowRunID: fr.ID, NodeID: "d", NodeType: NodeInbox}}
	must(t, s.CreateNodeRuns(ctx(), batch))
	if n := count(); n != 3 {
		t.Errorf("%d node runs after creating two, want 3", n)
	}
}

// A run is cancelled with everything queued in it, including the follow-ups of
// a node that finished a moment before: they exist by then, so none is left
// queued behind a cancelled run.
func TestCancelQueuedTakesTheFollowUpsOfAFinishedNode(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	parent := mkNodeRun(t, s, fr.ID, "split", NodeCommand, NodeRunning)
	waiting := mkNodeRun(t, s, fr.ID, "other", NodeCommand, NodeQueued)

	if _, err := s.CancelQueued(ctx(), fr.ID, "me"); !errors.Is(err, ErrNotQueued) {
		t.Fatalf("CancelQueued while a node runs: %v, want ErrNotQueued", err)
	}

	parent.Status, parent.Decision = NodeDone, DecisionEmitted
	next := &NodeRun{FlowRunID: fr.ID, NodeID: "note", NodeType: NodeInbox, ParentNodeRunID: parent.ID}
	must(t, s.FinishNodeRun(ctx(), parent, []*NodeRun{next}))

	got, err := s.CancelQueued(ctx(), fr.ID, "me")
	must(t, err)
	if got.Status != FlowRunCancelled {
		t.Errorf("flow run is %s, want cancelled", got.Status)
	}
	for _, id := range []string{waiting.ID, next.ID} {
		n, err := s.GetNodeRun(ctx(), id)
		must(t, err)
		if n.Status != NodeCancelled {
			t.Errorf("node run %s is %s after the cancellation, want cancelled", n.NodeID, n.Status)
		}
	}
}
