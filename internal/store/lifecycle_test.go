package store

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hal9001/internal/store/blob"
)

func TestMarkInterruptedOnRestart(t *testing.T) {
	s, _ := newTestStore(t)

	// Sensor poll in flight.
	poll := &SensorRun{SensorID: "gh"}
	must(t, s.CreateSensorRun(ctx(), poll))

	// Flow 1: a command node running; nothing waiting.
	f1 := mkFlowRun(t, s, "F", "", FlowRunRunning)
	n1 := mkNodeRun(t, s, f1.ID, "filter", NodeCommand, NodeRunning)

	// Flow 2: per-tool agent paused on a tool_call approval (process dies on restart).
	f2 := mkFlowRun(t, s, "F", "", FlowRunAwaitingApproval)
	n2 := mkNodeRun(t, s, f2.ID, "agent", NodeAgent, NodeAwaitingApproval)
	a2 := mkAgentRun(t, s, n2.ID, StylePerTool, AgentAwaitingApproval)
	pr := &PermissionRequest{AgentRunID: a2.ID, ToolKind: "edit", Title: "Edit main.go"}
	must(t, s.CreatePermissionRequest(ctx(), pr))
	tool := &Approval{AgentRunID: a2.ID, Type: ApprovalToolCall, PermissionRequestID: pr.ID}
	must(t, s.CreateApproval(ctx(), valid(tool)))

	// Flow 3: proposal waiting for the user; the agent process already exited.
	f3 := mkFlowRun(t, s, "F", "", FlowRunAwaitingApproval)
	n3 := mkNodeRun(t, s, f3.ID, "agent", NodeAgent, NodeAwaitingApproval)
	a3 := mkAgentRun(t, s, n3.ID, StyleProposal, AgentAwaitingApproval)
	prop := &Approval{AgentRunID: a3.ID, Type: ApprovalProposal, Proposal: Inline("Plan: do X")}
	must(t, s.CreateApproval(ctx(), valid(prop)))

	// Queued work resumes.
	f4 := mkFlowRun(t, s, "F", "", FlowRunQueued)
	n4 := mkNodeRun(t, s, f4.ID, "x", NodeCommand, NodeQueued)
	a4 := mkAgentRun(t, s, n4.ID, StyleProposal, AgentQueued)

	// Auto agent mid-run.
	f5 := mkFlowRun(t, s, "F", "", FlowRunRunning)
	n5 := mkNodeRun(t, s, f5.ID, "agent", NodeAgent, NodeRunning)
	a5 := mkAgentRun(t, s, n5.ID, "", AgentRunning)

	res, err := s.MarkInterrupted(ctx())
	must(t, err)
	if res.ExpiredApprovals != 1 || res.SensorRuns != 1 || res.FlowRuns != 3 || res.NodeRuns != 3 || res.AgentRuns != 2 || res.ExpiredPermissions != 1 {
		t.Errorf("result = %+v", res)
	}

	flow := func(id string) FlowRunStatus { fr, err := s.GetFlowRun(ctx(), id); must(t, err); return fr.Status }
	node := func(id string) NodeRunStatus { n, err := s.GetNodeRun(ctx(), id); must(t, err); return n.Status }
	agent := func(id string) AgentStatus { a, err := s.GetAgentRun(ctx(), id); must(t, err); return a.Status }

	for name, c := range map[string][2]any{
		"running flow run":         {flow(f1.ID), FlowRunInterrupted},
		"running node run":         {node(n1.ID), NodeInterrupted},
		"per-tool flow run":        {flow(f2.ID), FlowRunInterrupted},
		"per-tool node run":        {node(n2.ID), NodeInterrupted},
		"per-tool agent run":       {agent(a2.ID), AgentInterrupted},
		"proposal flow run":        {flow(f3.ID), FlowRunAwaitingApproval},
		"proposal node run":        {node(n3.ID), NodeAwaitingApproval},
		"proposal agent run":       {agent(a3.ID), AgentAwaitingApproval},
		"queued flow run":          {flow(f4.ID), FlowRunQueued},
		"queued node run":          {node(n4.ID), NodeQueued},
		"queued agent run":         {agent(a4.ID), AgentQueued},
		"auto agent run (running)": {agent(a5.ID), AgentInterrupted},
		"auto flow run (running)":  {flow(f5.ID), FlowRunInterrupted},
		"auto node run (running)":  {node(n5.ID), NodeInterrupted},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: status %v, want %v", name, c[0], c[1])
		}
	}

	got, err := s.GetApproval(ctx(), tool.ID)
	must(t, err)
	if got.Status != ApprovalExpired || got.DecidedAt == nil {
		t.Errorf("tool approval = %+v, want expired with decided_at", got)
	}
	perm, err := s.GetPermissionRequest(ctx(), pr.ID)
	must(t, err)
	if perm.Decision != PermExpired || perm.DecidedBy != DecidedByTimeout {
		t.Errorf("permission request = %s/%s, want expired/timeout", perm.Decision, perm.DecidedBy)
	}
	got, err = s.GetApproval(ctx(), prop.ID)
	must(t, err)
	if got.Status != ApprovalPending {
		t.Errorf("proposal approval = %s, want still pending", got.Status)
	}
	// The preserved proposal can still be approved after the restart.
	if _, err := s.DecideApproval(ctx(), prop.ID, ApprovalDecision{Status: ApprovalApproved, DecidedBy: "me"}); err != nil {
		t.Errorf("approving surviving proposal: %v", err)
	}
	// The expired one cannot be.
	if _, err := s.DecideApproval(ctx(), tool.ID, ApprovalDecision{Status: ApprovalApproved}); !errors.Is(err, ErrConflict) {
		t.Errorf("approving expired tool call: %v, want ErrConflict", err)
	}

	// Running it again changes nothing.
	again, err := s.MarkInterrupted(ctx())
	must(t, err)
	if again != (InterruptResult{}) {
		t.Errorf("second MarkInterrupted changed %+v", again)
	}
}

