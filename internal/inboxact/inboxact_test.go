package inboxact

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/store"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Interactions that were running when the daemon stopped can never finish: Start ends them as failed and leaves the
// finished ones alone.
func TestStartFailsInteractionsOfTheStoppedDaemon(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "hal.db"))
	must(t, err)
	t.Cleanup(func() { _ = st.Close() })

	fr := &store.FlowRun{FlowID: "f", FlowHash: "h", FlowYAML: "id: f", Kind: store.KindLive, Status: store.FlowRunSucceeded}
	must(t, st.CreateFlowRun(ctx, fr))
	nr := &store.NodeRun{FlowRunID: fr.ID, NodeID: "work", NodeType: store.NodeAgent, Status: store.NodeDone}
	must(t, st.CreateNodeRun(ctx, nr))
	ar := &store.AgentRun{NodeRunID: nr.ID, Runtime: "fake", Mode: store.ModeAuto, Policy: store.PolicyAuto, Phase: store.PhaseExecute, Status: store.AgentSucceeded, Prompt: store.Inline("p")}
	must(t, st.CreateAgentRun(ctx, ar))
	conv := &store.AssistSession{TargetKind: store.AssistTargetGeneral, Runtime: "fake", Status: store.AssistIdle, DraftHash: "h", DraftUpdatedBy: store.DraftByUser}
	must(t, st.CreateAssistSession(ctx, conv))
	it := &store.InboxItem{SourceType: store.SourceAgentRun, AgentRunID: ar.ID, Title: "note"}
	must(t, st.CreateInboxItem(ctx, it))

	svc := &Service{Store: st}
	first := &store.InboxInteraction{ItemID: it.ID, ConversationID: conv.ID, Kind: store.InteractionReply, Message: "one"}
	must(t, st.StartInboxInteraction(ctx, first))
	svc.TurnEnded(conv.ID, false, "", "fine")
	second := &store.InboxInteraction{ItemID: it.ID, ConversationID: conv.ID, Kind: store.InteractionReply, Message: "two"}
	must(t, st.StartInboxInteraction(ctx, second))
	svc.TurnWaiting(conv.ID, "approval")

	must(t, svc.Start(ctx))
	got, err := st.ListInboxInteractions(ctx, it.ID)
	must(t, err)
	if len(got) != 2 || got[0].Status != store.InteractionDone || got[0].Result != "fine" ||
		got[1].Status != store.InteractionFailed || got[1].Error != "the daemon stopped" {
		t.Fatalf("interactions = %+v", got)
	}
	// Nothing is active any more, so the note takes the next message.
	must(t, st.StartInboxInteraction(ctx, &store.InboxInteraction{ItemID: it.ID, ConversationID: conv.ID, Kind: store.InteractionReply, Message: "three"}))
}

func TestOneLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"\n\n  Merged the PR.\nMore.", "Merged the PR."},
		{"## **Done** with `x`\nbody", "Done with x"},
		{"- first item\n- second", "first item"},
		{strings.Repeat("é", 200), strings.Repeat("é", MaxResultRunes-1) + "…"},
	} {
		if got := OneLine(tc.in); got != tc.want {
			t.Errorf("OneLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
