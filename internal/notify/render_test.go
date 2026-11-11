package notify_test

import (
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

func TestApprovalRendersTitleDescriptionAndPriority(t *testing.T) {
	n := notify.Approval(notify.ApprovalNote{FlowID: "review", ApprovalID: "01AP", Type: store.ApprovalProposal,
		Title: "Create hello.txt", Description: "**Plan:** create hello.txt\nthen more\nthird", Priority: store.PriorityHigh}, "http://h/approvals/01AP")
	if n.Kind != "proposal" || n.ApprovalType != "proposal" || n.ApprovalID != "01AP" || n.Subtitle != "Create hello.txt" ||
		n.Priority != "high" || n.Body != "Plan: create hello.txt\nthen more" || n.Title != "review" || n.URL != "http://h/approvals/01AP" || n.Editable {
		t.Errorf("proposal: %+v", n)
	}
}

func TestApprovalToolCallIsNotForcedHigh(t *testing.T) {
	n := notify.Approval(notify.ApprovalNote{FlowID: "review", ApprovalID: "01AP", Type: store.ApprovalToolCall,
		Title: "Run `npm test`", Description: "Run `npm test` in sandbox docker-dev"}, "u")
	if n.Kind != "tool_call" || n.ApprovalType != "tool_call" || n.Priority != "normal" || n.Body != "Run npm test in sandbox docker-dev" {
		t.Errorf("tool call: %+v", n)
	}
	if n.Subtitle != "Run `npm test`" {
		t.Errorf("subtitle %q", n.Subtitle)
	}
}

func TestApprovalRequestEditableAndDryRun(t *testing.T) {
	a := notify.ApprovalNote{FlowID: "ask", ApprovalID: "01AP", Type: store.ApprovalRequest, Title: "Ship api?",
		Description: "Deploy **api** to prod", Priority: store.PriorityUrgent, DryRun: true, Editable: true}
	n := notify.Approval(a, "u")
	if n.Kind != "request" || n.ApprovalType != "request" || !n.Editable || !n.DryRun || n.Priority != "urgent" ||
		n.Subtitle != "[dry run] Ship api?" || n.Body != "Deploy api to prod" {
		t.Errorf("request: %+v", n)
	}
	a.Editable, a.DryRun, a.Priority = false, false, ""
	if n := notify.Approval(a, "u"); n.Editable || n.Priority != "normal" || strings.HasPrefix(n.Subtitle, "[dry run]") {
		t.Errorf("plain request: %+v", n)
	}
	long := strings.Repeat("é", 500)
	if n := notify.Approval(notify.ApprovalNote{Type: store.ApprovalRequest, Title: long, Description: long}, "u"); utf8.RuneCountInString(n.Subtitle) > 120 || utf8.RuneCountInString(n.Body) != 200 {
		t.Errorf("limits: subtitle %d, body %d runes", utf8.RuneCountInString(n.Subtitle), utf8.RuneCountInString(n.Body))
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