func TestDecideApprovalIsCompareAndSet(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunAwaitingApproval)
	n := mkNodeRun(t, s, fr.ID, "agent", NodeAgent, NodeAwaitingApproval)
	a := mkAgentRun(t, s, n.ID, StylePerTool, AgentAwaitingApproval)
	pr := &PermissionRequest{AgentRunID: a.ID, ToolKind: "execute", Title: "rm -rf"}
	must(t, s.CreatePermissionRequest(ctx(), pr))
	ap := &Approval{AgentRunID: a.ID, Type: ApprovalToolCall, PermissionRequestID: pr.ID}
	must(t, s.CreateApproval(ctx(), valid(ap)))

	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			status, perm := ApprovalApproved, PermAllow
			if i%2 == 0 {
				status, perm = ApprovalDenied, PermReject
			}
			_, err := s.DecideApproval(ctx(), ap.ID, ApprovalDecision{Status: status, DecidedBy: "me", PermissionDecision: perm})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 15 {
		t.Fatalf("wins=%d conflicts=%d, want 1 and 15", wins.Load(), conflicts.Load())
	}
	// The permission request records the same winning decision, by the user.
	got, _ := s.GetApproval(ctx(), ap.ID)
	perm, _ := s.GetPermissionRequest(ctx(), pr.ID)
	wantPerm := PermReject
	if got.Status == ApprovalApproved {
		wantPerm = PermAllow
	}
	if perm.Decision != wantPerm || perm.DecidedBy != DecidedByUser {
		t.Errorf("approval %s but permission request %s/%s", got.Status, perm.Decision, perm.DecidedBy)
	}
	if _, err := s.DecideApproval(ctx(), "nope", ApprovalDecision{Status: ApprovalDenied}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown approval: %v", err)
	}
	if _, err := s.DecideApproval(ctx(), ap.ID, ApprovalDecision{Status: ApprovalPending}); !errors.Is(err, ErrInvalid) {
		t.Errorf("deciding as pending: %v", err)
	}
}

