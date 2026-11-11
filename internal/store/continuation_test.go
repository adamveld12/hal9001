package store

import (
	"errors"
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
