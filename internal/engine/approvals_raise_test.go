package engine_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/engine"
	"hal9001/internal/store"
)

func mcpSpec() engine.ApprovalSpec {
	return engine.ApprovalSpec{
		Type: store.ApprovalRequest, Origin: store.OriginMCP, OriginClient: "e2e-client",
		Title: "Run the flow?", Description: "Run flow demo once.",
	}
}

// An approval needs a title, a description of at most 600 characters and what its type needs; one that fails
// validation stores nothing and notifies nobody.
func TestRaiseApprovalRejectsIncompleteApprovals(t *testing.T) {
	h := newHarness(t)
	h.start()

	noDesc := mcpSpec()
	noDesc.Description = " \n"
	tooLong := mcpSpec()
	tooLong.Description = strings.Repeat("é", store.MaxApprovalDescription+1)
	noTitle := mcpSpec()
	noTitle.Title = ""
	noRaiser := mcpSpec()
	noRaiser.Origin = ""
	toolNoPerm := mcpSpec()
	toolNoPerm.Type, toolNoPerm.AgentRunID = store.ApprovalToolCall, "x"
	propNoRun := mcpSpec()
	propNoRun.Type, propNoRun.Proposal = store.ApprovalProposal, "plan"

	for name, tc := range map[string]struct {
		spec engine.ApprovalSpec
		want error
	}{
		"no description":                   {noDesc, engine.ErrDescriptionRequired},
		"description too long":             {tooLong, store.ErrInvalid},
		"no title":                         {noTitle, store.ErrInvalid},
		"no origin or run":                 {noRaiser, store.ErrInvalid},
		"tool call, no permission request": {toolNoPerm, store.ErrInvalid},
		"proposal, no agent run":           {propNoRun, store.ErrInvalid},
	} {
		if _, err := h.eng.RaiseApproval(t.Context(), tc.spec); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	if page, err := h.st.ListApprovals(t.Context(), store.ApprovalFilter{}); err != nil || len(page.Items) != 0 {
		t.Errorf("rejected approvals were stored: %+v, %v", page.Items, err)
	}
	if n := len(h.notes.all()); n != 0 {
		t.Errorf("rejected approvals sent %d notifications", n)
	}
	if !strings.Contains(engine.ErrDescriptionRequired.Error(), "description is required: say in one or two sentences what you want to do and why") {
		t.Errorf("ErrDescriptionRequired = %q", engine.ErrDescriptionRequired)
	}
}

// Secrets never reach the stored approval or its notification; the notification is built from the title and
// description with the approval's priority; the waiter hears the decision with its steering comment.
func TestRaiseApprovalRedactsNotifiesAndAnswersTheWaiter(t *testing.T) {
	const secret = "s3cr3t-token-value"
	h := newHarness(t)
	h.env("API_TOKEN=" + secret + "\n")
	h.start()

	s := mcpSpec()
	s.Title, s.Description = "Use "+secret+"?", "Call the API with "+secret+"."
	s.BodyMD, s.Command = "token "+secret, "curl -H "+secret
	s.Fields = []store.RequestField{{Key: "k", Value: secret}}
	s.Priority, s.Wait = store.PriorityHigh, true
	r, err := h.eng.RaiseApproval(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Release()

	got, err := h.st.GetApproval(t.Context(), r.Approval.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blob := strings.Join([]string{got.Title, got.Description, got.Details.BodyMD, got.Details.Command, got.Details.Fields[0].Value}, "|"); strings.Contains(blob, secret) {
		t.Errorf("the stored approval holds the secret: %s", blob)
	}
	if got.Origin != store.OriginMCP || got.OriginLabel != "MCP: e2e-client" || got.DescriptionBy != store.DescByHal || got.ConversationID != "" || got.Status != store.ApprovalPending {
		t.Errorf("stored approval = %+v", got)
	}

	notes := h.notes.kind("request")
	if len(notes) != 1 {
		t.Fatalf("notifications = %+v", h.notes.all())
	}
	n := notes[0]
	if strings.Contains(n.Subtitle+n.Body, secret) || n.Priority != "high" || n.ApprovalID != got.ID || n.FlowID != "mcp" || !strings.Contains(n.Title, "e2e-client") || !strings.HasSuffix(n.URL, "/inbox/approval/"+got.ID) {
		t.Errorf("notification = %+v", n)
	}

	if _, err := h.eng.DecideApproval(t.Context(), got.ID, core.ApprovalDecision{Principal: "me", Comment: "use the staging key"}); err != nil {
		t.Fatal(err)
	}
	d := <-r.Decision
	if d.Verdict != agent.VerdictDeny || d.Comment != "use the staging key" || d.Held {
		t.Errorf("decision = %+v", d)
	}

	// A gated MCP approval whose call stopped waiting can no longer be decided: nobody would hear it.
	gated := mcpSpec()
	gated.Wait = true
	r2, err := h.eng.RaiseApproval(t.Context(), gated)
	if err != nil {
		t.Fatal(err)
	}
	r2.Release()
	if _, err := h.eng.DecideApproval(t.Context(), r2.Approval.ID, core.ApprovalDecision{Approve: true, Principal: "me"}); !errors.Is(err, core.ErrInvalidState) {
		t.Errorf("deciding an approval nobody waits on: %v", err)
	}
	// A detached one outlives its call.
	detached := mcpSpec()
	detached.Meta = store.RequestApproval{Detached: true}
	r3, err := h.eng.RaiseApproval(t.Context(), detached)
	if err != nil {
		t.Fatal(err)
	}
	if ap, err := h.eng.DecideApproval(t.Context(), r3.Approval.ID, core.ApprovalDecision{Approve: true, Principal: "me"}); err != nil || ap.Status != store.ApprovalApproved {
		t.Errorf("deciding a detached approval: %+v, %v", ap, err)
	}
}

var createApprovalCall = regexp.MustCompile(`\bCreateApproval\(`)

// store.CreateApproval is private to RaiseApproval: no code outside the store package and approvals_raise.go calls it.
func TestOnlyRaiseApprovalCreatesApprovals(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			if rel == filepath.Join("internal", "store") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || rel == filepath.Join("internal", "engine", "approvals_raise.go") {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // walking the repo's own sources
		if err != nil {
			return err
		}
		if createApprovalCall.Match(b) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("these files create approvals without RaiseApproval: %v", offenders)
	}
}