func bigText(n int) string { return strings.Repeat("x", n) }

// seedRun creates event -> flow run -> node run -> agent run -> approval/permission, with a
// distinct big stdout and prompt so each has its own blob.
type seededRun struct {
	event *Event
	flow  *FlowRun
	node  *NodeRun
	agent *AgentRun
	refs  []string
}

func seedRun(t *testing.T, s *Store, key string, status FlowRunStatus) seededRun {
	t.Helper()
	ev := mkEvent(t, s, "gh", key, `{"k":"`+key+`"}`)
	fr := mkFlowRun(t, s, "F", ev.ID, status)
	n := &NodeRun{FlowRunID: fr.ID, NodeID: "agent", NodeType: NodeAgent, Status: NodeDone, Stdout: Inline(bigText(SpillThreshold+1) + key)}
	must(t, s.CreateNodeRun(ctx(), n))
	a := &AgentRun{NodeRunID: n.ID, Runtime: "claude", Mode: ModeAuto, Policy: PolicyAuto, Phase: PhaseExecute, Status: AgentSucceeded,
		Prompt: Inline(bigText(SpillThreshold+2) + key), WorktreePath: "/wt/" + key}
	must(t, s.CreateAgentRun(ctx(), a))
	if n.Stdout.Ref == "" || a.Prompt.Ref == "" {
		t.Fatal("seed payloads did not spill")
	}
	return seededRun{event: ev, flow: fr, node: n, agent: a, refs: []string{n.Stdout.Ref, a.Prompt.Ref}}
}

