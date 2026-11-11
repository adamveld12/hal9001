package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/store"
)

// convRun is the agent run of a flow node with given times and status; prev links it behind a propose-phase run.
func (e *env) convRun(nodeRunID, prev string, status store.AgentStatus, prompt, result string, at time.Time) *store.AgentRun {
	e.t.Helper()
	end := at.Add(time.Minute)
	ar := &store.AgentRun{
		NodeRunID: nodeRunID, PrevAgentRunID: prev, Runtime: "claude", Model: "sonnet", Mode: store.ModeAuto, Policy: store.PolicyAuto, Phase: store.PhaseExecute,
		Status: status, Prompt: store.Inline(prompt), Result: store.Inline(result), CreatedAt: at, StartedAt: &at,
	}
	if status != store.AgentRunning && status != store.AgentAwaitingApproval && status != store.AgentQueued {
		ar.EndedAt = &end
	}
	must(e.t, e.st.CreateAgentRun(ctx(), ar))
	return ar
}

func (e *env) convChat(kind, title, status string, at time.Time, texts ...string) *store.AssistSession {
	e.t.Helper()
	a := &store.AssistSession{
		TargetKind: kind, Runtime: "claude", Status: store.AssistStatus(status), Title: title, CreatedAt: at,
		DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
	}
	must(e.t, e.st.CreateAssistSession(ctx(), a))
	for i, t := range texts {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		must(e.t, e.st.CreateAssistMessage(ctx(), &store.AssistMessage{SessionID: a.ID, Role: role, Text: &t}))
	}
	return a
}

type convItem struct {
	ID              string  `json:"id"`
	Source          string  `json:"source"`
	Kind            string  `json:"kind"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	Running         bool    `json:"running"`
	Continuable     bool    `json:"continuable"`
	Runtime         string  `json:"runtime"`
	Model           *string `json:"model"`
	FlowID          *string `json:"flow_id"`
	FlowRunID       *string `json:"flow_run_id"`
	NodeID          *string `json:"node_id"`
	NodeRunID       *string `json:"node_run_id"`
	AgentRunID      *string `json:"agent_run_id"`
	AssistSessionID *string `json:"assist_session_id"`
	ForkedFrom      *struct {
		AgentRunID string  `json:"agent_run_id"`
		FlowID     *string `json:"flow_id"`
		FlowRunID  *string `json:"flow_run_id"`
		NodeID     *string `json:"node_id"`
	} `json:"forked_from"`
	ForkContext *string    `json:"fork_context"`
	ForkCount   int        `json:"fork_count"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	EndedAt     *time.Time `json:"ended_at"`
	Preview     string     `json:"preview"`
	Forks       []convItem `json:"forks"`
}

func (e *env) convList(query string) []convItem {
	e.t.Helper()
	var page struct {
		Items []convItem `json:"items"`
	}
	e.get("/api/conversations" + query).want(200).decode(&page)
	return page.Items
}

