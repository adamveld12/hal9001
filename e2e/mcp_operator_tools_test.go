package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpNewFlow = `description: Created by an agent over MCP
nodes:
  - id: note
    type: inbox
    title: hello
    summary_md: hi
edges: []
`

const mcpNewSensor = `description: Counts to three
lang: bash
script: printf '{"n":1}\n'
key: "{{ .n }}"
`

func connectMCP(t *testing.T, d *Daemon, name string) *mcpsdk.ClientSession {
	t.Helper()
	s, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: name, Version: "9.9"}, nil).
		Connect(context.Background(), &mcpsdk.StreamableClientTransport{Endpoint: d.URL() + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func callMCP(t *testing.T, s *mcpsdk.ClientSession, tool string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return res
}

type requestView struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Comment       string `json:"comment"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	DescriptionBy string `json:"description_by"`
	Priority      string `json:"priority"`
	Origin        struct {
		Kind   string `json:"kind"`
		Client string `json:"client"`
	} `json:"origin"`
	Details struct {
		BodyMD string `json:"body_md"`
	} `json:"details"`
	Editable []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"editable"`
	Conversation *struct{} `json:"conversation"`
}

// waitRequest waits for a pending request approval whose title starts with prefix.
func waitRequest(t *testing.T, d *Daemon, prefix string) requestView {
	t.Helper()
	var got requestView
	d.Eventually("a pending approval titled "+prefix, func() bool {
		for _, a := range d.Approvals("pending") {
			var v requestView
			d.Get("/api/approvals/" + a.ID).Want(200).JSON(&v)
			if strings.HasPrefix(v.Title, prefix) {
				got = v
				return true
			}
		}
		return false
	})
	return got
}

func mcpJSON[T any](t *testing.T, res *mcpsdk.CallToolResult) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(resultText(t, res)), &v); err != nil {
		t.Fatalf("result is not the expected JSON: %v\n%s", err, resultText(t, res))
	}
	return v
}