func TestPruneKeepsStarredAndPendingAndReportsOrphans(t *testing.T) {
	s, clk := newTestStore(t)
	old := seedRun(t, s, "old", FlowRunSucceeded)         // plain: pruned
	starred := seedRun(t, s, "starred", FlowRunSucceeded) // kept: starred inbox item
	pending := seedRun(t, s, "pending", FlowRunInterrupted)
	running := seedRun(t, s, "running", FlowRunRunning) // kept: not terminal
	_ = running

	it := &InboxItem{SourceType: SourceAgentRun, AgentRunID: starred.agent.ID, Title: "keep me", Starred: true}
	must(t, s.CreateInboxItem(ctx(), it))
	if it.FlowRunID != starred.flow.ID || it.NodeRunID != starred.node.ID {
		t.Fatalf("inbox links not derived from the agent run: %+v", it)
	}
	unstarred := &InboxItem{SourceType: SourceAgentRun, AgentRunID: old.agent.ID, Title: "forget me"}
	must(t, s.CreateInboxItem(ctx(), unstarred))
	must(t, s.CreateApproval(ctx(), valid(&Approval{AgentRunID: pending.agent.ID, Type: ApprovalProposal, Proposal: Inline(bigText(SpillThreshold + 3))})))

	// A shared blob: both the old run and the kept pending run hold the same stdout.
	shared := bigText(SpillThreshold + 9)
	for _, r := range []seededRun{old, pending} {
		n := &NodeRun{FlowRunID: r.flow.ID, NodeID: "shared", NodeType: NodeCommand, Status: NodeDone, Stdout: Inline(shared)}
		must(t, s.CreateNodeRun(ctx(), n))
	}
	sharedRef := blob.RefOf([]byte(shared))

	// Other old rows.
	must(t, s.CreateAuthEvent(ctx(), &AuthEvent{Outcome: "success", Service: ServiceAPI}))
	must(t, s.CreateSession(ctx(), &Session{ID: "sess-old", Principal: "me", ExpiresAt: clk.Now().Add(time.Hour)}))
	sr := &SensorRun{SensorID: "gh", Status: SensorSucceeded, Stdout: Inline(bigText(SpillThreshold + 4))}
	must(t, s.CreateSensorRun(ctx(), sr))
	_, err := s.PutFlowVersion(ctx(), "F", "h-old", "old yaml")
	must(t, err)

	clk.Advance(40 * 24 * time.Hour)
	fresh := seedRun(t, s, "fresh", FlowRunSucceeded) // too young to prune

	res, err := s.Prune(ctx(), clk.Now(), 30, 180)
	must(t, err)

	if res.FlowRuns != 1 {
		t.Errorf("pruned %d flow runs, want 1 (only the plain old one): %+v", res.FlowRuns, res)
	}
	for name, r := range map[string]seededRun{"starred": starred, "pending": pending, "running": running, "fresh": fresh} {
		if _, err := s.GetFlowRun(ctx(), r.flow.ID); err != nil {
			t.Errorf("%s flow run was pruned: %v", name, err)
		}
		if _, err := s.GetNodeRun(ctx(), r.node.ID); err != nil {
			t.Errorf("%s node run was pruned: %v", name, err)
		}
		if _, err := s.GetEvent(ctx(), r.event.ID); err != nil {
			t.Errorf("%s event was pruned: %v", name, err)
		}
	}
	if _, err := s.GetFlowRun(ctx(), old.flow.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old flow run survived: %v", err)
	}
	if _, err := s.GetNodeRun(ctx(), old.node.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old node run survived (cascade): %v", err)
	}
	if _, err := s.GetAgentRun(ctx(), old.agent.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old agent run survived (cascade): %v", err)
	}
	if _, err := s.GetEvent(ctx(), old.event.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old event survived: %v", err)
	}
	if res.Events != 1 || res.SensorRuns != 1 || res.AuthEvents != 1 || res.Sessions != 1 {
		t.Errorf("result = %+v", res)
	}
	// The unstarred inbox note goes, the starred one stays.
	if _, err := s.GetInboxItem(ctx(), unstarred.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unstarred inbox item survived: %v", err)
	}
	if _, err := s.GetInboxItem(ctx(), it.ID); err != nil {
		t.Errorf("starred inbox item pruned: %v", err)
	}
	// The pending approval is intact.
	if p, err := s.ListApprovals(ctx(), ApprovalFilter{Statuses: []ApprovalState{ApprovalPending}}); err != nil || len(p.Items) != 1 {
		t.Errorf("pending approvals = %v %v", p.Items, err)
	}

	// Orphans: the old run's two blobs and the old sensor run's stdout; NOT the
	// shared blob (still referenced by the kept run) nor anything of kept runs.
	orphans := map[string]bool{}
	for _, r := range res.OrphanBlobs {
		orphans[r] = true
	}
	for _, r := range old.refs {
		if !orphans[r] {
			t.Errorf("blob %s of the pruned run not reported as orphan", r)
		}
	}
	if !orphans[sr.Stdout.Ref] {
		t.Error("pruned sensor run's stdout blob not reported")
	}
	if orphans[sharedRef] {
		t.Error("blob still referenced by a kept run was reported as orphan")
	}
	for _, r := range append(append(append([]string{}, starred.refs...), pending.refs...), fresh.refs...) {
		if orphans[r] {
			t.Errorf("blob %s of a kept run was reported as orphan", r)
		}
	}
	if len(res.OrphanBlobs) != 3 {
		t.Errorf("orphans = %d, want 3: %v", len(res.OrphanBlobs), res.OrphanBlobs)
	}
	if len(res.Worktrees) != 1 || res.Worktrees[0] != "/wt/old" {
		t.Errorf("worktrees = %v, want [/wt/old]", res.Worktrees)
	}

	// Prune doesn't delete files; RemoveOrphanBlobs does, and spares referenced ones.
	if !s.Blobs().Has(old.refs[0]) {
		t.Fatal("Prune deleted a blob file itself")
	}
	removed, err := s.RemoveOrphanBlobs(ctx(), append(res.OrphanBlobs, starred.refs[0]))
	must(t, err)
	if len(removed) != 3 {
		t.Errorf("removed %d blobs, want 3 (the still-referenced one must be spared)", len(removed))
	}
	if s.Blobs().Has(old.refs[0]) || !s.Blobs().Has(starred.refs[0]) {
		t.Error("wrong blobs on disk after RemoveOrphanBlobs")
	}
	if got, err := s.ReadContent(starred.node.Stdout); err != nil || len(got) != SpillThreshold+1+len("starred") {
		t.Errorf("kept run's content unreadable after cleanup: %d bytes, %v", len(got), err)
	}
}

