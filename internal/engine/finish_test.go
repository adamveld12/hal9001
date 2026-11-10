package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	flow, ds := spec.ParseFlow("draft", []byte("enabled: true\nsubscribe: [src]\nnodes:\n  - {id: a, type: inbox, title: A}\n  - {id: b, type: inbox, title: B}\n"))
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

// A flow run must not be ended by a sibling branch while a node that has just
// finished still has follow-ups to queue. The test holds the split node where
// its record becomes visible, lets the sibling end the run (it finishes, fails
// or is killed by the kill switch) and waits for the run to end before the
// split node goes on. With the follow-up committed together with the node it
// exists by then, and the run waits for it. Committed afterwards, it reaches a
// run that is already over: skipped, so its inbox item is never created (the
// dry-run replay e2e flake), or left queued for good behind the kill switch.
func TestRunWaitsForTheFollowUpsOfTheNodeThatFinishedLast(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tail   string // what the sibling does after it signals that it runs
		kill   bool   // end the sibling with the kill switch instead of opening its gate
		status store.FlowRunStatus
		note   store.NodeRunStatus // what became of the follow-up of the split node
	}{
		{"succeeded", "while [ ! -f GATE ]; do sleep 0.02; done", false, store.FlowRunSucceeded, store.NodeDone},
		{"failed", "while [ ! -f GATE ]; do sleep 0.02; done\nexit 1", false, store.FlowRunFailed, store.NodeDone},
		{"killed", "sleep 300", true, store.FlowRunKilled, store.NodeSkipped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.idleSensor()
			dir := t.TempDir()
			started, gate := filepath.Join(dir, "started"), filepath.Join(dir, "gate")
			tail := strings.ReplaceAll(strings.ReplaceAll(tc.tail, "GATE", gate), "\n", "\n      ")
			h.flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: split
    type: command
    lang: bash
    script: |
      while [ ! -f `+started+` ]; do sleep 0.02; done
      echo '{"n":1}'
  - id: sibling
    type: command
    lang: bash
    script: |
      touch `+started+`
      `+tail+`
  - {id: note, type: inbox, title: "note"}
edges:
  - {from: split, to: note}
`)
			var split, ended sync.Once
			over := make(chan struct{})
			h.st.SetNotifier(func(c store.Change) {
				switch {
				case c.Resource == store.ResFlowRun && c.Op == store.OpUpdate:
					if fr, err := h.st.GetFlowRun(context.Background(), c.ID); err == nil && fr.Status.Terminal() {
						ended.Do(func() { close(over) })
					}
				case c.Resource == store.ResNodeRun && c.Op == store.OpUpdate:
					nr, err := h.st.GetNodeRun(context.Background(), c.ID)
					if err != nil || nr.NodeID != "split" || nr.Status != store.NodeDone {
						return
					}
					split.Do(func() {
						if tc.kill {
							_, _ = h.eng.Pause(context.Background(), true, "test")
						} else {
							_ = os.WriteFile(gate, nil, 0o600)
						}
						select {
						case <-over:
						case <-time.After(waitFor):
						}
					})
				}
			})
			h.start()

			id := h.injectRun(`{"id":"1"}`)
			h.waitRun(id, tc.status)
			if n := h.node(id, "note"); n.Status != tc.note {
				t.Errorf("follow-up of the split node is %s (%s), want %s\n%s", n.Status, n.Reason, tc.note, h.dumpRun(id))
			}
			for _, n := range h.nodeRuns(id) {
				if n.Status == store.NodeQueued || n.Status == store.NodeRunning {
					t.Errorf("node %s is still %s in a %s run\n%s", n.NodeID, n.Status, tc.status, h.dumpRun(id))
				}
			}
			wantItems := 1
			if tc.note != store.NodeDone {
				wantItems = 0
			}
			if got := len(h.inboxItems()); got != wantItems {
				t.Errorf("%d inbox items, want %d\n%s", got, wantItems, h.dumpRun(id))
			}
		})
	}
}

// Removing a run that waits in the queue (the cancelled end of a run) takes the
// follow-ups of a node that finished a moment before. The run waits behind a
// full command pool (max_command_runs: 1). The engine is paused as split is
// claimed, so the slot split frees is not given to the other node before the
// test acts; the run is then removed where split's record becomes visible.
// With the follow-up committed together with the node it is cancelled with the
// rest; committed afterwards it joined a run that was already cancelled and
// stayed queued behind the kill switch.
func TestRemoveQueuedTakesTheFollowUpsOfTheNodeThatFinishedLast(t *testing.T) {
	h := newHarness(t, withConfig("max_command_runs: 1\n"))
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
  - id: other
    type: command
    lang: bash
    script: "true"
  - {id: note, type: inbox, title: note}
edges:
  - {from: split, to: note}
`)
	var paused, removed sync.Once
	h.st.SetNotifier(func(c store.Change) {
		if c.Resource != store.ResNodeRun || c.Op != store.OpUpdate {
			return
		}
		nr, err := h.st.GetNodeRun(context.Background(), c.ID)
		if err != nil || nr.NodeID != "split" {
			return
		}
		switch nr.Status {
		case store.NodeRunning:
			// Runs on the dispatcher, before split starts: once paused, nothing
			// else is claimed, however long split and the test take.
			paused.Do(func() {
				if _, err := h.eng.Pause(context.Background(), false, "test"); err != nil {
					t.Errorf("pause: %v", err)
				}
			})
		case store.NodeDone:
			removed.Do(func() {
				if _, err := h.eng.RemoveQueued(context.Background(), nr.FlowRunID, "test"); err != nil {
					t.Errorf("remove from the queue: %v", err)
				}
			})
		}
	})
	h.start()

	id := h.injectRun(`{"id":"1"}`)
	h.waitRun(id, store.FlowRunCancelled)
	for _, node := range []string{"other", "note"} {
		if n := h.node(id, node); n.Status != store.NodeCancelled {
			t.Errorf("node %s is %s, want cancelled with its run\n%s", node, n.Status, h.dumpRun(id))
		}
	}
}
