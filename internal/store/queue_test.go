package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func perFlow(limits map[string]int) func(string) int {
	return func(id string) int {
		if l, ok := limits[id]; ok {
			return l
		}
		return Unlimited
	}
}

func claimAllFlowRuns(t *testing.T, s *Store, limit func(string) int) []*FlowRun {
	t.Helper()
	var out []*FlowRun
	for {
		fr, err := s.ClaimNextFlowRun(ctx(), limit)
		must(t, err)
		if fr == nil {
			return out
		}
		out = append(out, fr)
	}
}

func TestFlowRunClaimingRespectsPerFlowLimit(t *testing.T) {
	s, _ := newTestStore(t)
	var a, b []*FlowRun
	for range 3 {
		a = append(a, mkFlowRun(t, s, "A", "", FlowRunQueued))
	}
	for range 3 {
		b = append(b, mkFlowRun(t, s, "B", "", FlowRunQueued))
	}
	limit := perFlow(map[string]int{"A": 1, "B": 2})

	claimed := claimAllFlowRuns(t, s, limit)
	// Oldest first, but A's second and third runs must be skipped while A is at
	// its cap, not block B behind them.
	want := []string{a[0].ID, b[0].ID, b[1].ID}
	if len(claimed) != 3 {
		t.Fatalf("claimed %d runs, want 3", len(claimed))
	}
	for i, w := range want {
		if claimed[i].ID != w || claimed[i].Status != FlowRunRunning || claimed[i].StartedAt == nil {
			t.Errorf("claim %d = %+v, want id %s running with started_at", i, claimed[i], w)
		}
	}
	c, err := s.Counts(ctx())
	must(t, err)
	if c.FlowRunsRunning != 3 || c.FlowRunsQueued != 3 {
		t.Errorf("counts = %+v", c)
	}

	// Finishing A's run frees its slot for A's next queued run only.
	a[0].Status = FlowRunSucceeded
	must(t, s.UpdateFlowRun(ctx(), a[0]))
	next := claimAllFlowRuns(t, s, limit)
	if len(next) != 1 || next[0].ID != a[1].ID {
		t.Fatalf("after finishing A#1 claimed %v, want only A#2", next)
	}

	// A run waiting for approval does not hold a concurrency slot.
	a[1].Status = FlowRunAwaitingApproval
	must(t, s.UpdateFlowRun(ctx(), a[1]))
	next = claimAllFlowRuns(t, s, limit)
	if len(next) != 1 || next[0].ID != a[2].ID {
		t.Fatalf("awaiting_approval run held a slot; claimed %v", next)
	}
}

func TestFlowRunLimitZeroNeverClaimsAndTestRunsDontCount(t *testing.T) {
	s, _ := newTestStore(t)
	mkFlowRun(t, s, "off", "", FlowRunQueued)
	live := mkFlowRun(t, s, "on", "", FlowRunQueued)
	// A running test run of the same flow is not a live run and holds no slot.
	tr := &FlowRun{FlowID: "on", FlowHash: "h1", Kind: KindTest, Status: FlowRunRunning}
	must(t, s.CreateFlowRun(ctx(), tr))

	got := claimAllFlowRuns(t, s, perFlow(map[string]int{"off": 0, "on": 1}))
	if len(got) != 1 || got[0].ID != live.ID {
		t.Fatalf("claimed %v, want only the 'on' live run", got)
	}
}

func TestConcurrentClaimsNeverExceedLimit(t *testing.T) {
	s, _ := newTestStore(t)
	for range 20 {
		mkFlowRun(t, s, "F", "", FlowRunQueued)
	}
	var claimed atomic.Int32
	seen := sync.Map{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for {
				fr, err := s.ClaimNextFlowRun(ctx(), perFlow(map[string]int{"F": 5}))
				if err != nil {
					t.Error(err)
					return
				}
				if fr == nil {
					return
				}
				if _, dup := seen.LoadOrStore(fr.ID, true); dup {
					t.Errorf("run %s claimed twice", fr.ID)
				}
				claimed.Add(1)
			}
		})
	}
	wg.Wait()
	if claimed.Load() != 5 {
		t.Fatalf("claimed %d runs with limit 5", claimed.Load())
	}
}

