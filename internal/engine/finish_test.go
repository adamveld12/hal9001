package engine_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// A node run is recorded as finished in the same commit that queues the node
// runs it feeds. Done as two commits, another node of the run could finish in
// between, see every node finished and end the flow run: the follow-ups were
// then skipped ("flow run succeeded") and their inbox items never created,
// although an API client had been told the run succeeded (the flaky "1 dry
// (want 2)" of the dry-run replay e2e test).
//
// The race itself needs lucky timing, so this checks what makes it possible:
// the follow-ups exist when the finished node run is first observable.
func TestFinishedNodeAndItsFollowUpsAreCommittedTogether(t *testing.T) {
	h := newHarness(t)
	h.idleSensor()
	h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: split
    type: command
    lang: bash
    script: |
      echo '{"n":1}'
      echo '{"n":2}'
  - {id: note, type: inbox, title: "note {{.input.n}}"}
edges:
  - {from: split, to: note}
`)
	var mu sync.Mutex
	var followUps []int // per commit that recorded split as done: the node runs it already fed
	h.st.SetNotifier(func(c store.Change) {
		if c.Resource != store.ResNodeRun || c.Op != store.OpUpdate {
			return
		}
		nr, err := h.st.GetNodeRun(context.Background(), c.ID)
		if err != nil || nr.NodeID != "split" || nr.Status != store.NodeDone {
			return
		}
		page, err := h.st.ListNodeRuns(context.Background(), store.NodeRunFilter{FlowRunID: nr.FlowRunID, Slim: true})
		if err != nil {
			return
		}
		n := 0
		for _, k := range page.Items {
			if k.ParentNodeRunID == nr.ID {
				n++
			}
		}
		mu.Lock()
		followUps = append(followUps, n)
		mu.Unlock()
	})
	h.start()

	id := h.waitRun(h.injectRun(`{"id":"1"}`), store.FlowRunSucceeded).ID

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(followUps, []int{2}) {
		t.Errorf("follow-ups queued when the split node was recorded as done: %v, want one commit with both\n%s", followUps, h.dumpRun(id))
	}
	titles := map[string]bool{}
	for _, it := range h.inboxItems() {
		titles[it.Title] = true
	}
	if len(titles) != 2 || !titles["note 1"] || !titles["note 2"] {
		t.Errorf("inbox titles = %v, want note 1 and note 2\n%s", titles, h.dumpRun(id))
	}
	for _, n := range h.nodeRuns(id) {
		if n.Status != store.NodeDone {
			t.Errorf("node %s is %s (%s), want done\n%s", n.NodeID, n.Status, n.Reason, h.dumpRun(id))
		}
	}
}

// The entry nodes of a flow test are queued in one commit. The dispatcher may
// pick up the first while the second is still being queued, and a run whose
// queued node runs have all finished is over.
func TestFlowTestQueuesAllEntryNodesInOneCommit(t *testing.T) {
	h := newHarness(t)
	h.start()
	flow, ds := spec.ParseFlow([]byte("id: draft\nenabled: true\nsubscribe: [src]\nnodes:\n  - {id: a, type: inbox, title: A}\n  - {id: b, type: inbox, title: B}\n"))
	if flow == nil {
		t.Fatalf("parse flow: %v", ds)
	}

	var once sync.Once
	var mu sync.Mutex
	queued := -1 // node runs of the test run when the first of them became visible
	h.st.SetNotifier(func(c store.Change) {
		if c.Resource != store.ResNodeRun || c.Op != store.OpCreate {
			return
		}
		once.Do(func() {
			nr, err := h.st.GetNodeRun(context.Background(), c.ID)
			if err != nil {
				return
			}
			page, err := h.st.ListNodeRuns(context.Background(), store.NodeRunFilter{FlowRunID: nr.FlowRunID, Slim: true})
			if err != nil {
				return
			}
			mu.Lock()
			queued = len(page.Items)
			mu.Unlock()
		})
	})

	res, err := h.eng.TestFlow(t.Context(), core.FlowTestRequest{Flow: flow, Input: json.RawMessage(`{"id":1}`)})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if queued != 2 {
		t.Errorf("node runs queued when the first became visible: %d, want both entry nodes", queued)
	}
	if res.Status != string(store.FlowRunSucceeded) || len(res.Nodes) != 2 {
		t.Fatalf("flow test result: status %s, %d nodes, want succeeded with 2", res.Status, len(res.Nodes))
	}
	for _, n := range res.Nodes {
		if n.Status != string(store.NodeDone) {
			t.Errorf("node %s is %s, want done", n.NodeID, n.Status)
		}
	}
}
