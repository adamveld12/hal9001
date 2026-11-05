package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hal9001/internal/store/blob"
)

func TestLargePayloadsSpillToBlobsAndRoundtrip(t *testing.T) {
	s, _ := newTestStore(t)

	exact := strings.Repeat("a", SpillThreshold)  // 64 KiB exactly stays inline
	over := strings.Repeat("b", SpillThreshold+1) // one byte more spills
	huge := strings.Repeat("0123456789", 300_000) // 3 MB
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	n := &NodeRun{FlowRunID: fr.ID, NodeID: "n", NodeType: NodeCommand, Stdin: Inline(exact), Stdout: Inline(over), Stderr: Inline(huge)}
	must(t, s.CreateNodeRun(ctx(), n))

	got, err := s.GetNodeRun(ctx(), n.ID)
	must(t, err)
	if got.Stdin.Text != exact || got.Stdin.Ref != "" {
		t.Errorf("64 KiB payload was not kept inline: ref=%q len=%d", got.Stdin.Ref, len(got.Stdin.Text))
	}
	for name, c := range map[string]Content{"stdout": got.Stdout, "stderr": got.Stderr} {
		if c.Ref == "" || c.Text != "" {
			t.Errorf("%s over the threshold was not spilled: ref=%q text=%d", name, c.Ref, len(c.Text))
		}
	}
	if got.Stdout.Size != int64(len(over)) || got.Stderr.Size != int64(len(huge)) {
		t.Errorf("sizes = %d, %d", got.Stdout.Size, got.Stderr.Size)
	}
	b, err := s.ReadContent(got.Stderr)
	must(t, err)
	if string(b) != huge {
		t.Error("spilled payload did not roundtrip")
	}
	if b, err := s.ReadContent(got.Stdin); err != nil || string(b) != exact {
		t.Error("inline payload did not roundtrip through ReadContent")
	}
	// The row itself holds no payload bytes.
	var text string
	must(t, s.rdb.QueryRow(`SELECT stdout_text FROM node_runs WHERE id = ?`, n.ID).Scan(&text))
	if text != "" {
		t.Errorf("spilled payload also stored inline (%d bytes)", len(text))
	}

	// Identical content is stored once.
	n2 := &NodeRun{FlowRunID: fr.ID, NodeID: "n2", NodeType: NodeCommand, Stdout: Inline(over)}
	must(t, s.CreateNodeRun(ctx(), n2))
	if n2.Stdout.Ref != n.Stdout.Ref {
		t.Error("same content produced different refs")
	}
	count := 0
	must(t, s.Blobs().Walk(func(_ blob.Info) error { count++; return nil }))
	if count != 2 {
		t.Errorf("blob files = %d, want 2 (stdout, stderr)", count)
	}

	// A missing blob surfaces as ErrNotFound rather than empty content.
	must(t, s.Blobs().Delete(n.Stdout.Ref))
	if _, err := s.ReadContent(got.Stdout); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing blob: %v", err)
	}
}

func TestUpdatePreservesUnhydratedContent(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	big := strings.Repeat("z", SpillThreshold*2)
	n := &NodeRun{FlowRunID: fr.ID, NodeID: "n", NodeType: NodeCommand, Status: NodeRunning, Stdin: Inline(`{"in":1}`), Stdout: Inline(big)}
	must(t, s.CreateNodeRun(ctx(), n))

	// Read through a slim list (no inline text), change the status, write back.
	page, err := s.ListNodeRuns(ctx(), NodeRunFilter{FlowRunID: fr.ID, Slim: true})
	must(t, err)
	slim := page.Items[0]
	if slim.Stdin.Text != "" || slim.Stdin.Size != int64(len(`{"in":1}`)) {
		t.Fatalf("slim read = %+v, want size without text", slim.Stdin)
	}
	slim.Status, slim.Decision = NodeDone, DecisionEmitted
	must(t, s.UpdateNodeRun(ctx(), &slim))

	got, err := s.GetNodeRun(ctx(), n.ID)
	must(t, err)
	if got.Stdin.Text != `{"in":1}` {
		t.Errorf("inline stdin wiped by write-back of a slim record: %+v", got.Stdin)
	}
	if got.Stdout.Ref != n.Stdout.Ref || got.Stdout.Size != int64(len(big)) {
		t.Errorf("spilled stdout lost: %+v", got.Stdout)
	}
	if got.Status != NodeDone || got.Decision != DecisionEmitted {
		t.Errorf("status/decision not updated: %s/%s", got.Status, got.Decision)
	}

	// Setting new text replaces; clearing works.
	got.Stdout = Inline("small now")
	got.Stdin = Content{}
	must(t, s.UpdateNodeRun(ctx(), got))
	got, err = s.GetNodeRun(ctx(), n.ID)
	must(t, err)
	if got.Stdout.Text != "small now" || got.Stdout.Ref != "" || got.Stdin.Size != 0 {
		t.Errorf("after replace: stdout=%+v stdin=%+v", got.Stdout, got.Stdin)
	}
}