func TestNodeRunClaimingPoolsAndAgentSlots(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	var agents []*NodeRun
	for range 4 {
		agents = append(agents, mkNodeRun(t, s, fr.ID, "agent", NodeAgent, NodeQueued))
	}
	cmd := mkNodeRun(t, s, fr.ID, "cmd", NodeCommand, NodeQueued)
	inbox := mkNodeRun(t, s, fr.ID, "note", NodeInbox, NodeQueued)
	limits := NodeLimits{NodeCommand: 1, NodeAgent: 2, NodeInbox: Unlimited}

	claim := func() []*NodeRun {
		var out []*NodeRun
		for {
			n, err := s.ClaimNextNodeRun(ctx(), limits)
			must(t, err)
			if n == nil {
				return out
			}
			if n.Status != NodeRunning || n.StartedAt == nil {
				t.Fatalf("claimed node run not running: %+v", n)
			}
			out = append(out, n)
		}
	}
	got := claim()
	if len(got) != 4 { // 2 agents, 1 command, 1 inbox
		t.Fatalf("claimed %d node runs, want 4 (2 agent + cmd + inbox)", len(got))
	}
	if got[0].ID != agents[0].ID || got[1].ID != agents[1].ID || got[2].ID != cmd.ID || got[3].ID != inbox.ID {
		t.Errorf("claim order = %s %s %s %s, want oldest first within each pool", got[0].NodeID, got[1].NodeID, got[2].NodeID, got[3].NodeID)
	}

	// Agent #1 waits on a proposal approval: it releases its slot.
	a1 := got[0]
	a1.Status = NodeAwaitingApproval
	must(t, s.UpdateNodeRun(ctx(), a1))
	ar := mkAgentRun(t, s, a1.ID, StyleProposal, AgentAwaitingApproval)
	_ = ar
	if n := claim(); len(n) != 1 || n[0].ID != agents[2].ID {
		t.Fatalf("proposal-waiting agent should free a slot for exactly one more agent, got %v", n)
	}

	// Agent #2 waits on a per-tool approval: its process is alive, so it keeps its slot.
	a2 := got[1]
	a2.Status = NodeAwaitingApproval
	must(t, s.UpdateNodeRun(ctx(), a2))
	mkAgentRun(t, s, a2.ID, StylePerTool, AgentAwaitingApproval)
	if n := claim(); len(n) != 0 {
		t.Fatalf("per-tool waiting agent released its slot; claimed %v", n)
	}

	// Completing a running agent frees one.
	a3 := agents[2]
	a3.Status, a3.Decision = NodeDone, DecisionEmitted
	must(t, s.UpdateNodeRun(ctx(), a3))
	if n := claim(); len(n) != 1 || n[0].ID != agents[3].ID {
		t.Fatalf("expected the last queued agent after a slot freed, got %v", n)
	}

	// Types missing from the limits map are never claimed.
	mkNodeRun(t, s, fr.ID, "cmd2", NodeCommand, NodeQueued)
	if n, err := s.ClaimNextNodeRun(ctx(), NodeLimits{NodeAgent: Unlimited}); err != nil || n != nil {
		t.Errorf("claimed a command node with no command pool: %v %v", n, err)
	}
}

func TestConcurrentNodeClaimsRespectGlobalAgentLimit(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	for range 12 {
		mkNodeRun(t, s, fr.ID, "agent", NodeAgent, NodeQueued)
	}
	var n atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for {
				got, err := s.ClaimNextNodeRun(ctx(), NodeLimits{NodeAgent: 2})
				if err != nil {
					t.Error(err)
					return
				}
				if got == nil {
					return
				}
				n.Add(1)
			}
		})
	}
	wg.Wait()
	if n.Load() != 2 {
		t.Fatalf("claimed %d agent node runs under max_agent_runs=2", n.Load())
	}
}

func TestClaimNextAgentRunHonoursGlobalCap(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	nr := mkNodeRun(t, s, fr.ID, "agent", NodeAgent, NodeRunning)
	var runs []*AgentRun
	for range 5 {
		runs = append(runs, mkAgentRun(t, s, nr.ID, StyleProposal, AgentQueued))
	}
	var claimed []*AgentRun
	for {
		a, err := s.ClaimNextAgentRun(ctx(), 2)
		must(t, err)
		if a == nil {
			break
		}
		claimed = append(claimed, a)
	}
	if len(claimed) != 2 || claimed[0].ID != runs[0].ID || claimed[1].ID != runs[1].ID {
		t.Fatalf("claimed %d agent runs, want the 2 oldest", len(claimed))
	}
	c, err := s.Counts(ctx())
	must(t, err)
	if c.AgentRunsRunning != 2 || c.AgentRunsQueued != 3 {
		t.Errorf("counts = %+v", c)
	}
	claimed[0].Status = AgentSucceeded
	must(t, s.UpdateAgentRun(ctx(), claimed[0]))
	if a, err := s.ClaimNextAgentRun(ctx(), 2); err != nil || a == nil || a.ID != runs[2].ID {
		t.Errorf("after one finished, next claim = %v %v", a, err)
	}
}