type loggedNotification struct {
	Kind     string `json:"kind"`
	FlowID   string `json:"flow_id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Priority string `json:"priority"`
	ThreadID string `json:"thread_id"`
	ItemID   string `json:"item_id"`
	Editable bool   `json:"editable"`
}

// notificationLog is the daemon's notification log (GET /api/notifications).
func notificationLog(d *Daemon) []loggedNotification {
	d.t.Helper()
	var l struct {
		Items []loggedNotification `json:"items"`
	}
	d.Get("/api/notifications?limit=100").Want(200).JSON(&l)
	return l.Items
}

// flow_create and sensor_create: validation errors come straight back, a valid spec waits for the Inbox approval
// and is created only when it is approved, a denial or an existing id creates nothing.
func TestMCPCreateFlowAndSensorNeedApproval(t *testing.T) {
	d := newDaemon(t)
	d.Start()
	s := connectMCP(t, d, "e2e-agent")

	// Invalid: errors, no approval, nothing on disk.
	res := callMCP(t, s, "flow_create", map[string]any{"id": "broken", "spec": "nodes:\n  - {id: a, type: inbox, title: t}\nedges:\n  - {from: a, to: ghost}\n"})
	if !res.IsError || !strings.Contains(resultText(t, res), "unknown_node") {
		t.Fatalf("invalid flow_create: %+v", res)
	}
	if got := d.Approvals("pending"); len(got) != 0 {
		t.Fatalf("an invalid spec raised approvals: %+v", got)
	}
	d.Get("/api/flows/broken").Want(404)

	// Valid: pending approval shows the spec; the flow does not exist until approval; created disabled.
	wait := callAsync(s, "flow_create", map[string]any{"id": "agent-made", "spec": mcpNewFlow})
	ap := waitRequest(t, d, "Create flow: agent-made")
	if ap.Origin.Kind != "mcp" || !strings.Contains(ap.Origin.Client, "e2e-agent") || ap.DescriptionBy != "hal" || ap.Description == "" || ap.Conversation != nil ||
		!strings.Contains(ap.Details.BodyMD, "Created by an agent over MCP") ||
		!strings.Contains(ap.Details.BodyMD, "```yaml") || !strings.Contains(ap.Details.BodyMD, "disabled") {
		t.Errorf("approval = %+v", ap)
	}
	d.Get("/api/flows/agent-made").Want(404)
	d.Decide(ap.ID, true, nil)
	res, err := wait()
	if err != nil || res.IsError || !strings.Contains(resultText(t, res), `"created":true`) {
		t.Fatalf("approved flow_create: %v %+v", err, res)
	}
	var flow struct {
		Flow struct {
			Enabled bool `json:"enabled"`
		} `json:"flow"`
	}
	d.Get("/api/flows/agent-made").Want(200).JSON(&flow)
	if flow.Flow.Enabled {
		t.Error("a flow whose spec says nothing about enabled was created enabled")
	}

	// A duplicate id is refused before anyone is asked.
	res = callMCP(t, s, "flow_create", map[string]any{"id": "agent-made", "spec": mcpNewFlow})
	if !res.IsError || !strings.Contains(resultText(t, res), "already exists") || len(d.Approvals("pending")) != 0 {
		t.Errorf("duplicate flow_create: %+v", res)
	}

	// Denied: nothing is created and the agent learns why.
	wait = callAsync(s, "flow_create", map[string]any{"id": "denied-flow", "spec": mcpNewFlow})
	ap = waitRequest(t, d, "Create flow: denied-flow")
	d.Decide(ap.ID, false, map[string]any{"comment": "not this one"})
	res, err = wait()
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), `Denied by the user: "not this one"`) {
		t.Fatalf("denied flow_create: %v %+v", err, res)
	}
	d.Get("/api/flows/denied-flow").Want(404)

	// Sensors: the same pattern, with a JSON spec.
	sensorJSON := `{"description":"Counts to three","lang":"bash","script":"printf '{\"n\":1}\\n'","key":"{{ .n }}"}`
	wait = callAsync(s, "sensor_create", map[string]any{"id": "agent-sensor", "spec": sensorJSON})
	ap = waitRequest(t, d, "Create sensor: agent-sensor")
	d.Get("/api/sensors/agent-sensor").Want(404)
	d.Decide(ap.ID, true, nil)
	if res, err = wait(); err != nil || res.IsError {
		t.Fatalf("approved sensor_create: %v %+v", err, res)
	}
	d.Get("/api/sensors/agent-sensor").Want(200)
	if !strings.Contains(d.Read("sensors/agent-sensor.yaml"), "lang: bash") {
		t.Errorf("sensor file:\n%s", d.Read("sensors/agent-sensor.yaml"))
	}
	res = callMCP(t, s, "sensor_create", map[string]any{"id": "agent-sensor", "spec": mcpNewSensor})
	if !res.IsError || !strings.Contains(resultText(t, res), "already exists") {
		t.Errorf("duplicate sensor_create: %+v", res)
	}
	res = callMCP(t, s, "sensor_validate", map[string]any{"spec": "lang: cobol\n", "id": "x"})
	if res.IsError || !strings.Contains(resultText(t, res), `"valid":false`) {
		t.Errorf("sensor_validate: %+v", res)
	}
	if got := d.Approvals("pending"); len(got) != 0 {
		t.Errorf("approvals left pending: %+v", got)
	}
}

type askResult struct {
	ApprovalID string                     `json:"approval_id"`
	Status     string                     `json:"status"`
	Decision   string                     `json:"decision"`
	Comment    string                     `json:"comment"`
	Edited     bool                       `json:"edited"`
	Values     map[string]json.RawMessage `json:"values"`
}

