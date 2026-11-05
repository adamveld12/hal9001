package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hal9001/internal/store"
)

type agentFixture struct {
	event    *store.Event
	flowRun  *store.FlowRun
	nodeRun  *store.NodeRun
	agentRun *store.AgentRun
	perm     *store.PermissionRequest
	toolAppr *store.Approval
	propAppr *store.Approval
	raw      string
}

func (e *env) agentFixture() agentFixture {
	e.t.Helper()
	var f agentFixture
	f.event = e.event("gh", "pr-7", `{"pr":7}`)
	f.flowRun = e.flowRun("demo", f.event.ID, store.KindLive, store.FlowRunAwaitingApproval)
	f.nodeRun = e.nodeRun(f.flowRun.ID, "review", store.NodeAwaitingApproval, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	f.agentRun = e.agentRun(f.nodeRun.ID, store.AgentAwaitingApproval)
	f.raw = `{"ts":"2026-11-05T12:00:00Z","dir":"out","msg":{"method":"initialize"}}` + "\n"
	rawBlob, err := e.st.PutBlob([]byte(f.raw))
	must(e.t, err)
	f.agentRun.RawJSONRPC = rawBlob
	f.agentRun.AgentInfo = json.RawMessage(`{"name":"claude-agent-acp","version":"1.2.3"}`)
	f.agentRun.Capabilities = json.RawMessage(`{"loadSession":true}`)
	f.agentRun.SessionID = "sess-1"
	f.agentRun.Result = store.Inline(`{"final_message":"Looks fine.","note":{"title":"PR 7","priority":"high","summary_md":"ok","links":[{"label":"pr","url":"https://example.test/7"}]},"parse_error":false}`)
	must(e.t, e.st.UpdateAgentRun(ctx(), f.agentRun))

	kinds := []string{"message", "thought", "tool_call", "tool_call_update", "message"}
	for i, k := range kinds {
		ev := &store.AgentEvent{AgentRunID: f.agentRun.ID, Kind: k, Payload: store.Inline(`{"i":` + string(rune('0'+i)) + `}`)}
		if k == "tool_call" || k == "tool_call_update" {
			ev.ToolCallID = "call-1"
		}
		must(e.t, e.st.AppendAgentEvent(ctx(), ev))
	}

	f.perm = &store.PermissionRequest{
		AgentRunID: f.agentRun.ID, ToolCallID: "call-1", ToolKind: "edit", Title: "Edit main.go",
		RawInput: store.Inline(`{"path":"main.go"}`),
		Options:  json.RawMessage(`[{"optionId":"allow","name":"Allow once","kind":"allow_once"},{"optionId":"deny","name":"Reject","kind":"reject_once"}]`),
	}
	must(e.t, e.st.CreatePermissionRequest(ctx(), f.perm))
	f.toolAppr = &store.Approval{AgentRunID: f.agentRun.ID, Type: store.ApprovalToolCall, PermissionRequestID: f.perm.ID}
	must(e.t, e.st.CreateApproval(ctx(), f.toolAppr))
	f.propAppr = &store.Approval{AgentRunID: f.agentRun.ID, Type: store.ApprovalProposal, Proposal: store.Inline("## Plan\nFix it.")}
	must(e.t, e.st.CreateApproval(ctx(), f.propAppr))
	return f
}

func TestAgentRunDetailTranscriptAndPermissions(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()

	var run struct {
		ID           string                  `json:"id"`
		FlowRunID    string                  `json:"flow_run_id"`
		FlowID       string                  `json:"flow_id"`
		NodeID       string                  `json:"node_id"`
		AgentInfo    map[string]any          `json:"agent_info"`
		Capabilities map[string]any          `json:"capabilities"`
		RawBlob      *string                 `json:"raw_blob"`
		RawSize      int                     `json:"raw_size"`
		Prompt       string                  `json:"rendered_prompt"`
		FinalMessage *string                 `json:"final_message"`
		Result       map[string]any          `json:"result"`
		PhaseRunIDs  []string                `json:"phase_run_ids"`
		Approvals    []struct{ Type string } `json:"approvals"`
	}
	e.get("/api/agent-runs/" + f.agentRun.ID).want(200).decode(&run)
	if run.FlowRunID != f.flowRun.ID || run.FlowID != "demo" || run.NodeID != "review" || run.Prompt != "review it" {
		t.Errorf("lineage / prompt wrong: %+v", run)
	}
	if run.AgentInfo["name"] != "claude-agent-acp" || run.Capabilities["loadSession"] != true {
		t.Errorf("agent info = %v, capabilities = %v", run.AgentInfo, run.Capabilities)
	}
	if run.FinalMessage == nil || *run.FinalMessage != "Looks fine." || run.Result["title"] != "PR 7" {
		t.Errorf("result = %v, final = %v", run.Result, run.FinalMessage)
	}
	if run.RawBlob == nil || run.RawSize != len(f.raw) || len(run.PhaseRunIDs) != 1 || run.PhaseRunIDs[0] != f.agentRun.ID || len(run.Approvals) != 2 {
		t.Errorf("raw/phase/approvals = %v %d %v %d", run.RawBlob, run.RawSize, run.PhaseRunIDs, len(run.Approvals))
	}
	e.get("/api/agent-runs/01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(404)

	// Transcript paging by seq.
	var page struct {
		Items []struct {
			Seq        int     `json:"seq"`
			Kind       string  `json:"kind"`
			ToolCallID *string `json:"tool_call_id"`
		} `json:"items"`
		LastSeq int  `json:"last_seq"`
		HasMore bool `json:"has_more"`
	}
	base := "/api/agent-runs/" + f.agentRun.ID + "/events"
	e.get(base + "?after_seq=1&limit=2").want(200).decode(&page)
	if len(page.Items) != 2 || page.Items[0].Seq != 2 || page.LastSeq != 3 || !page.HasMore {
		t.Errorf("after_seq=1&limit=2 -> %+v", page)
	}
	e.get(base + "?after_seq=3").want(200).decode(&page)
	if len(page.Items) != 2 || page.Items[1].Seq != 5 || page.LastSeq != 5 || page.HasMore {
		t.Errorf("after_seq=3 -> %+v", page)
	}
	e.get(base + "?after_seq=5").want(200).decode(&page)
	if len(page.Items) != 0 || page.LastSeq != 5 || page.HasMore {
		t.Errorf("past the end -> %+v", page)
	}
	e.get(base + "?kind=tool_call,tool_call_update").want(200).decode(&page)
	if len(page.Items) != 2 || page.Items[0].ToolCallID == nil || *page.Items[0].ToolCallID != "call-1" {
		t.Errorf("kind filter -> %+v", page)
	}
	for _, bad := range []string{"after_seq=-1", "after_seq=x", "limit=0", "limit=1001"} {
		e.get(base + "?" + bad).want(400)
	}
	e.get("/api/agent-runs/01JC7Z3K9Q8V5T2M4N6P8R0S1T/events").want(404)

	var perms struct {
		Items []struct {
			ToolKind   string  `json:"tool_kind"`
			Decision   *string `json:"decision"`
			ApprovalID *string `json:"approval_id"`
			Options    []struct {
				OptionID string `json:"option_id"`
				Kind     string `json:"kind"`
			} `json:"options"`
		} `json:"items"`
	}
	e.get("/api/agent-runs/" + f.agentRun.ID + "/permission-requests").want(200).decode(&perms)
	if len(perms.Items) != 1 || perms.Items[0].Decision != nil || perms.Items[0].ApprovalID == nil || *perms.Items[0].ApprovalID != f.toolAppr.ID ||
		len(perms.Items[0].Options) != 2 || perms.Items[0].Options[0].OptionID != "allow" {
		t.Errorf("permission requests = %+v", perms)
	}

	raw := e.get("/api/agent-runs/" + f.agentRun.ID + "/raw").want(200)
	if string(raw.Body) != f.raw || raw.Header.Get("Content-Type") != "application/x-ndjson" ||
		!strings.Contains(raw.Header.Get("Content-Disposition"), "agent-run-"+f.agentRun.ID+".jsonl") {
		t.Errorf("raw download: %q %v", raw.Body, raw.Header)
	}

	// Lists filter by status, runtime and lineage.
	if got := e.get("/api/agent-runs?status=awaiting_approval").want(200).ids(); len(got) != 1 {
		t.Errorf("status filter: %v", got)
	}
	for _, q := range []string{"status=running", "runtime=codex", "flow_run_id=01JC7Z3K9Q8V5T2M4N6P8R0S1T"} {
		if got := e.get("/api/agent-runs?" + q).want(200).ids(); len(got) != 0 {
			t.Errorf("?%s matched %v", q, got)
		}
	}
	if got := e.get("/api/agent-runs?flow_run_id=" + f.flowRun.ID + "&node_run_id=" + f.nodeRun.ID).want(200).ids(); len(got) != 1 {
		t.Errorf("lineage filter: %v", got)
	}
}

func TestApprovalQueueAndHistory(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()

	var tool struct {
		Type              string  `json:"type"`
		Status            string  `json:"status"`
		FlowID            string  `json:"flow_id"`
		NodeID            string  `json:"node_id"`
		Runtime           string  `json:"runtime"`
		EventID           *string `json:"event_id"`
		ProposalMD        *string `json:"proposal_md"`
		CancelRun         *bool   `json:"cancel_run"`
		PermissionRequest struct {
			Title    string         `json:"title"`
			RawInput map[string]any `json:"raw_input"`
		} `json:"permission_request"`
	}
	e.get("/api/approvals/" + f.toolAppr.ID).want(200).decode(&tool)
	if tool.Type != "tool_call" || tool.Status != "pending" || tool.FlowID != "demo" || tool.NodeID != "review" || tool.Runtime != "claude" ||
		tool.EventID == nil || *tool.EventID != f.event.ID || tool.ProposalMD != nil || tool.CancelRun != nil ||
		tool.PermissionRequest.Title != "Edit main.go" || tool.PermissionRequest.RawInput["path"] != "main.go" {
		t.Errorf("tool approval = %+v", tool)
	}
	prop := e.get("/api/approvals/" + f.propAppr.ID).want(200).json()
	if prop["proposal_md"] != "## Plan\nFix it." || prop["permission_request"] != nil {
		t.Errorf("proposal approval = %v", prop)
	}

	if got := e.get("/api/approvals?status=pending").want(200).ids(); len(got) != 2 || got[0] != f.propAppr.ID {
		t.Errorf("queue = %v, want both, newest first", got)
	}
	if got := e.get("/api/approvals?type=tool_call").want(200).ids(); len(got) != 1 || got[0] != f.toolAppr.ID {
		t.Errorf("type filter = %v", got)
	}

	// Once decided it leaves the queue and shows up in the history.
	_, err := e.st.DecideApproval(ctx(), f.toolAppr.ID, store.ApprovalDecision{Status: store.ApprovalDenied, DecidedBy: "localhost", Comment: "no"})
	must(t, err)
	if got := e.get("/api/approvals?status=pending").want(200).ids(); len(got) != 1 || got[0] != f.propAppr.ID {
		t.Errorf("queue after a decision = %v", got)
	}
	if got := e.get("/api/approvals?status=denied&agent_run_id=" + f.agentRun.ID).want(200).ids(); len(got) != 1 || got[0] != f.toolAppr.ID {
		t.Errorf("history = %v", got)
	}
	if got := e.get("/api/approvals?flow_run_id=" + f.flowRun.ID).want(200).ids(); len(got) != 2 {
		t.Errorf("flow run filter = %v", got)
	}
	if got := e.get("/api/approvals?status=approved,denied,expired").want(200).ids(); len(got) != 1 || got[0] != f.toolAppr.ID {
		t.Errorf("history with several statuses = %v", got)
	}
	if got := e.get("/api/approvals?status=pending,denied").want(200).ids(); len(got) != 2 {
		t.Errorf("pending and denied = %v", got)
	}
	e.get("/api/approvals?status=maybe").want(400)
	e.get("/api/approvals?status=pending,maybe").want(400)
	e.get("/api/approvals/01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(404)
}

func TestInboxOrderingFiltersAndFlags(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture()
	mk := func(title string, p store.Priority, mod func(*store.InboxItem)) *store.InboxItem {
		it := &store.InboxItem{SourceType: store.SourceNodeRun, NodeRunID: f.nodeRun.ID, Title: title, Priority: p, SummaryMD: "summary of " + title}
		if mod != nil {
			mod(it)
		}
		must(t, e.st.CreateInboxItem(ctx(), it))
		return it
	}
	low := mk("low one", store.PriorityLow, nil)
	urgent1 := mk("urgent one", store.PriorityUrgent, nil)
	normal := mk("normal one", store.PriorityNormal, func(i *store.InboxItem) { i.Starred = true })
	high := mk("high one", store.PriorityHigh, func(i *store.InboxItem) { i.Read = true })
	urgent2 := mk("urgent two", store.PriorityUrgent, nil)
	archived := mk("archived", store.PriorityHigh, func(i *store.InboxItem) { i.Archived = true })
	dry := mk("dry", store.PriorityLow, func(i *store.InboxItem) { i.DryRun = true })

	list := func(q string) []string { return e.get("/api/inbox?" + q).want(200).ids() }
	join := func(items ...*store.InboxItem) string {
		var ids []string
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		return strings.Join(ids, ",")
	}
	check := func(name, q string, want ...*store.InboxItem) {
		t.Helper()
		if got := strings.Join(list(q), ","); got != join(want...) {
			t.Errorf("%s (?%s): got %s, want %s", name, q, got, join(want...))
		}
	}
	check("priority then newest", "", urgent2, urgent1, high, normal, dry, low)
	check("time order", "sort=time", dry, urgent2, high, normal, urgent1, low)
	check("minimum priority", "priority=high", urgent2, urgent1, high)
	check("unread", "read=false&priority=urgent", urgent2, urgent1)
	check("starred", "starred=true", normal)
	check("only archived", "archived=true", archived)
	check("everything", "archived=any&sort=time", dry, archived, urgent2, high, normal, urgent1, low)
	check("dry runs", "dry_run=true", dry)
	check("search title", "q=URGENT", urgent2, urgent1)
	check("search summary", "q=summary+of+low", low)
	check("by flow", "flow_id=demo&priority=high", urgent2, urgent1, high)
	check("other flow", "flow_id=other")

	// Priority ordering paginates without skipping or repeating.
	want := []string{urgent2.ID, urgent1.ID, high.ID, normal.ID, dry.ID, low.ID}
	if ids, pages := e.walk("/api/inbox", 2); strings.Join(ids, ",") != strings.Join(want, ",") || pages != 3 {
		t.Errorf("walk by priority: %v over %d pages", ids, pages)
	}

	var item struct {
		FlowID   string  `json:"flow_id"`
		NodeID   string  `json:"node_id"`
		EventID  *string `json:"event_id"`
		SourceID string  `json:"source_id"`
		Source   string  `json:"source"`
		Starred  bool    `json:"starred"`
		Archived bool    `json:"archived"`
		Links    []any   `json:"links"`
		DryRun   bool    `json:"dry_run"`
	}
	e.get("/api/inbox/" + normal.ID).want(200).decode(&item)
	if item.FlowID != "demo" || item.NodeID != "review" || item.EventID == nil || item.Source != "node_run" || item.SourceID != f.nodeRun.ID || !item.Starred || item.Links == nil {
		t.Errorf("item = %+v", item)
	}

	e.do("PATCH", "/api/inbox/"+low.ID, `{"read":true,"starred":true}`).want(200)
	check("starred after patch", "starred=true", normal, low)
	e.do("PATCH", "/api/inbox/"+low.ID, `{"archived":true}`).want(200)
	check("archived hides it", "starred=true", normal)
	for _, bad := range []string{`{}`, `{"title":"x"}`, `{"read":"yes"}`} {
		if r := e.do("PATCH", "/api/inbox/"+low.ID, bad); r.Status != 400 && r.Status != 422 {
			t.Errorf("PATCH %s -> %d", bad, r.Status)
		}
	}
	if r := e.do("PATCH", "/api/inbox/"+low.ID, `{}`).want(422); r.problem() != "validation-failed" {
		t.Errorf("empty patch: %s", r.Body)
	}
	e.do("PATCH", "/api/inbox/01JC7Z3K9Q8V5T2M4N6P8R0S1T", `{"read":true}`).want(404)
	e.get("/api/inbox?priority=meh").want(400)
	e.get("/api/inbox?archived=maybe").want(400)
}

func TestStatusAndKillSwitchReflectTheStore(t *testing.T) {
	e := newEnv(t)
	f := e.agentFixture() // one running-ish agent flow, two pending approvals
	e.flowRun("demo", "", store.KindLive, store.FlowRunQueued)
	e.flowRun("demo", "", store.KindLive, store.FlowRunRunning)
	e.flowRun("demo", "", store.KindTest, store.FlowRunRunning) // test runs do not count
	e.agentRun(f.nodeRun.ID, store.AgentRunning)
	e.agentRun(f.nodeRun.ID, store.AgentQueued)
	for i, mod := range []func(*store.InboxItem){nil, nil, func(i *store.InboxItem) { i.DryRun = true }, func(i *store.InboxItem) { i.Archived = true }, func(i *store.InboxItem) { i.Read = true }} {
		it := &store.InboxItem{SourceType: store.SourceNodeRun, NodeRunID: f.nodeRun.ID, Title: "n" + string(rune('0'+i))}
		if mod != nil {
			mod(it)
		}
		must(t, e.st.CreateInboxItem(ctx(), it))
	}

	var st struct {
		KillSwitch       struct{ Paused bool } `json:"kill_switch"`
		RunningFlowRuns  int                   `json:"running_flow_runs"`
		QueuedFlowRuns   int                   `json:"queued_flow_runs"`
		RunningAgentRuns int                   `json:"running_agent_runs"`
		QueuedAgentRuns  int                   `json:"queued_agent_runs"`
		PendingApprovals int                   `json:"pending_approvals"`
		UnreadInbox      int                   `json:"unread_inbox"`
	}
	e.get("/api/status").want(200).decode(&st)
	if st.KillSwitch.Paused || st.RunningFlowRuns != 1 || st.QueuedFlowRuns != 1 || st.RunningAgentRuns != 1 || st.QueuedAgentRuns != 1 ||
		st.PendingApprovals != 2 || st.UnreadInbox != 2 {
		t.Errorf("status = %+v", st)
	}

	if ks := e.get("/api/killswitch").want(200).json(); ks["paused"] != false || ks["since"] != nil || ks["reason"] != nil {
		t.Errorf("released kill switch = %v", ks)
	}
	must(t, e.st.SetSetting(ctx(), killSwitchReasonKey, json.RawMessage(`"maintenance"`)))
	must(t, e.st.SetKillSwitch(ctx(), store.KillSwitch{Paused: true, KillRunning: true, By: "localhost", Since: time.Now()}))
	ks := e.get("/api/killswitch").want(200).json()
	if ks["paused"] != true || ks["kill_running"] != true || ks["by"] != "localhost" || ks["reason"] != "maintenance" || ks["since"] == nil {
		t.Errorf("paused kill switch = %v", ks)
	}
	if got := e.get("/api/status").want(200).json()["kill_switch"].(map[string]any); got["paused"] != true {
		t.Errorf("status kill_switch = %v", got)
	}

	e.get("/api/health").want(200)
	if v := e.get("/api/version").want(200).json(); v["version"] != "test" || v["home"] != e.home {
		t.Errorf("version = %v", v)
	}
}