func TestCancelQueuedNeverRacesTheClaimer(t *testing.T) {
	s, _ := newTestStore(t)
	const n = 40
	ids := make([]string, n)
	for i := range ids {
		ids[i] = mkFlowRun(t, s, "F", "", FlowRunQueued).ID
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed, cancelled := map[string]bool{}, map[string]bool{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			fr, err := s.ClaimNextFlowRun(ctx(), nil)
			must(t, err)
			if fr == nil {
				return
			}
			mu.Lock()
			claimed[fr.ID] = true
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := len(ids) - 1; i >= 0; i-- {
			fr, err := s.CancelQueued(ctx(), ids[i], "me")
			if err == nil {
				if fr.Status != FlowRunCancelled || fr.CancelledBy != "me" || fr.Error != QueueReason || fr.EndedAt == nil {
					t.Errorf("cancelled run = %+v", fr)
				}
				mu.Lock()
				cancelled[ids[i]] = true
				mu.Unlock()
			}
		}
	}()
	wg.Wait()
	for _, id := range ids {
		fr, err := s.GetFlowRun(ctx(), id)
		must(t, err)
		if claimed[id] == cancelled[id] {
			t.Errorf("run %s: claimed=%v cancelled=%v, want exactly one", id, claimed[id], cancelled[id])
		}
		if want := map[bool]FlowRunStatus{true: FlowRunRunning, false: FlowRunCancelled}[claimed[id]]; fr.Status != want {
			t.Errorf("run %s status = %s, want %s", id, fr.Status, want)
		}
	}
}

func TestQueueListsAndCancelsRunsWaitingOnlyForNodeSlots(t *testing.T) {
	s, _ := newTestStore(t)
	waiting := mkFlowRun(t, s, "F", "", FlowRunRunning)
	mkNodeRun(t, s, waiting.ID, "a", NodeAgent, NodeQueued)
	busy := mkFlowRun(t, s, "F", "", FlowRunRunning)
	mkNodeRun(t, s, busy.ID, "a", NodeAgent, NodeRunning)
	approval := mkFlowRun(t, s, "F", "", FlowRunAwaitingApproval)
	queued := mkFlowRun(t, s, "G", "", FlowRunQueued)
	test := &FlowRun{FlowID: "F", FlowHash: "h1", Kind: KindTest, Status: FlowRunQueued}
	must(t, s.CreateFlowRun(ctx(), test))

	q, err := s.ListQueue(ctx(), "")
	must(t, err)
	var got []string
	for _, it := range q.Items {
		got = append(got, it.FlowRun.ID)
	}
	if len(got) != 2 || got[0] != waiting.ID || got[1] != queued.ID {
		t.Fatalf("queue = %v, want [waiting queued] (not the busy, approval or test runs)", got)
	}
	if q.Items[0].WaitingType != NodeAgent || q.Items[1].WaitingType != "" {
		t.Errorf("waiting types = %q, %q", q.Items[0].WaitingType, q.Items[1].WaitingType)
	}

	for _, id := range []string{busy.ID, approval.ID, test.ID} {
		if _, err := s.CancelQueued(ctx(), id, "me"); !errors.Is(err, ErrNotQueued) {
			t.Errorf("CancelQueued(%s) = %v, want ErrNotQueued", id, err)
		}
	}
	if _, err := s.CancelQueued(ctx(), "nope", "me"); !errors.Is(err, ErrNotFound) {
		t.Errorf("CancelQueued(unknown) = %v, want ErrNotFound", err)
	}
	fr, err := s.CancelQueued(ctx(), waiting.ID, "me")
	must(t, err)
	if fr.Status != FlowRunCancelled {
		t.Errorf("status = %s", fr.Status)
	}
	nodes, err := s.ListNodeRuns(ctx(), NodeRunFilter{FlowRunID: waiting.ID})
	must(t, err)
	if len(nodes.Items) != 1 || nodes.Items[0].Status != NodeCancelled || nodes.Items[0].Reason != QueueReason || nodes.Items[0].EndedAt == nil {
		t.Errorf("node runs after cancel = %+v", nodes.Items)
	}
	if n, err := s.ListQueue(ctx(), "F"); err != nil || len(n.Items) != 0 {
		t.Errorf("queue of F after cancel = %v, %v", n.Items, err)
	}
}

func TestCancelledRunsCountAsFinishedNotFailed(t *testing.T) {
	s, _ := newTestStore(t)
	for _, st := range []FlowRunStatus{FlowRunSucceeded, FlowRunFailed, FlowRunCancelled} {
		mkFlowRun(t, s, "F", "", st)
	}
	stats, err := s.FlowRunStats(ctx(), time.Time{})
	must(t, err)
	if st := stats["F"]; st.Runs != 3 || st.Finished != 3 || st.Failed != 1 {
		t.Errorf("stats = %+v, want 3 runs, 3 finished, 1 failed", st)
	}
	if !FlowRunCancelled.Terminal() {
		t.Error("cancelled is not terminal")
	}
}
