package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// The migration to the unified approval shape keeps every existing approval and backfills the new columns from what
// the old rows say: description, title, priority, an explicit origin, the conversation and the trimmed request JSON.
func TestMigrationUnifiedApprovalsBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hal.db")
	raw, err := sql.Open("sqlite", dsn(path, true))
	must(t, err)
	migs, err := loadMigrations()
	must(t, err)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)`)
	for _, m := range migs {
		if m.version > 16 {
			continue
		}
		exec(m.sql)
		exec(`INSERT INTO schema_migrations VALUES (?, ?, ?)`, m.version, m.name, "2026-11-05T12:00:00.000000000Z")
	}
	const now = "2026-11-05T12:00:00.000000000Z"
	exec(`INSERT INTO flow_versions VALUES ('reviews', 'h1', 'id: reviews', ?)`, now)
	exec(`INSERT INTO flow_runs (id, flow_id, flow_hash, kind, dry_run, status, created_at) VALUES ('FR', 'reviews', 'h1', 'live', 1, 'running', ?)`, now)
	exec(`INSERT INTO node_runs (id, flow_run_id, node_id, node_type, status, queued_at) VALUES
		('NA', 'FR', 'triage', 'agent', 'running', ?), ('NQ', 'FR', 'ok', 'approval', 'awaiting_approval', ?), ('NG', 'FR', 'deploy', 'command', 'awaiting_approval', ?)`, now, now, now)
	exec(`INSERT INTO agent_runs (id, node_run_id, runtime, mode, approve_style, policy, phase, status, created_at) VALUES ('AR', 'NA', 'claude', 'approve', 'per_tool', 'gated', 'execute', 'running', ?)`, now)
	exec(`INSERT INTO assist_sessions (id, target_kind, runtime, status, draft_hash, draft_updated_at, draft_updated_by, created_at, updated_at, title)
		VALUES ('S1', 'general', 'claude', 'running', 'h', ?, 'user', ?, ?, 'Fix the sensor')`, now, now, now)
	exec(`INSERT INTO agent_runs (id, assist_session_id, runtime, mode, approve_style, policy, phase, status, created_at) VALUES ('AC', 'S1', 'claude', 'approve', 'per_tool', 'gated', 'execute', 'running', ?)`, now)
	exec(`INSERT INTO assist_messages (id, session_id, role, text, created_at) VALUES ('M1', 'S1', 'user', 'hi', ?), ('M2', 'S1', 'tool', NULL, ?)`, now, now)
	exec(`UPDATE assist_messages SET approval_id = 'A-chat' WHERE id = 'M2'`)
	exec(`INSERT INTO permission_requests (id, agent_run_id, tool_kind, title, raw_input_text, reason, requested_at) VALUES
		('P1', 'AR', 'execute', 'Bash', '{"command":"npm test"}', 'Run the tests before the review', ?),
		('P2', 'AR', 'execute', 'Bash', '{}', 'approved by user', ?),
		('P3', 'AC', 'other', 'hal tool: draft_test', '{"tool":"draft_test","arguments":{"script":"echo hi"}}', '', ?)`, now, now, now)
	ins := `INSERT INTO approvals (id, agent_run_id, node_run_id, type, permission_request_id, proposal_text, request, status, comment, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`
	exec(ins, "A-tool", "AR", nil, "tool_call", "P1", "", "", "pending", "", now)
	exec(ins, "A-decided", "AR", nil, "tool_call", "P2", "", "", "denied", "no", now)
	exec(ins, "A-chat", "AC", nil, "tool_call", "P3", "", "", "pending", "", now)
	exec(ins, "A-prop", "AR", nil, "proposal", nil, "\n## Bump the **`version`**\n\nDetails follow.", "", "pending", "", now)
	exec(ins, "A-node", nil, "NQ", "request", nil, "", `{"title":"Ship it?","body_md":"Release 1.2 is ready.\n\nMore.","fields":[{"key":"v","value":"1.2"}],"editable":[{"key":"v","value":"1.2"}],"priority":"high","dry_run":true,"on_timeout":"denied"}`, "pending", "", now)
	exec(ins, "A-gate", nil, "NG", "request", nil, "", `{"title":"reviews: run deploy?","body_md":"**Script**\n\nx","fields":[],"priority":"normal","dry_run":false,"gate":true}`, "pending", "", now)
	exec(ins, "A-mcp", nil, nil, "request", nil, "", `{"title":"MCP client: flow_run","body_md":"","fields":[{"key":"tool","value":"flow_run"}],"priority":"normal","dry_run":false,"source":"mcp","client":"claude-code","principal":"adam","detached":true}`, "pending", "", now)
	must(t, raw.Close())

	s, err := Open(path)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })

	get := func(id string) *Approval {
		t.Helper()
		a, err := s.GetApproval(ctx(), id)
		must(t, err)
		return a
	}

	a := get("A-tool")
	if a.Title != "Bash" || a.Description != "Run the tests before the review" || a.DescriptionBy != DescByHal || a.Priority != PriorityHigh {
		t.Errorf("pending tool call: %+v", a)
	}
	if a.Origin != OriginFlow || a.OriginLabel != "reviews / triage" || a.ConversationID != "AR" || a.ConversationKind != ConvFlow || a.Anchor != "" {
		t.Errorf("tool call origin and conversation: %+v", a)
	}
	if a.Details.Command != "npm test" || a.Details.Tool != "execute" || !a.Details.DryRun || a.Request != "" {
		t.Errorf("tool call details: %+v", a.Details)
	}
	if d := get("A-decided").Description; d != "Bash" {
		t.Errorf("a decided tool call is described by its title, got %q (the reason holds the decision)", d)
	}

	a = get("A-chat")
	if a.Origin != OriginChat || a.OriginLabel != "Chat: Fix the sensor" || a.ConversationID != "S1" || a.ConversationKind != ConvChat || a.Anchor != "M2" {
		t.Errorf("chat tool call: %+v", a)
	}
	if a.Details.Command != "echo hi" {
		t.Errorf("chat tool call command = %q", a.Details.Command)
	}

	a = get("A-prop")
	if a.Description != "Bump the version" || a.Title != a.Description || a.Priority != PriorityNormal || a.Proposal.Text == "" {
		t.Errorf("proposal: %+v", a)
	}

	a = get("A-node")
	if a.Title != "Ship it?" || a.Description != "Release 1.2 is ready." || a.Priority != PriorityHigh || a.Origin != OriginFlow || a.OriginLabel != "reviews / ok" {
		t.Errorf("approval node request: %+v", a)
	}
	if a.ConversationID != "" || a.ConversationKind != "" || !a.Details.DryRun || a.Details.BodyMD != "Release 1.2 is ready.\n\nMore." || len(a.Details.Fields) != 1 {
		t.Errorf("approval node details: %+v", a)
	}
	if a.Request != `{"editable":[{"key":"v","value":"1.2"}],"on_timeout":"denied"}` {
		t.Errorf("approval node request JSON trimmed to the machinery: %s", a.Request)
	}

	a = get("A-gate")
	if a.Description != "Run node deploy (command) of flow reviews" || a.Title != "reviews: run deploy?" || !strings.Contains(a.Request, `"gate":true`) {
		t.Errorf("gate: %+v", a)
	}

	a = get("A-mcp")
	if a.Origin != OriginMCP || a.OriginLabel != "MCP: claude-code" || a.OriginClient != "claude-code" || a.Description != "MCP client: flow_run" || a.ConversationID != "" {
		t.Errorf("mcp request: %+v", a)
	}
	if a.Request != `{"principal":"adam","detached":true}` || a.Details.Fields[0].Key != "tool" {
		t.Errorf("mcp request machinery: %q, %+v", a.Request, a.Details)
	}

	var fk int
	must(t, s.wdb.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	if fk != 1 {
		t.Errorf("foreign_keys = %d after migrations, want 1", fk)
	}
}

func TestCreateApprovalValidates(t *testing.T) {
	s, _ := newTestStore(t)
	for name, a := range map[string]*Approval{
		"no title":           {Type: ApprovalRequest, Origin: OriginMCP, Description: "d"},
		"no description":     {Type: ApprovalRequest, Origin: OriginMCP, Title: "t"},
		"blank":              {Type: ApprovalRequest, Origin: OriginMCP, Title: "t", Description: "  \n"},
		"too long":           {Type: ApprovalRequest, Origin: OriginMCP, Title: "t", Description: strings.Repeat("é", MaxApprovalDescription+1)},
		"no origin":          {Type: ApprovalRequest, NodeRunID: "x", Title: "t", Description: "d"},
		"conversation id":    {Type: ApprovalRequest, Origin: OriginMCP, Title: "t", Description: "d", ConversationID: "c"},
		"description_by":     {Type: ApprovalRequest, Origin: OriginMCP, Title: "t", Description: "d", DescriptionBy: "robot"},
		"request, no raiser": {Type: ApprovalRequest, Origin: OriginFlow, Title: "t", Description: "d"},
	} {
		if err := s.CreateApproval(ctx(), a); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	ok := &Approval{Type: ApprovalRequest, Origin: OriginMCP, Title: "t", Description: strings.Repeat("é", MaxApprovalDescription)}
	must(t, s.CreateApproval(ctx(), ok))
	got, err := s.GetApproval(ctx(), ok.ID)
	must(t, err)
	if got.DescriptionBy != DescByHal || got.Priority != PriorityNormal || got.Details.Fields == nil || got.Details.Diffs == nil {
		t.Errorf("defaults = %+v", got)
	}
}

// Approvals are listed by conversation, and steered denials (denied with a comment) are counted per conversation.
func TestApprovalsByConversationAndSteeredDenials(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "f", "", FlowRunRunning)
	nr := mkNodeRun(t, s, fr.ID, "agent", NodeAgent, NodeRunning)
	a1, a2 := mkAgentRun(t, s, nr.ID, StylePerTool, AgentRunning), mkAgentRun(t, s, nr.ID, StylePerTool, AgentRunning)

	raise := func(run *AgentRun) *Approval {
		t.Helper()
		pr := &PermissionRequest{AgentRunID: run.ID, ToolKind: "execute", Title: "Bash"}
		must(t, s.CreatePermissionRequest(ctx(), pr))
		ap := valid(&Approval{AgentRunID: run.ID, Type: ApprovalToolCall, PermissionRequestID: pr.ID, ConversationID: run.ID, ConversationKind: ConvFlow})
		must(t, s.CreateApproval(ctx(), ap))
		return ap
	}
	decide := func(ap *Approval, st ApprovalState, comment string) {
		t.Helper()
		_, err := s.DecideApproval(ctx(), ap.ID, ApprovalDecision{Status: st, Comment: comment, DecidedBy: "me"})
		must(t, err)
	}
	decide(raise(a1), ApprovalDenied, "use make instead")
	decide(raise(a1), ApprovalDenied, "")
	decide(raise(a1), ApprovalApproved, "go ahead")
	decide(raise(a1), ApprovalDenied, "no, read the docs first")
	pending := raise(a1)
	decide(raise(a2), ApprovalDenied, "steer")

	if n, err := s.CountSteeredDenials(ctx(), a1.ID); err != nil || n != 2 {
		t.Errorf("steered denials of the first run = %d, %v; want 2 (a plain denial and an approval do not count)", n, err)
	}
	if n, _ := s.CountSteeredDenials(ctx(), a2.ID); n != 1 {
		t.Errorf("steered denials of the second run = %d, want 1", n)
	}
	page, err := s.ListApprovals(ctx(), ApprovalFilter{ConversationID: a1.ID, Statuses: []ApprovalState{ApprovalPending}})
	must(t, err)
	if len(page.Items) != 1 || page.Items[0].ID != pending.ID {
		t.Errorf("pending approvals of the first run = %+v", page.Items)
	}
	if all, _ := s.ListApprovals(ctx(), ApprovalFilter{ConversationID: a1.ID}); len(all.Items) != 5 {
		t.Errorf("approvals of the first run = %d, want 5", len(all.Items))
	}
}

// After a restart an agent-asked request approval (it has an agent run, a waiter blocked the tool call) is expired,
// while a node's request approval survives.
func TestRestartExpiresAgentAskedRequests(t *testing.T) {
	s, _ := newTestStore(t)
	fr := mkFlowRun(t, s, "f", "", FlowRunRunning)
	nr := mkNodeRun(t, s, fr.ID, "agent", NodeAgent, NodeRunning)
	ar := mkAgentRun(t, s, nr.ID, StylePerTool, AgentRunning)
	asked := valid(&Approval{Type: ApprovalRequest, AgentRunID: ar.ID, NodeRunID: nr.ID, DescriptionBy: DescByAgent, Request: `{}`})
	node := valid(&Approval{Type: ApprovalRequest, NodeRunID: nr.ID, Request: `{}`})
	must(t, s.CreateApproval(ctx(), asked))
	must(t, s.CreateApproval(ctx(), node))
	if _, err := s.MarkInterrupted(ctx()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetApproval(ctx(), asked.ID); got.Status != ApprovalExpired {
		t.Errorf("the agent-asked approval after restart: %s", got.Status)
	}
	if got, _ := s.GetApproval(ctx(), node.ID); got.Status != ApprovalPending {
		t.Errorf("the node approval after restart: %s", got.Status)
	}
}