// request_approval: approved with a comment and edited values, denied, still pending after wait_seconds (then read
// with approval_get), and expired when its timeout passes.
func TestMCPRequestApproval(t *testing.T) {
	d := newDaemon(t)
	d.Start()
	s := connectMCP(t, d, "e2e-agent")

	wait := callAsync(s, "request_approval", map[string]any{
		"title": "Send this reply?", "description": "The user asked for a shorter reply.", "body": "Draft below", "priority": "high",
		"editable": []map[string]any{{"key": "reply", "value": "Hi"}, {"key": "count", "value": 2}},
	})
	ap := waitRequest(t, d, "Send this reply?")
	if ap.Priority != "high" || ap.Origin.Kind != "mcp" || len(ap.Editable) != 2 || !strings.Contains(ap.Origin.Client, "e2e-agent") ||
		ap.DescriptionBy != "agent" || ap.Description != "The user asked for a shorter reply." {
		t.Errorf("approval = %+v", ap)
	}
	// The notification follows the approval a moment later.
	d.Eventually("the notification of the request", func() bool {
		for _, n := range notificationLog(d) {
			if n.Kind == "request" && n.FlowID == "mcp" && strings.Contains(n.Subtitle, "Send this reply?") && strings.Contains(n.Title, "e2e-agent") && n.Priority == "high" && n.Editable {
				return true
			}
		}
		return false
	})
	d.Decide(ap.ID, true, map[string]any{"comment": "ok, shorter", "edits": map[string]any{"reply": "Hello"}})
	res, err := wait()
	if err != nil || res.IsError {
		t.Fatalf("request_approval: %v %+v", err, res)
	}
	got := mcpJSON[askResult](t, res)
	if got.ApprovalID != ap.ID || got.Decision != "approved" || got.Comment != "ok, shorter" || !got.Edited || string(got.Values["reply"]) != `"Hello"` || string(got.Values["count"]) != "2" {
		t.Errorf("result = %+v", got)
	}

	// Denied.
	wait = callAsync(s, "request_approval", map[string]any{"title": "Delete everything?", "description": "Clean up the build directory."})
	ap = waitRequest(t, d, "Delete everything?")
	d.Decide(ap.ID, false, map[string]any{"comment": "no way"})
	res, _ = wait()
	if got = mcpJSON[askResult](t, res); got.Decision != "denied" || got.Comment != "no way" || len(got.Values) != 0 {
		t.Errorf("denied result = %+v", got)
	}

	// Undecided when the wait ends: pending, with the id; the approval stays open and is decided later.
	start := time.Now()
	res = callMCP(t, s, "request_approval", map[string]any{"title": "Later?", "description": "Can wait.", "wait_seconds": 1, "timeout_seconds": 3600})
	got = mcpJSON[askResult](t, res)
	if got.Status != "pending" || got.ApprovalID == "" || time.Since(start) > 10*time.Second {
		t.Fatalf("pending result = %+v after %s", got, time.Since(start))
	}
	var detail requestView
	d.Get("/api/approvals/" + got.ApprovalID).Want(200).JSON(&detail)
	if detail.Status != "pending" {
		t.Fatalf("the approval did not stay open: %+v", detail)
	}
	d.Decide(got.ApprovalID, true, map[string]any{"comment": "yes, later is fine"})
	res = callMCP(t, s, "approval_get", map[string]any{"id": got.ApprovalID})
	var polled struct {
		Status string    `json:"status"`
		Result askResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &polled); err != nil || polled.Status != "approved" || polled.Result.Decision != "approved" || polled.Result.Comment != "yes, later is fine" {
		t.Errorf("approval_get = %v %s", err, resultText(t, res))
	}

	// A timeout shorter than the wait expires the approval: the agent is told expired.
	start = time.Now()
	res = callMCP(t, s, "request_approval", map[string]any{"title": "Quick?", "description": "Needs an answer fast.", "timeout_seconds": 10})
	got = mcpJSON[askResult](t, res)
	if got.Decision != "expired" || time.Since(start) > 30*time.Second {
		t.Errorf("expiry result = %+v after %s", got, time.Since(start))
	}
}

// send_note posts an Inbox note that names the MCP client and raises a notification.
func TestMCPSendNote(t *testing.T) {
	d := newDaemon(t)
	d.Start()
	s := connectMCP(t, d, "e2e-agent")

	res := callMCP(t, s, "send_note", map[string]any{
		"title": "Refactor finished", "body": "All **12** files migrated.", "priority": "high",
		"url": "https://example.com/pr/7", "link_label": "PR", "thread": "refactor",
	})
	if res.IsError {
		t.Fatalf("send_note: %+v", res)
	}
	note := d.InboxByTitle()["Refactor finished"]
	if note.ID == "" || note.Source != "mcp" || note.Priority != "high" || note.FlowID != "mcp" || !strings.Contains(note.SummaryMD, "12") || len(note.Links) != 1 || note.Links[0].URL != "https://example.com/pr/7" {
		t.Fatalf("inbox note = %+v", note)
	}
	var full struct {
		Client    string `json:"client"`
		ThreadKey string `json:"thread_key"`
	}
	d.Get("/api/inbox/" + note.ID).Want(200).JSON(&full)
	if !strings.Contains(full.Client, "e2e-agent") || full.ThreadKey != "refactor" {
		t.Errorf("attribution = %+v", full)
	}
	var notified bool
	for _, n := range notificationLog(d) {
		if n.Kind == "note" && n.FlowID == "mcp" && strings.Contains(n.Subtitle, "Refactor finished") && strings.Contains(n.Title, "e2e-agent") && n.ThreadID == "mcp:refactor" && n.ItemID == note.ID {
			notified = true
		}
	}
	if !notified {
		t.Errorf("no notification for the note: %+v", notificationLog(d))
	}

	// Rate limit: more notes than a client may post in a minute are refused.
	var refused bool
	for range 8 {
		if r := callMCP(t, s, "send_note", map[string]any{"title": "spam"}); r.IsError && strings.Contains(resultText(t, r), "rate limit") {
			refused = true
		}
	}
	if !refused {
		t.Error("send_note was never rate limited")
	}
}