func TestPruneDedupKeysOutliveEvents(t *testing.T) {
	s, clk := newTestStore(t)
	mkEvent(t, s, "gh", "old", "{}")
	clk.Advance(60 * 24 * time.Hour)
	mkEvent(t, s, "gh", "recent", "{}")

	// 60 days on: the event is past retention (30d) but the key is within dedup retention (180d).
	res, err := s.Prune(ctx(), clk.Now(), 30, 180)
	must(t, err)
	if res.Events != 1 || res.SeenKeys != 0 {
		t.Fatalf("result = %+v, want the event pruned and no keys", res)
	}
	if ok, err := s.IngestEvent(ctx(), &Event{SensorID: "gh", DedupKey: "old"}); err != nil || ok {
		t.Fatalf("pruned item re-fired: new=%v err=%v", ok, err)
	}

	// Past dedup retention the key is forgotten.
	clk.Advance(200 * 24 * time.Hour)
	res, err = s.Prune(ctx(), clk.Now(), 30, 180)
	must(t, err)
	if res.SeenKeys != 2 {
		t.Fatalf("seen keys pruned = %d, want 2", res.SeenKeys)
	}
	if ok, err := s.ClaimEvent(ctx(), "gh", "old"); err != nil || !ok {
		t.Errorf("key still remembered after dedup retention: new=%v err=%v", ok, err)
	}
	if _, err := s.Prune(ctx(), clk.Now(), 0, 180); !errors.Is(err, ErrInvalid) {
		t.Errorf("zero retention accepted: %v", err)
	}
}

func TestPruneKeepsNewestFlowVersionAndReferencedOnes(t *testing.T) {
	s, clk := newTestStore(t)
	for _, h := range []string{"v1", "v2", "v3"} {
		_, err := s.PutFlowVersion(ctx(), "F", h, "yaml "+h)
		must(t, err)
		clk.Advance(time.Hour)
	}
	must(t, s.CreateFlowRun(ctx(), &FlowRun{FlowID: "F", FlowHash: "v1", Status: FlowRunRunning}))
	clk.Advance(60 * 24 * time.Hour)
	res, err := s.Prune(ctx(), clk.Now(), 30, 180)
	must(t, err)
	if res.FlowVersions != 1 {
		t.Fatalf("pruned %d versions, want only v2 (v1 referenced by a run, v3 newest)", res.FlowVersions)
	}
	vs, err := s.ListFlowVersions(ctx(), "F")
	must(t, err)
	if len(vs) != 2 || vs[0].Hash != "v3" || vs[1].Hash != "v1" {
		t.Errorf("remaining versions = %+v", vs)
	}
}

func TestGCBlobsRemovesOnlyOldUnreferencedBlobs(t *testing.T) {
	s, _ := newTestStore(t)
	kept := mkRunWithBlob(t, s)
	orphan, err := s.PutBlob([]byte("orphan one"))
	must(t, err)

	// A just-written blob may belong to a row that is about to be created.
	removed, err := s.GCBlobs(ctx(), time.Hour)
	must(t, err)
	if len(removed) != 0 || !s.Blobs().Has(orphan.Ref) {
		t.Fatalf("fresh orphan collected despite grace period: %v", removed)
	}
	time.Sleep(10 * time.Millisecond)
	removed, err = s.GCBlobs(ctx(), 5*time.Millisecond)
	must(t, err)
	if len(removed) != 1 || removed[0] != orphan.Ref {
		t.Fatalf("removed = %v, want only the orphan", removed)
	}
	if !s.Blobs().Has(kept.Ref) {
		t.Error("GC removed a referenced blob")
	}
}

func mkRunWithBlob(t *testing.T, s *Store) Content {
	t.Helper()
	r := seedRun(t, s, "gc", FlowRunSucceeded)
	return r.node.Stdout
}
