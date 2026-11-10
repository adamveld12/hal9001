package store

import (
	"errors"
	"testing"
	"time"
)

// An operator approval is a request approval with neither an agent run nor a node run. It is listed and decided like
// any approval, expires when the daemon restarts (the MCP call that waited for it is gone) and is pruned with the rest.
func TestOperatorApprovalLifecycle(t *testing.T) {
	s, clk := newTestStore(t)

	if err := s.CreateApproval(ctx(), &Approval{Type: ApprovalRequest}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a request approval without content: %v", err)
	}
	if err := s.CreateApproval(ctx(), &Approval{Type: ApprovalRequest, AgentRunID: "x", Request: `{}`}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a request approval with an agent run: %v", err)
	}
	if err := s.CreateApproval(ctx(), &Approval{Type: ApprovalProposal, Proposal: Inline("p")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a proposal without an agent run: %v", err)
	}

	pending := &Approval{Type: ApprovalRequest, Request: `{"title":"MCP client: flow_run","body_md":"","fields":[],"priority":"normal","dry_run":false}`}
	decided := &Approval{Type: ApprovalRequest, Request: pending.Request}
	must(t, s.CreateApproval(ctx(), pending))
	must(t, s.CreateApproval(ctx(), decided))
	if _, err := s.DecideApproval(ctx(), decided.ID, ApprovalDecision{Status: ApprovalApproved, DecidedBy: "me"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetApproval(ctx(), pending.ID)
	must(t, err)
	if got.NodeRunID != "" || got.AgentRunID != "" || got.Status != ApprovalPending {
		t.Fatalf("stored approval = %+v", got)
	}

	res, err := s.MarkInterrupted(ctx())
	must(t, err)
	if res.ExpiredApprovals != 1 {
		t.Errorf("restart expired %d approvals, want the one pending operator approval", res.ExpiredApprovals)
	}
	if got, _ := s.GetApproval(ctx(), pending.ID); got.Status != ApprovalExpired || got.DecidedBy != "system" {
		t.Errorf("after restart: %+v", got)
	}
	if got, _ := s.GetApproval(ctx(), decided.ID); got.Status != ApprovalApproved {
		t.Errorf("a decided approval changed on restart: %+v", got)
	}

	live := &Approval{Type: ApprovalRequest, Request: pending.Request}
	must(t, s.CreateApproval(ctx(), live))
	clk.Advance(40 * 24 * time.Hour)
	if _, err := s.Prune(ctx(), clk.Now(), 30, 180); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{pending.ID, decided.ID} {
		if _, err := s.GetApproval(ctx(), id); !errors.Is(err, ErrNotFound) {
			t.Errorf("old decided operator approval %s survived pruning: %v", id, err)
		}
	}
	if _, err := s.GetApproval(ctx(), live.ID); err != nil {
		t.Errorf("a pending operator approval was pruned: %v", err)
	}
}

// A detached operator approval (request_approval) outlives the call that raised it: a restart leaves it pending.
func TestDetachedOperatorApprovalSurvivesRestart(t *testing.T) {
	s, _ := newTestStore(t)
	gated := &Approval{Type: ApprovalRequest, Request: `{"title":"gate","body_md":"","fields":[],"priority":"normal","dry_run":false,"source":"mcp"}`}
	asked := &Approval{Type: ApprovalRequest, Request: `{"title":"ask","body_md":"","fields":[],"priority":"normal","dry_run":false,"source":"mcp","detached":true}`}
	must(t, s.CreateApproval(ctx(), gated))
	must(t, s.CreateApproval(ctx(), asked))
	if _, err := s.MarkInterrupted(ctx()); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetApproval(ctx(), gated.ID); got.Status != ApprovalExpired {
		t.Errorf("the gated approval after restart: %s", got.Status)
	}
	if got, _ := s.GetApproval(ctx(), asked.ID); got.Status != ApprovalPending {
		t.Errorf("the detached approval after restart: %s", got.Status)
	}
}

// A note from an MCP client has no run; it names the client and keeps its thread.
func TestMCPInboxNote(t *testing.T) {
	s, _ := newTestStore(t)
	it := &InboxItem{SourceType: SourceMCP, Title: "Build done", SummaryMD: "green", Client: "claude-code 2.1", ThreadKey: "ci", Links: []InboxLink{{Label: "CI", URL: "https://example.com"}}}
	must(t, s.CreateInboxItem(ctx(), it))
	got, err := s.GetInboxItem(ctx(), it.ID)
	must(t, err)
	if got.SourceType != SourceMCP || got.Client != "claude-code 2.1" || got.ThreadKey != "ci" || got.Priority != PriorityNormal || len(got.Links) != 1 {
		t.Errorf("stored note = %+v", got)
	}
	if err := s.CreateInboxItem(ctx(), &InboxItem{SourceType: SourceMCP, Title: "x", NodeRunID: "n"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an mcp note with a run id: %v", err)
	}
}