func TestPutBlobContentIsUsableAsRawJSONRPC(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	n := mkNodeRun(t, s, fr.ID, "a", NodeAgent, NodeRunning)
	a := &AgentRun{NodeRunID: n.ID, Runtime: "claude", Mode: ModeReadonly, Policy: PolicyReadonly, Phase: PhaseExecute}
	raw, err := s.PutBlob([]byte(`{"jsonrpc":"2.0"}` + "\n"))
	must(t, err)
	a.RawJSONRPC = raw
	must(t, s.CreateAgentRun(ctx(), a))
	got, err := s.GetAgentRun(ctx(), a.ID)
	must(t, err)
	if b, err := s.ReadContent(got.RawJSONRPC); err != nil || !strings.HasPrefix(string(b), `{"jsonrpc"`) {
		t.Errorf("raw jsonrpc = %q, %v", b, err)
	}
	if got.Status != AgentQueued {
		t.Errorf("default status = %s", got.Status)
	}
}

func TestFanOutTreeFromEventToNodeRuns(t *testing.T) {
	s, _ := newTestStore(t)
	ev := mkEvent(t, s, "gh", "k", `{"pr":1}`)
	f1 := mkFlowRun(t, s, "triage", ev.ID, FlowRunSucceeded)
	f2 := mkFlowRun(t, s, "audit", ev.ID, FlowRunFailed)
	tr := &FlowRun{FlowID: "triage", FlowHash: "h1", EventID: ev.ID, Kind: KindTest, Status: FlowRunSucceeded}
	must(t, s.CreateFlowRun(ctx(), tr))

	root := mkNodeRun(t, s, f1.ID, "filter", NodeCommand, NodeDone)
	// JSONL fan-out: two children of the same parent, each routed by its own port.
	c1 := &NodeRun{FlowRunID: f1.ID, NodeID: "agent", NodeType: NodeAgent, ParentNodeRunID: root.ID, Status: NodeDone, Decision: DecisionEmitted, Ports: []string{"out"}, Stdout: Inline("child1 output")}
	c2 := &NodeRun{FlowRunID: f1.ID, NodeID: "note", NodeType: NodeInbox, ParentNodeRunID: root.ID, Status: NodeDone, Decision: DecisionDropped}
	must(t, s.CreateNodeRun(ctx(), c1))
	must(t, s.CreateNodeRun(ctx(), c2))
	grand := &NodeRun{FlowRunID: f1.ID, NodeID: "after", NodeType: NodeCommand, ParentNodeRunID: c1.ID, Status: NodeFailed, Decision: DecisionError, Reason: "exit 1"}
	must(t, s.CreateNodeRun(ctx(), grand))
	other := mkNodeRun(t, s, f2.ID, "only", NodeCommand, NodeFailed)

	fan, err := s.GetEventFanOut(ctx(), ev.ID)
	must(t, err)
	if fan.Event.Payload.Text != `{"pr":1}` {
		t.Errorf("event payload = %+v", fan.Event.Payload)
	}
	if len(fan.FlowRuns) != 2 || fan.FlowRuns[0].ID != f1.ID || fan.FlowRuns[1].ID != f2.ID {
		t.Fatalf("flow runs = %d (test runs must be excluded, order oldest first)", len(fan.FlowRuns))
	}
	t1 := fan.FlowRuns[0]
	if len(t1.Nodes) != 1 || t1.Nodes[0].ID != root.ID || len(t1.Nodes[0].Children) != 2 {
		t.Fatalf("tree shape wrong: %+v", t1.Nodes)
	}
	kids := t1.Nodes[0].Children
	if kids[0].ID != c1.ID || kids[1].ID != c2.ID || len(kids[0].Children) != 1 || kids[0].Children[0].ID != grand.ID {
		t.Errorf("children wrong: %v %v", kids[0].ID, kids[1].ID)
	}
	if kids[0].Stdout.Text != "" || kids[0].Stdout.Size != int64(len("child1 output")) {
		t.Errorf("tree content should be slim: %+v", kids[0].Stdout)
	}
	if kids[0].Children[0].Decision != DecisionError || kids[0].Children[0].Reason != "exit 1" {
		t.Errorf("decision path lost: %+v", kids[0].Children[0].NodeRun)
	}
	if len(fan.FlowRuns[1].Nodes) != 1 || fan.FlowRuns[1].Nodes[0].ID != other.ID {
		t.Errorf("second flow's tree wrong")
	}

	// Serialises with snake_case names (API surface).
	b, err := json.Marshal(fan)
	must(t, err)
	for _, want := range []string{`"flow_runs"`, `"parent_node_run_id"`, `"children"`, `"node_id"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s", want)
		}
	}
	if _, err := s.GetEventFanOut(ctx(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown event: %v", err)
	}
}

func TestFlowRunListFilters(t *testing.T) {
	s, clk := newTestStore(t)
	e1 := mkEvent(t, s, "gh", "1", "{}")
	e2 := mkEvent(t, s, "slack", "2", "{}")
	r1 := mkFlowRun(t, s, "A", e1.ID, FlowRunSucceeded)
	clk.Advance(time.Hour)
	r2 := mkFlowRun(t, s, "B", e2.ID, FlowRunFailed)
	replay := &FlowRun{FlowID: "A", FlowHash: "h1", EventID: e1.ID, Kind: KindReplay, ReplayOf: r1.ID, DryRun: true, Status: FlowRunSucceeded}
	must(t, s.CreateFlowRun(ctx(), replay))
	test := &FlowRun{FlowID: "A", FlowHash: "h1", Kind: KindTest, Status: FlowRunSucceeded}
	must(t, s.CreateFlowRun(ctx(), test))
	must(t, s.CreateNodeRun(ctx(), &NodeRun{FlowRunID: r1.ID, NodeID: "f", NodeType: NodeCommand, Status: NodeDone, Decision: DecisionDropped}))
	n := &NodeRun{FlowRunID: r2.ID, NodeID: "f", NodeType: NodeCommand, Status: NodeFailed, Decision: DecisionError}
	must(t, s.CreateNodeRun(ctx(), n))

	list := func(f FlowRunFilter) []string {
		t.Helper()
		p, err := s.ListFlowRuns(ctx(), f)
		must(t, err)
		var out []string
		for _, r := range p.Items {
			out = append(out, r.ID)
		}
		return out
	}
	check := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
	check("default hides test runs", list(FlowRunFilter{}), replay.ID, r2.ID, r1.ID)
	check("kind test", list(FlowRunFilter{Kinds: []FlowRunKind{KindTest}}), test.ID)
	check("flow", list(FlowRunFilter{FlowID: "A"}), replay.ID, r1.ID)
	check("sensor via event", list(FlowRunFilter{SensorID: "slack"}), r2.ID)
	check("status", list(FlowRunFilter{Statuses: []FlowRunStatus{FlowRunFailed}}), r2.ID)
	check("decision", list(FlowRunFilter{Decision: DecisionError}), r2.ID)
	check("replay_of", list(FlowRunFilter{ReplayOf: r1.ID}), replay.ID)
	check("time", list(FlowRunFilter{Time: TimeRange{Since: clk.Now()}}), replay.ID, r2.ID)

	got, err := s.GetFlowRun(ctx(), replay.ID)
	must(t, err)
	if got.Kind != KindReplay || got.ReplayOf != r1.ID || !got.DryRun {
		t.Errorf("replay run = %+v", got)
	}
}

func TestFlowRunNeedsStoredVersion(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.CreateFlowRun(ctx(), &FlowRun{FlowID: "F", FlowHash: "unknown"})
	if err == nil {
		t.Fatal("flow run referencing an unstored flow version was accepted")
	}
	created, err := s.PutFlowVersion(ctx(), "F", "abc", "id: F")
	must(t, err)
	again, err := s.PutFlowVersion(ctx(), "F", "abc", "different yaml, same hash")
	must(t, err)
	if !created || again {
		t.Errorf("created=%v again=%v", created, again)
	}
	v, err := s.GetFlowVersion(ctx(), "F", "abc")
	must(t, err)
	if v.YAML != "id: F" {
		t.Errorf("version overwritten: %q", v.YAML)
	}
	must(t, s.CreateFlowRun(ctx(), &FlowRun{FlowID: "F", FlowHash: "abc"}))
}

func TestAgentTranscriptSequenceAndPermissions(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	n := mkNodeRun(t, s, fr.ID, "a", NodeAgent, NodeRunning)
	a1 := mkAgentRun(t, s, n.ID, StylePerTool, AgentRunning)
	a2 := mkAgentRun(t, s, n.ID, StylePerTool, AgentRunning)

	add := func(run, kind, tool, payload string) *AgentEvent {
		ev := &AgentEvent{AgentRunID: run, Kind: kind, ToolCallID: tool, Payload: Inline(payload)}
		must(t, s.AppendAgentEvent(ctx(), ev))
		return ev
	}
	e1 := add(a1.ID, "message", "", `{"t":"hi"}`)
	e2 := add(a1.ID, "tool_call", "tc1", `{"title":"Read"}`)
	f1 := add(a2.ID, "message", "", `{}`)
	e3 := add(a1.ID, "tool_call_update", "tc1", `{"status":"completed"}`)
	if e1.Seq != 1 || e2.Seq != 2 || e3.Seq != 3 || f1.Seq != 1 {
		t.Fatalf("seq per run: %d %d %d / %d", e1.Seq, e2.Seq, e3.Seq, f1.Seq)
	}
	batch := []*AgentEvent{{AgentRunID: a1.ID, Kind: "plan"}, {AgentRunID: a1.ID, Kind: "thought"}}
	must(t, s.AppendAgentEvents(ctx(), batch))
	if batch[0].Seq != 4 || batch[1].Seq != 5 {
		t.Errorf("batch seqs = %d %d", batch[0].Seq, batch[1].Seq)
	}
	if err := s.AppendAgentEvent(ctx(), &AgentEvent{AgentRunID: a1.ID, Kind: "x", Seq: 2}); err == nil {
		t.Error("duplicate seq accepted")
	}
	evs, err := s.ListAgentEvents(ctx(), AgentEventFilter{AgentRunID: a1.ID, AfterSeq: 1, Limit: 2})
	must(t, err)
	if len(evs) != 2 || evs[0].Seq != 2 || evs[1].Seq != 3 || evs[0].Payload.Text != `{"title":"Read"}` {
		t.Errorf("after seq 1: %+v", evs)
	}
	evs, err = s.ListAgentEvents(ctx(), AgentEventFilter{AgentRunID: a1.ID, ToolCallID: "tc1"})
	must(t, err)
	if len(evs) != 2 {
		t.Errorf("tool call filter returned %d", len(evs))
	}
	evs, err = s.ListAgentEvents(ctx(), AgentEventFilter{AgentRunID: a1.ID, Kinds: []string{"plan", "thought"}})
	must(t, err)
	if len(evs) != 2 {
		t.Errorf("kind filter returned %d", len(evs))
	}

	// Policy-decided permission request recorded at creation; user-decided later.
	now := s.Now()
	byPolicy := &PermissionRequest{AgentRunID: a1.ID, ToolCallID: "tc1", ToolKind: "read", Title: "Read file", Options: json.RawMessage(`[{"id":"allow"}]`),
		Decision: PermAllow, DecidedBy: DecidedByPolicy, MatchedRule: "read-class", DecidedAt: &now}
	must(t, s.CreatePermissionRequest(ctx(), byPolicy))
	pending := &PermissionRequest{AgentRunID: a1.ID, ToolCallID: "tc2", ToolKind: "edit", RawInput: Inline(`{"path":"x"}`)}
	must(t, s.CreatePermissionRequest(ctx(), pending))
	un, err := s.ListPermissionRequests(ctx(), PermissionRequestFilter{AgentRunID: a1.ID, Undecided: true})
	must(t, err)
	if len(un.Items) != 1 || un.Items[0].ID != pending.ID || un.Items[0].RawInput.Text != `{"path":"x"}` {
		t.Errorf("undecided = %+v", un.Items)
	}
	got, err := s.GetPermissionRequest(ctx(), byPolicy.ID)
	must(t, err)
	if got.DecidedBy != DecidedByPolicy || got.MatchedRule != "read-class" || string(got.Options) != `[{"id":"allow"}]` {
		t.Errorf("policy decision lost: %+v", got)
	}
	// A decided request cannot be flipped.
	got.Decision = PermReject
	if err := s.UpdatePermissionRequest(ctx(), got); !errors.Is(err, ErrConflict) {
		t.Errorf("re-deciding a decided request: %v, want ErrConflict", err)
	}
	pending.Decision, pending.DecidedBy, pending.DecidedAt = PermReject, DecidedByPolicy, &now
	pending.Reason = "edit is not read-class"
	must(t, s.UpdatePermissionRequest(ctx(), pending))
	un, _ = s.ListPermissionRequests(ctx(), PermissionRequestFilter{AgentRunID: a1.ID, Undecided: true})
	if len(un.Items) != 0 {
		t.Error("request still undecided after update")
	}
	if err := s.CreateApproval(ctx(), &Approval{AgentRunID: a1.ID, Type: ApprovalToolCall}); !errors.Is(err, ErrInvalid) {
		t.Errorf("tool_call approval without permission request: %v", err)
	}
}

func TestInboxFlagsFilterAndLinks(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "F", "", FlowRunRunning)
	n := mkNodeRun(t, s, fr.ID, "note", NodeInbox, NodeDone)
	mk := func(title string, p Priority, dry bool) *InboxItem {
		it := &InboxItem{SourceType: SourceNodeRun, NodeRunID: n.ID, Title: title, Priority: p, DryRun: dry, SummaryMD: "details of " + title,
			Links: []InboxLink{{Label: "PR", URL: "https://example.com/pr/1"}}}
		must(t, s.CreateInboxItem(ctx(), it))
		return it
	}
	a := mk("Build broke", PriorityHigh, false)
	b := mk("FYI release", PriorityLow, false)
	c := mk("Dry thing", "", true)
	if c.Priority != PriorityNormal || a.FlowRunID != fr.ID {
		t.Errorf("defaults/links: prio=%s flow_run=%s", c.Priority, a.FlowRunID)
	}

	yes := true
	got, err := s.SetInboxFlags(ctx(), a.ID, InboxFlags{Read: &yes, Starred: &yes})
	must(t, err)
	if !got.Read || !got.Starred || got.Archived {
		t.Errorf("flags = %+v", got)
	}
	// Unset flags are untouched.
	no := false
	got, err = s.SetInboxFlags(ctx(), a.ID, InboxFlags{Read: &no})
	must(t, err)
	if got.Read || !got.Starred {
		t.Errorf("partial flag update clobbered others: %+v", got)
	}

	list := func(f InboxFilter) int {
		p, err := s.ListInbox(ctx(), f)
		must(t, err)
		return len(p.Items)
	}
	if list(InboxFilter{Read: &no}) != 3 || list(InboxFilter{Starred: &yes}) != 1 || list(InboxFilter{DryRun: &yes}) != 1 {
		t.Error("flag filters wrong")
	}
	if list(InboxFilter{Priorities: []Priority{PriorityHigh, PriorityLow}}) != 2 || list(InboxFilter{Search: "RELEASE"}) != 1 || list(InboxFilter{Search: "details of"}) != 3 {
		t.Error("priority/search filters wrong")
	}
	full, err := s.GetInboxItem(ctx(), b.ID)
	must(t, err)
	if len(full.Links) != 1 || full.Links[0].URL != "https://example.com/pr/1" {
		t.Errorf("links = %+v", full.Links)
	}
	if _, err := s.SetInboxFlags(ctx(), "missing", InboxFlags{Read: &yes}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing item: %v", err)
	}
	if err := s.CreateInboxItem(ctx(), &InboxItem{SourceType: SourceAgentRun, NodeRunID: n.ID, Title: "mismatch"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("source type/id mismatch: %v", err)
	}
}

func TestSensorRunsStateAndKillSwitch(t *testing.T) {
	s, clk := newTestStore(t)

	st, err := s.GetSensorState(ctx(), "gh")
	must(t, err)
	if st.LastSuccessAt != nil {
		t.Fatal("fresh sensor has a last success")
	}
	okAt := clk.Now()
	must(t, s.RecordSensorOutcome(ctx(), "gh", "r1", okAt, true, ""))
	clk.Advance(time.Minute)
	must(t, s.RecordSensorOutcome(ctx(), "gh", "r2", clk.Now(), false, "exit 1"))
	must(t, s.RecordSensorOutcome(ctx(), "gh", "r3", clk.Now().Add(time.Second), false, "exit 2"))
	st, err = s.GetSensorState(ctx(), "gh")
	must(t, err)
	if st.LastSuccessAt == nil || !st.LastSuccessAt.Equal(okAt) {
		t.Errorf("a failed poll advanced last_success_at: %v", st.LastSuccessAt)
	}
	if st.ConsecutiveFailures != 2 || st.LastError != "exit 2" || st.LastRunID != "r3" {
		t.Errorf("state = %+v", st)
	}
	must(t, s.RecordSensorOutcome(ctx(), "gh", "r4", clk.Now(), true, ""))
	st, _ = s.GetSensorState(ctx(), "gh")
	if st.ConsecutiveFailures != 0 || st.LastError != "" || !st.LastSuccessAt.Equal(clk.Now()) {
		t.Errorf("success did not reset: %+v", st)
	}

	code := 0
	run := &SensorRun{SensorID: "gh", Origin: OriginManual, Stdout: Inline(`[{"id":1}]`)}
	must(t, s.CreateSensorRun(ctx(), run))
	end := clk.Now()
	run.Status, run.EndedAt, run.ExitCode, run.ItemCount, run.NewCount = SensorSucceeded, &end, &code, 1, 1
	must(t, s.UpdateSensorRun(ctx(), run))
	got, err := s.GetSensorRun(ctx(), run.ID)
	must(t, err)
	if got.Status != SensorSucceeded || got.ExitCode == nil || *got.ExitCode != 0 || got.Stdout.Text != `[{"id":1}]` || got.Origin != OriginManual {
		t.Errorf("sensor run = %+v", got)
	}
	p, err := s.ListSensorRuns(ctx(), SensorRunFilter{SensorID: "gh", Status: SensorSucceeded})
	must(t, err)
	if len(p.Items) != 1 {
		t.Errorf("listed %d", len(p.Items))
	}

	// Kill switch persists across reopen.
	ks, err := s.GetKillSwitch(ctx())
	must(t, err)
	if ks.Paused {
		t.Fatal("kill switch starts engaged")
	}
	must(t, s.SetKillSwitch(ctx(), KillSwitch{Paused: true, KillRunning: true, By: "me"}))
	path := s.Path()
	must(t, s.Close())
	s2, err := Open(path)
	must(t, err)
	defer func() { _ = s2.Close() }()
	ks, err = s2.GetKillSwitch(ctx())
	must(t, err)
	if !ks.Paused || !ks.KillRunning || ks.By != "me" || ks.Since.IsZero() {
		t.Errorf("kill switch after reopen = %+v", ks)
	}
	if _, err := s2.GetSetting(ctx(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing setting: %v", err)
	}
	if err := s2.SetSetting(ctx(), "bad", json.RawMessage(`{not json`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid JSON setting: %v", err)
	}
}

func TestSessionsAuthEventsAndPasskeys(t *testing.T) {
	s, clk := newTestStore(t)
	sess := &Session{ID: "hash-1", Principal: "me", Modules: []string{"password"}, Services: []AuthService{ServiceUI, ServiceAPI},
		Host: "localhost:9001", RemoteAddr: "127.0.0.1", ExpiresAt: clk.Now().Add(7 * 24 * time.Hour)}
	must(t, s.CreateSession(ctx(), sess))
	expired := &Session{ID: "hash-2", Principal: "me", ExpiresAt: clk.Now().Add(-time.Hour)}
	must(t, s.CreateSession(ctx(), expired))
	revoked := &Session{ID: "hash-3", Principal: "me", ExpiresAt: clk.Now().Add(time.Hour)}
	must(t, s.CreateSession(ctx(), revoked))
	must(t, s.RevokeSession(ctx(), revoked.ID))

	active, err := s.ListSessions(ctx(), clk.Now())
	must(t, err)
	if len(active) != 1 || active[0].ID != "hash-1" || len(active[0].Services) != 2 || active[0].Host != "localhost:9001" {
		t.Fatalf("active sessions = %+v", active)
	}
	all, err := s.ListSessions(ctx(), time.Time{})
	must(t, err)
	if len(all) != 3 {
		t.Errorf("all sessions = %d", len(all))
	}
	// Sliding expiry.
	sess.LastSeenAt, sess.ExpiresAt = clk.Now().Add(time.Hour), clk.Now().Add(8*24*time.Hour)
	must(t, s.UpdateSession(ctx(), sess))
	got, err := s.GetSession(ctx(), "hash-1")
	must(t, err)
	if !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Error("expiry not extended")
	}
	first := mustGetSession(t, s, revoked.ID).RevokedAt
	clk.Advance(time.Hour)
	must(t, s.RevokeSession(ctx(), revoked.ID))
	if !mustGetSession(t, s, revoked.ID).RevokedAt.Equal(*first) {
		t.Error("re-revoking changed the revocation time")
	}
	n, err := s.RevokeAllSessions(ctx())
	must(t, err)
	if n != 2 { // hash-1 and hash-2; hash-3 was already revoked
		t.Errorf("revoked %d, want 2", n)
	}
	if left, _ := s.ListSessions(ctx(), clk.Now()); len(left) != 0 {
		t.Errorf("%d sessions still active", len(left))
	}
	if _, err := s.GetSession(ctx(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing session: %v", err)
	}

	must(t, s.CreateAuthEvent(ctx(), &AuthEvent{Service: ServiceAdmin, Endpoint: "PUT /api/flows/x", Outcome: "denied", RemoteAddr: "100.64.0.1", Host: "mac.ts.net",
		Results: []ModuleResult{{Module: "localhost-trust", Control: "sufficient", Result: "ignore"}, {Module: "password", Control: "sufficient", Result: "fail", Reason: "bad password"}}}))
	must(t, s.CreateAuthEvent(ctx(), &AuthEvent{Action: "login", Outcome: "success", Principal: "me", SessionID: "hash-1"}))
	evs, err := s.ListAuthEvents(ctx(), AuthEventFilter{Service: ServiceAdmin})
	must(t, err)
	if len(evs.Items) != 1 || len(evs.Items[0].Results) != 2 || evs.Items[0].Results[1].Reason != "bad password" {
		t.Errorf("auth events = %+v", evs.Items)
	}
	evs, err = s.ListAuthEvents(ctx(), AuthEventFilter{SessionID: "hash-1"})
	must(t, err)
	if len(evs.Items) != 1 || evs.Items[0].Action != "login" || evs.Items[0].Results == nil {
		t.Errorf("by session = %+v", evs.Items)
	}

	cred := &WebAuthnCredential{ID: "cred-1", RPID: "localhost", Principal: "me", PublicKey: []byte{1, 2, 3}, Label: "MacBook", Data: json.RawMessage(`{"aaguid":"x"}`)}
	must(t, s.CreateWebAuthnCredential(ctx(), cred))
	must(t, s.CreateWebAuthnCredential(ctx(), &WebAuthnCredential{ID: "cred-2", RPID: "mac.ts.net", PublicKey: []byte{9}}))
	if err := s.CreateWebAuthnCredential(ctx(), &WebAuthnCredential{ID: "cred-1", RPID: "x", PublicKey: []byte{1}}); err == nil {
		t.Error("duplicate credential id accepted")
	}
	local, err := s.ListWebAuthnCredentials(ctx(), "localhost")
	must(t, err)
	if len(local) != 1 || local[0].ID != "cred-1" || string(local[0].Data) != `{"aaguid":"x"}` {
		t.Errorf("credentials for localhost = %+v", local)
	}
	if all, _ := s.ListWebAuthnCredentials(ctx(), ""); len(all) != 2 {
		t.Errorf("all credentials = %d", len(all))
	}
	used := clk.Now()
	cred.SignCount, cred.LastUsedAt = 7, &used
	must(t, s.UpdateWebAuthnCredential(ctx(), cred))
	g, err := s.GetWebAuthnCredential(ctx(), "cred-1")
	must(t, err)
	if g.SignCount != 7 || g.LastUsedAt == nil || string(g.PublicKey) != "\x01\x02\x03" {
		t.Errorf("credential = %+v", g)
	}
	must(t, s.DeleteWebAuthnCredential(ctx(), "cred-1"))
	if err := s.DeleteWebAuthnCredential(ctx(), "cred-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func mustGetSession(t *testing.T, s *Store, id string) *Session {
	t.Helper()
	ss, err := s.GetSession(ctx(), id)
	must(t, err)
	return ss
}