func convIDs(items []convItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func sameSet(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	g, w := slices.Clone(got), slices.Clone(want)
	slices.Sort(g)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// fixture: two flow conversations (one with a propose and an execute phase), a flow run that is still running, an
// assist turn, and chats of several kinds.
type convFixture struct {
	t0                           time.Time
	proposeRun, execRun, doneRun *store.AgentRun
	runningRun, turnRun          *store.AgentRun
	general, draft, closed       *store.AssistSession
}

func (e *env) convFixture() convFixture {
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var f convFixture
	f.t0 = t0
	fr1 := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	nr1 := e.nodeRun(fr1.ID, "review", store.NodeDone, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	f.proposeRun = e.convRun(nr1.ID, "", store.AgentSucceeded, "Review pull request 7", "plan: rebase first", t0)
	f.execRun = e.convRun(nr1.ID, f.proposeRun.ID, store.AgentSucceeded, "Approved. Execute the plan.", `{"final_message":"Rebased and merged PR seven."}`, t0.Add(2*time.Minute))
	fr2 := e.flowRun("other", "", store.KindLive, store.FlowRunSucceeded)
	nr2 := e.nodeRun(fr2.ID, "triage", store.NodeDone, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	f.doneRun = e.convRun(nr2.ID, "", store.AgentFailed, "Triage the inbox", "", t0.Add(10*time.Minute))
	fr3 := e.flowRun("demo", "", store.KindLive, store.FlowRunRunning)
	nr3 := e.nodeRun(fr3.ID, "review", store.NodeRunning, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	f.runningRun = e.convRun(nr3.ID, "", store.AgentRunning, "Review pull request 8", "", t0.Add(30*time.Minute))

	f.general = e.convChat(store.AssistTargetGeneral, "why did the deploy fail?", "idle", t0.Add(5*time.Minute), "why did the deploy fail?", "The lint step failed on main.")
	f.draft = e.convChat(store.AssistTargetSensor, "watch the queue", "failed", t0.Add(15*time.Minute), "watch the queue")
	f.closed = e.convChat(store.AssistTargetFlow, "old flow draft", "closed", t0.Add(20*time.Minute), "old flow draft")
	// A turn of a chat is an agent run with an assist session: never a conversation of its own.
	f.turnRun = &store.AgentRun{AssistSessionID: f.general.ID, Runtime: "claude", Mode: store.ModeApprove, ApproveStyle: store.StylePerTool, Policy: store.PolicyGated, Phase: store.PhaseExecute, Status: store.AgentSucceeded, Prompt: store.Inline("why did the deploy fail?")}
	must(e.t, e.st.CreateAgentRun(ctx(), f.turnRun))
	return f
}

func TestConversationsListMixesChatsAndFlowRuns(t *testing.T) {
	e := newEnv(t)
	f := e.convFixture()

	items := e.convList("")
	// Newest activity first; the propose phase is part of the execute phase's conversation and the assist turn is a turn.
	want := []string{f.runningRun.ID, f.closed.ID, f.draft.ID, f.doneRun.ID, f.general.ID, f.execRun.ID}
	if got := convIDs(items); !slices.Equal(got, want) {
		t.Fatalf("conversations = %v, want %v (propose %s, turn %s must not be listed)", got, want, f.proposeRun.ID, f.turnRun.ID)
	}
	byID := map[string]convItem{}
	for _, it := range items {
		byID[it.ID] = it
	}

	run := byID[f.execRun.ID]
	if run.Source != "flow" || run.Kind != "agent" || run.Status != "succeeded" || run.Running || !run.Continuable || run.Title != "demo / review" || run.Runtime != "claude" ||
		run.FlowID == nil || *run.FlowID != "demo" || run.NodeID == nil || *run.NodeID != "review" || run.AgentRunID == nil || *run.AgentRunID != f.execRun.ID ||
		run.AssistSessionID != nil || run.ForkedFrom != nil || run.Preview != "Rebased and merged PR seven." || run.EndedAt == nil {
		t.Errorf("flow conversation = %+v", run)
	}
	if r := byID[f.runningRun.ID]; r.Status != "running" || !r.Running || r.Continuable || r.EndedAt != nil {
		t.Errorf("running run = %+v", r)
	}
	if r := byID[f.doneRun.ID]; r.Status != "failed" || r.Running || !r.Continuable || r.Preview != "Triage the inbox" {
		t.Errorf("failed run = %+v", r)
	}
	chat := byID[f.general.ID]
	if chat.Source != "chat" || chat.Kind != "general" || chat.Title != "why did the deploy fail?" || chat.Status != "idle" || !chat.Continuable || chat.AgentRunID != nil ||
		chat.AssistSessionID == nil || *chat.AssistSessionID != f.general.ID || chat.Preview != "The lint step failed on main." {
		t.Errorf("chat = %+v", chat)
	}
	if c := byID[f.closed.ID]; c.Status != "closed" || c.Continuable || c.Running || c.Kind != "flow" {
		t.Errorf("closed chat = %+v", c)
	}
	if c := byID[f.draft.ID]; c.Status != "failed" || !c.Continuable {
		t.Errorf("failed chat = %+v", c)
	}
}

func TestConversationsFilters(t *testing.T) {
	e := newEnv(t)
	f := e.convFixture()
	ids := func(query string) []string { e.t.Helper(); return convIDs(e.convList(query)) }

	sameSet(t, "source=flow", ids("?source=flow"), f.runningRun.ID, f.doneRun.ID, f.execRun.ID)
	sameSet(t, "source=chat", ids("?source=chat"), f.general.ID, f.draft.ID, f.closed.ID)
	sameSet(t, "flow=demo", ids("?flow=demo"), f.runningRun.ID, f.execRun.ID)
	sameSet(t, "node=triage", ids("?node=triage"), f.doneRun.ID)
	sameSet(t, "runtime", ids("?runtime=claude"), f.runningRun.ID, f.closed.ID, f.draft.ID, f.doneRun.ID, f.general.ID, f.execRun.ID)
	sameSet(t, "runtime=other", ids("?runtime=codex"))
	sameSet(t, "status=running", ids("?status=running"), f.runningRun.ID)
	sameSet(t, "status=failed (a run and a chat)", ids("?status=failed"), f.doneRun.ID, f.draft.ID)
	sameSet(t, "status=closed", ids("?status=closed"), f.closed.ID)
	sameSet(t, "status=idle", ids("?status=idle"), f.general.ID)
	sameSet(t, "status=succeeded", ids("?status=succeeded"), f.execRun.ID)
	sameSet(t, "kind=general", ids("?kind=general"), f.general.ID)
	sameSet(t, "kind=agent", ids("?kind=agent"), f.runningRun.ID, f.doneRun.ID, f.execRun.ID)
	sameSet(t, "kind=flow (a chat that drafts a flow)", ids("?kind=flow"), f.closed.ID)
	sameSet(t, "flow_run", ids("?flow_run="+*e.convGet(f.execRun.ID).FlowRunID), f.execRun.ID)

	since := f.t0.Add(14 * time.Minute).Format(time.RFC3339Nano)
	until := f.t0.Add(21 * time.Minute).Format(time.RFC3339Nano)
	sameSet(t, "since", ids("?since="+url.QueryEscape(since)), f.runningRun.ID, f.closed.ID, f.draft.ID)
	sameSet(t, "since+until", ids("?since="+url.QueryEscape(since)+"&until="+url.QueryEscape(until)), f.closed.ID, f.draft.ID)

	// q: chat titles and message text, the prompt and result text of runs (also of a propose phase), ids, flow ids; no
	// case sensitivity; LIKE metacharacters are literal.
	sameSet(t, "q in a chat message", ids("?q=lint+step"), f.general.ID)
	sameSet(t, "q in a chat message, upper case", ids("?q=LINT+STEP"), f.general.ID)
	sameSet(t, "q in a prompt", ids("?q=pull+request+8"), f.runningRun.ID)
	sameSet(t, "q in the propose phase's prompt", ids("?q=pull+request+7"), f.execRun.ID)
	sameSet(t, "q in a result", ids("?q=rebased"), f.execRun.ID)
	sameSet(t, "q in a flow id", ids("?q=other"), f.doneRun.ID)
	sameSet(t, "q is an id", ids("?q="+f.draft.ID), f.draft.ID)
	sameSet(t, "q with a percent sign matches nothing", ids("?q=%25"))
	sameSet(t, "q with an underscore is literal (r_b would match rebased)", ids("?q=r_b"))
	sameSet(t, "q and source", ids("?q=lint&source=flow"))

	for _, bad := range []string{"?source=x", "?status=queued", "?flow=a%20b", "?since=yesterday", "?limit=0", "?cursor=nope", "?forked_from=x", "?q=" + strings.Repeat("a", 201)} {
		e.get("/api/conversations" + bad).want(400)
	}
}

func (e *env) convGet(id string) convItem {
	e.t.Helper()
	var it convItem
	e.get("/api/conversations/" + id).want(200).decode(&it)
	return it
}

// Chats and runs that share one timestamp keep a stable order across pages: nothing is skipped or repeated.
func TestConversationsCursorIsStableUnderEqualTimestamps(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var want []string
	for i := range 7 {
		want = append(want, e.convChat(store.AssistTargetGeneral, fmt.Sprintf("chat %d", i), "idle", at).ID)
	}
	fr := e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	for i := range 3 {
		nr := e.nodeRun(fr.ID, fmt.Sprintf("n%d", i), store.NodeDone, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
		want = append(want, e.convRun(nr.ID, "", store.AgentSucceeded, "p", "", at.Add(-time.Minute)).ID) // activity = end = at
	}
	slices.Sort(want)
	slices.Reverse(want) // the order within one timestamp is id descending
	got, pages := e.walk("/api/conversations", 3)
	if !slices.Equal(got, want) || pages != 4 {
		t.Errorf("paged ids = %v in %d pages, want %v in 4", got, pages, want)
	}
	all := convIDs(e.convList("?limit=200"))
	if !slices.Equal(all, want) {
		t.Errorf("one page = %v, want %v", all, want)
	}
}

func TestConversationForksAreListedAndCounted(t *testing.T) {
	e := newEnv(t)
	f := e.convFixture()
	t1 := f.t0.Add(40 * time.Minute)
	mkFork := func(from string, at time.Time, ctxKind string) *store.AssistSession {
		a := &store.AssistSession{
			TargetKind: store.AssistTargetGeneral, Runtime: "claude", Status: store.AssistIdle, CreatedAt: at, DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
			ForkedFromAgentRunID: from, ForkContext: ctxKind,
		}
		must(t, e.st.CreateAssistSession(ctx(), a))
		return a
	}
	older := mkFork(f.proposeRun.ID, t1, "") // a fork of the propose phase belongs to the same conversation
	newer := mkFork(f.execRun.ID, t1.Add(time.Minute), store.ForkResumed)
	mkFork(f.doneRun.ID, t1.Add(2*time.Minute), store.ForkReplayed)

	sameSet(t, "forked_from the execute phase", convIDs(e.convList("?forked_from="+f.execRun.ID)), older.ID, newer.ID)
	sameSet(t, "forked_from the propose phase", convIDs(e.convList("?forked_from="+f.proposeRun.ID)), older.ID, newer.ID)

	got := e.convGet(f.execRun.ID)
	if got.ForkCount != 2 || len(got.Forks) != 2 || got.Forks[0].ID != newer.ID || got.Forks[1].ID != older.ID {
		t.Errorf("conversation = count %d, forks %v; want the newest fork first", got.ForkCount, convIDs(got.Forks))
	}
	fk := got.Forks[0]
	if fk.Source != "chat" || fk.ForkedFrom == nil || fk.ForkedFrom.AgentRunID != f.execRun.ID || fk.ForkedFrom.FlowID == nil || *fk.ForkedFrom.FlowID != "demo" ||
		fk.ForkedFrom.NodeID == nil || *fk.ForkedFrom.NodeID != "review" || fk.FlowID == nil || *fk.FlowID != "demo" || fk.ForkContext == nil || *fk.ForkContext != "resumed" ||
		fk.Title != "Continued: demo / review" {
		t.Errorf("fork = %+v", fk)
	}
	// The propose phase's id resolves to the conversation of the execute phase.
	if p := e.convGet(f.proposeRun.ID); p.ID != f.execRun.ID {
		t.Errorf("propose phase resolves to %s, want %s", p.ID, f.execRun.ID)
	}
	// The list carries the counts too; forks of a chat do not exist.
	for _, it := range e.convList("?source=flow") {
		wantCount := map[string]int{f.execRun.ID: 2, f.doneRun.ID: 1}[it.ID]
		if it.ForkCount != wantCount {
			t.Errorf("fork_count of %s = %d, want %d", it.ID, it.ForkCount, wantCount)
		}
	}
	if c := e.convGet(f.general.ID); len(c.Forks) != 0 || c.Source != "chat" {
		t.Errorf("chat detail = %+v", c)
	}
	// An assist turn and unknown ids are not conversations.
	e.get("/api/conversations/" + f.turnRun.ID).want(404)
	e.get("/api/conversations/01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(404)
	e.get("/api/conversations/01JC7Z3K9Q8V5T2M4N6P8R0S1T/messages").want(404)
}

type convMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Kind       string          `json:"kind"`
	Text       *string         `json:"text"`
	AgentRunID *string         `json:"agent_run_id"`
	ToolCall   json.RawMessage `json:"tool_call"`
	ApprovalID *string         `json:"approval_id"`
}

func (e *env) convMessages(id string) (items []convMessage, truncated bool) {
	e.t.Helper()
	var out struct {
		Items     []convMessage `json:"items"`
		Truncated bool          `json:"truncated"`
	}
	e.get("/api/conversations/" + id + "/messages").want(200).decode(&out)
	return out.Items, out.Truncated
}

func TestConversationMessagesOfAChatKeepDividersAndNotices(t *testing.T) {
	e := newEnv(t)
	a := &store.AssistSession{
		TargetKind: store.AssistTargetGeneral, Runtime: "claude", Status: store.AssistIdle, DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
		ForkedFromAgentRunID: "01JC7Z3K9Q8V5T2M4N6P8R0S1T",
	}
	must(t, e.st.CreateAssistSession(ctx(), a))
	add := func(role, kind, text string) {
		must(t, e.st.CreateAssistMessage(ctx(), &store.AssistMessage{SessionID: a.ID, Role: role, Kind: kind, Text: &text}))
	}
	add("system", store.MessageDivider, "Continued from run 01JC7Z3K9Q8V5T2M4N6P8R0S1T; session resumed")
	add("user", "", "go on")
	add("system", store.MessageNotice, "The sandbox was recreated.")
	must(t, e.st.CreateAssistMessage(ctx(), &store.AssistMessage{
		SessionID: a.ID, Role: "tool", AgentRunID: "01JC7Z3K9Q8V5T2M4N6P8R0S1V", ApprovalID: "01JC7Z3K9Q8V5T2M4N6P8R0S1W",
		ToolCall: json.RawMessage(`{"tool_call_id":"t1","title":"shell","kind":"execute","status":"completed","raw_input":{"command":"ls"},"content":[]}`),
	}))

	msgs, truncated := e.convMessages(a.ID)
	if truncated || len(msgs) != 4 {
		t.Fatalf("messages = %+v truncated=%v", msgs, truncated)
	}
	if m := msgs[0]; m.Role != "system" || m.Kind != "divider" || !strings.Contains(*m.Text, "session resumed") {
		t.Errorf("divider = %+v", m)
	}
	if m := msgs[2]; m.Kind != "notice" || m.Role != "system" || *m.Text != "The sandbox was recreated." {
		t.Errorf("notice = %+v", m)
	}
	if m := msgs[1]; m.Kind != "" || m.Role != "user" || m.Text == nil || *m.Text != "go on" {
		t.Errorf("user message = %+v", m)
	}
	if m := msgs[3]; m.Role != "tool" || m.Text != nil || m.ApprovalID == nil || !strings.Contains(string(m.ToolCall), `"command":"ls"`) {
		t.Errorf("tool message = %+v", m)
	}
	// The assist endpoint carries the kind too.
	var detail struct {
		Messages []struct {
			Kind string `json:"kind"`
		} `json:"messages"`
		ForkedFrom *string `json:"forked_from_agent_run_id"`
	}
	e.get("/api/assist/sessions/" + a.ID).want(200).decode(&detail)
	if len(detail.Messages) != 4 || detail.Messages[0].Kind != "divider" || detail.ForkedFrom == nil || *detail.ForkedFrom != "01JC7Z3K9Q8V5T2M4N6P8R0S1T" {
		t.Errorf("assist session = %+v", detail)
	}
}

func TestConversationMessagesOfAFlowRunFoldTheTranscript(t *testing.T) {
	e := newEnv(t)
	f := e.convFixture()
	run := f.execRun
	events := []struct {
		kind, tool, payload string
	}{
		{"message", "", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Looking at "}}`},
		{"message", "", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"the branch."}}`},
		{"thought", "", `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hmm"}}`},
		{"tool_call", "c1", `{"sessionUpdate":"tool_call","toolCallId":"c1","title":"git rebase","kind":"execute","status":"pending","rawInput":{"command":"git rebase main"}}`},
		{"tool_call_update", "c1", `{"sessionUpdate":"tool_call_update","toolCallId":"c1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"rebased 3 commits"}}]}`},
		{"tool_call", "c2", `{"sessionUpdate":"tool_call","toolCallId":"c2","title":"hal tool: emit_out","kind":"other","status":"pending","rawInput":{}}`},
		{"tool_call_update", "c2", `{"sessionUpdate":"tool_call_update","toolCallId":"c2","status":"failed","rawOutput":{"error":"denied"}}`},
		{"plan", "", `{"sessionUpdate":"plan","entries":[]}`},
		{"message", "", `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Done."}}`},
	}
	for _, ev := range events {
		must(t, e.st.AppendAgentEvent(ctx(), &store.AgentEvent{AgentRunID: run.ID, Kind: ev.kind, ToolCallID: ev.tool, Payload: store.Inline(ev.payload)}))
	}
	perm := &store.PermissionRequest{AgentRunID: run.ID, ToolCallID: "c2", ToolKind: "other", Title: "hal tool: emit_out"}
	must(t, e.st.CreatePermissionRequest(ctx(), perm))
	appr := &store.Approval{AgentRunID: run.ID, Type: store.ApprovalToolCall, PermissionRequestID: perm.ID}
	must(t, e.st.CreateApproval(ctx(), appr))
	// The propose phase's proposal approval and its events come first.
	must(t, e.st.AppendAgentEvent(ctx(), &store.AgentEvent{AgentRunID: f.proposeRun.ID, Kind: "message", Payload: store.Inline(`{"content":{"type":"text","text":"Plan: rebase first."}}`)}))
	prop := &store.Approval{AgentRunID: f.proposeRun.ID, Type: store.ApprovalProposal, Proposal: store.Inline("Plan: rebase first.")}
	must(t, e.st.CreateApproval(ctx(), prop))
	if _, err := e.st.DecideApproval(ctx(), prop.ID, store.ApprovalDecision{Status: store.ApprovalApproved, Comment: "go ahead", DecidedBy: "me"}); err != nil {
		t.Fatal(err)
	}

	msgs, truncated := e.convMessages(f.execRun.ID)
	if truncated {
		t.Error("truncated")
	}
	type shape struct{ role, text string }
	var got []shape
	for _, m := range msgs {
		txt := ""
		if m.Text != nil {
			txt = *m.Text
		}
		if m.Role == "tool" {
			var c struct{ Title, Status string }
			_ = json.Unmarshal(m.ToolCall, &c)
			txt = c.Title + "|" + c.Status
		}
		got = append(got, shape{m.Role, txt})
	}
	want := []shape{
		{"user", "Review pull request 7"},
		{"assistant", "Plan: rebase first."},
		{"system", "The proposal was approved. Comment: go ahead"},
		{"user", "Approved. Execute the plan."},
		{"assistant", "Looking at the branch."},
		{"tool", "git rebase|completed"},
		{"tool", "hal tool: emit_out|failed"},
		{"assistant", "Done."},
		{"system", "Run finished."},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("transcript:\n got %v\nwant %v", got, want)
	}
	// ids: <agent run>:<zero padded seq>, in order; the thought and plan events took seq 3 and 8.
	if msgs[0].ID != f.proposeRun.ID+":00000000" || msgs[4].ID != run.ID+":00000001" || msgs[5].ID != run.ID+":00000004" || msgs[7].ID != run.ID+":00000009" {
		t.Errorf("ids = %s %s %s %s", msgs[0].ID, msgs[4].ID, msgs[5].ID, msgs[7].ID)
	}
	var c1, c2 struct {
		RawInput json.RawMessage   `json:"raw_input"`
		Content  []json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(msgs[5].ToolCall, &c1)
	_ = json.Unmarshal(msgs[6].ToolCall, &c2)
	if !strings.Contains(string(c1.RawInput), "git rebase main") || len(c1.Content) != 1 || !strings.Contains(string(c1.Content[0]), "rebased 3 commits") {
		t.Errorf("tool call c1 = %s", msgs[5].ToolCall)
	}
	if len(c2.Content) != 1 || !strings.Contains(string(c2.Content[0]), "denied") {
		t.Errorf("a raw output without content becomes content: %s", msgs[6].ToolCall)
	}
	if msgs[5].ApprovalID != nil || msgs[6].ApprovalID == nil || *msgs[6].ApprovalID != appr.ID {
		t.Errorf("approval links = %v / %v, want none / %s", msgs[5].ApprovalID, msgs[6].ApprovalID, appr.ID)
	}
	if msgs[2].ApprovalID == nil || *msgs[2].ApprovalID != prop.ID {
		t.Errorf("proposal message = %+v", msgs[2])
	}
	// A failed run closes with its error.
	fr := e.convMessagesOf(f.doneRun.ID)
	if last := fr[len(fr)-1]; last.Role != "system" || last.Text == nil || !strings.HasPrefix(*last.Text, "Run failed") {
		t.Errorf("last message of a failed run = %+v", last)
	}
	// A run in progress has no closing message.
	if rm := e.convMessagesOf(f.runningRun.ID); len(rm) != 1 || rm[0].Role != "user" {
		t.Errorf("running run = %+v", rm)
	}
}

func (e *env) convMessagesOf(id string) []convMessage {
	e.t.Helper()
	m, _ := e.convMessages(id)
	return m
}

func TestForkConversationAnswersFromTheService(t *testing.T) {
	var forkedFrom string
	var sess *store.AssistSession
	e := newEnv(t, func(o *envOptions) {
		o.assist = &assistDouble{fork: func(_ context.Context, id string) (*store.AssistSession, error) {
			forkedFrom = id
			if sess == nil {
				return nil, fmt.Errorf("%w: agent run %s", store.ErrNotFound, id)
			}
			return sess, nil
		}}
	})
	f := e.convFixture()

	e.do("POST", "/api/conversations/"+f.execRun.ID+"/fork", nil).want(404)

	// The service creates the chat row; the response is the conversation view of it.
	sess = &store.AssistSession{
		TargetKind: store.AssistTargetGeneral, Runtime: "claude", Model: "sonnet", Status: store.AssistIdle, DraftHash: hash64, DraftUpdatedBy: store.DraftByUser,
		ForkedFromAgentRunID: f.execRun.ID,
	}
	must(t, e.st.CreateAssistSession(ctx(), sess))
	r := e.do("POST", "/api/conversations/"+f.execRun.ID+"/fork", nil).want(201)
	var got convItem
	r.decode(&got)
	if forkedFrom != f.execRun.ID || got.ID != sess.ID || got.Source != "chat" || got.ForkedFrom == nil || got.ForkedFrom.AgentRunID != f.execRun.ID ||
		got.Kind != "general" || got.Running || r.Header.Get("Location") != "/api/conversations/"+sess.ID {
		t.Errorf("fork response = %+v (asked to fork %s), Location %q", got, forkedFrom, r.Header.Get("Location"))
	}
}

func TestForkConversationErrorsMapToProblems(t *testing.T) {
	var next error
	e := newEnv(t, func(o *envOptions) {
		o.assist = &assistDouble{fork: func(context.Context, string) (*store.AssistSession, error) { return nil, next }}
	})
	id := "01JC7Z3K9Q8V5T2M4N6P8R0S1T"
	next = fmt.Errorf("%w: agent run %s is a turn of a chat", store.ErrNotFound, id)
	if p := e.do("POST", "/api/conversations/"+id+"/fork", nil).want(404).problem(); p != "not-found" {
		t.Errorf("not a flow run: problem %q", p)
	}
	next = fmt.Errorf("%w: the agent run is running", core.ErrInvalidState)
	if p := e.do("POST", "/api/conversations/"+id+"/fork", nil).want(409).problem(); p != "invalid-state" {
		t.Errorf("running run: problem %q", p)
	}
	next = &core.ValidationError{}
	if p := e.do("POST", "/api/conversations/"+id+"/fork", nil).want(422).problem(); p != "validation-failed" {
		t.Errorf("runtime gone: problem %q", p)
	}

	// Without the assist service the fork is unavailable while reads still work.
	e2 := newEnv(t)
	e2.do("POST", "/api/conversations/"+id+"/fork", nil).want(503)
	e2.get("/api/conversations").want(200)
}
