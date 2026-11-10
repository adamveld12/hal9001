package notify_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"hal9001/internal/notify"
	"hal9001/internal/store"
)

func TestNoteRendersFlowTitleItemSubtitleAndPlainBody(t *testing.T) {
	it := &store.InboxItem{ID: "01ITEM", Title: "Deploy is failing", Priority: store.PriorityHigh,
		SummaryMD: "# Heading\n\nRoll back **now** via [the PR](https://x.test/1).\n- second `line`\nthird line"}
	n := notify.Note("triage", it, "http://h/inbox/01ITEM")
	if n.Kind != "note" || n.Title != "triage" || n.ThreadID != "triage" || n.FlowID != "triage" || n.Subtitle != "Deploy is failing" ||
		n.ItemID != "01ITEM" || n.ApprovalID != "" || n.Priority != "high" || n.URL != "http://h/inbox/01ITEM" || n.DryRun {
		t.Errorf("note: %+v", n)
	}
	if want := "Heading\nRoll back now via the PR."; n.Body != want {
		t.Errorf("body = %q, want %q (first two lines, markdown stripped)", n.Body, want)
	}
}

func TestProposalRendering(t *testing.T) {
	n := notify.Proposal("review", "01AP", "**Plan:** create hello.txt\nthen more", false, "http://h/inbox/approval/01AP")
	if n.Kind != "proposal" || n.ApprovalType != "proposal" || n.ApprovalID != "01AP" || n.Subtitle != "Approval: Plan: create hello.txt" ||
		n.Priority != "normal" || n.Body != "Plan: create hello.txt\nthen more" {
		t.Errorf("proposal: %+v", n)
	}
}

func TestToolCallRenderingShortensArguments(t *testing.T) {
	args := json.RawMessage(`{"path":"/tmp/a.txt","content":"` + strings.Repeat("x", 200) + `"}`)
	n := notify.ToolCall("review", "01AP", "Write file", args, false, "u")
	if n.Kind != "tool_call" || n.ApprovalType != "tool_call" || n.Priority != "high" || n.Body != "" {
		t.Errorf("tool call: %+v", n)
	}
	if !strings.HasPrefix(n.Subtitle, "Tool call: Write file content=xxx") || !strings.HasSuffix(n.Subtitle, "…") || utf8.RuneCountInString(n.Subtitle) > 120 {
		t.Errorf("subtitle %q", n.Subtitle)
	}
	if n := notify.ToolCall("f", "a", "Read", nil, false, "u"); n.Subtitle != "Tool call: Read" {
		t.Errorf("no args: %q", n.Subtitle)
	}
}

func TestRequestRenderingEditableAndDryRun(t *testing.T) {
	req := store.RequestApproval{Title: "Ship api?", BodyMD: notify.DryRunNotice + "\n\nDeploy **api** to prod", Priority: store.PriorityUrgent,
		DryRun: true, Editable: []store.RequestEditable{{Key: "name"}}}
	n := notify.Request("ask", "01AP", req, "u")
	if n.Kind != "request" || n.ApprovalType != "request" || !n.Editable || !n.DryRun || n.Priority != "urgent" ||
		n.Subtitle != "[dry run] Request: Ship api?" || n.Body != "Deploy api to prod" {
		t.Errorf("request: %+v", n)
	}
	req.Editable, req.DryRun, req.Priority = nil, false, ""
	if n := notify.Request("ask", "01AP", req, "u"); n.Editable || n.Priority != "normal" || strings.HasPrefix(n.Subtitle, "[dry run]") {
		t.Errorf("plain request: %+v", n)
	}
}

func TestBodyTruncatesAtTwoHundredCharacters(t *testing.T) {
	b := notify.Body(strings.Repeat("é", 500))
	if utf8.RuneCountInString(b) != 200 || !strings.HasSuffix(b, "…") {
		t.Errorf("body has %d runes: %.20q", utf8.RuneCountInString(b), b)
	}
	if got := notify.Body("```go\ncode line\n```\nafter"); got != "code line\nafter" {
		t.Errorf("fenced code: %q", got)
	}
}

func TestDryRunNote(t *testing.T) {
	n := notify.Note("f", &store.InboxItem{Title: "T", DryRun: true, Priority: store.PriorityNormal}, "u")
	if !n.DryRun || n.Subtitle != "[dry run] T" {
		t.Errorf("dry run note: %+v", n)
	}
}
