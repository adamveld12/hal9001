package api

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/inboxact"
	"hal9001/internal/store"
)

// inboxFixture is a note an agent wrote (two questions, two actions) behind the real router and the real interaction
// service; the assist manager is a double that records the forks and the messages it is sent. The tests play the part of
// the manager's turn observer.
type inboxFixture struct {
	t    *testing.T
	e    *env
	svc  *inboxact.Service
	item *store.InboxItem
	conv *store.AssistSession

	mu      sync.Mutex
	forks   int
	sent    []string
	sendErr error
}

type noteJSON struct {
	CanReply       bool    `json:"can_reply"`
	ConversationID *string `json:"conversation_id"`
	Questions      []struct {
		ID     string `json:"id"`
		Answer *struct {
			QuestionID string   `json:"question_id"`
			Choices    []string `json:"choices"`
			Other      *string  `json:"other"`
		} `json:"answer"`
	} `json:"questions"`
	Actions []struct {
		Label string `json:"label"`
		Used  bool   `json:"used"`
	} `json:"actions"`
	Interactions []struct {
		Kind       string  `json:"kind"`
		Summary    string  `json:"summary"`
		Message    string  `json:"message"`
		Status     string  `json:"status"`
		ApprovalID *string `json:"approval_id"`
		Result     *string `json:"result"`
		Error      *string `json:"error"`
	} `json:"interactions"`
}

func newInboxFixture(t *testing.T) *inboxFixture {
	t.Helper()
	f := &inboxFixture{t: t}
	dbl := &assistDouble{
		fork: func(context.Context, string) (*store.AssistSession, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.forks++
			return f.conv, nil
		},
		send: func(_ context.Context, id, text string, _ *string, _ *core.AssistPageContext) (*store.AssistSession, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.sendErr != nil {
				return nil, f.sendErr
			}
			f.sent = append(f.sent, id+": "+text)
			return f.conv, nil
		},
	}
	f.e = newEnv(t, func(o *envOptions) {
		o.assist = dbl
		o.inbox = func(e *env) *inboxact.Service {
			f.svc = &inboxact.Service{Store: e.st, Assist: dbl}
			return f.svc
		}
	})
	f.conv = f.e.convChat(store.AssistTargetGeneral, "fork", "idle", time.Now().UTC())
	fr := f.e.flowRun("demo", "", store.KindLive, store.FlowRunSucceeded)
	nr := f.e.nodeRun(fr.ID, "review", store.NodeDone, func(n *store.NodeRun) { n.NodeType = store.NodeAgent })
	ar := f.e.agentRun(nr.ID, store.AgentSucceeded)
	two := func(a, b string) []store.InboxOption {
		return []store.InboxOption{{Label: a}, {Label: b, Description: "d"}}
	}
	f.item = &store.InboxItem{
		SourceType: store.SourceAgentRun, AgentRunID: ar.ID, Title: "Merge PR 7", SummaryMD: "s",
		Questions: []store.InboxQuestion{
			{ID: "method", Question: "How?", Options: two("Squash", "Merge commit")},
			{ID: "checks", Question: "Which checks?", Options: two("Lint", "Tests"), Multi: true},
			{ID: "notify", Question: "Tell whom?", Options: two("Team", "Nobody")},
		},
		Actions: []store.InboxAction{
			{Type: "prompt", Label: "Merge it", Prompt: "Merge the PR now."},
			{Type: "prompt", Label: "Close it", Prompt: "Close the PR."},
		},
	}
	must(t, f.e.st.CreateInboxItem(ctx(), f.item))
	return f
}

func (f *inboxFixture) path(suffix string) string { return "/api/inbox/" + f.item.ID + suffix }

func (f *inboxFixture) get() noteJSON {
	f.t.Helper()
	var n noteJSON
	f.e.get(f.path("")).want(200).decode(&n)
	return n
}

func (f *inboxFixture) answer(answers ...map[string]any) *resp {
	f.t.Helper()
	return f.e.do("POST", f.path("/answers"), map[string]any{"answers": answers})
}

func (f *inboxFixture) action(i int) *resp {
	return f.e.do("POST", f.path(fmt.Sprintf("/actions/%d", i)), nil)
}

func (f *inboxFixture) reply(text string) *resp {
	return f.e.do("POST", f.path("/reply"), map[string]any{"text": text})
}

func (f *inboxFixture) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func (f *inboxFixture) forkCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forks
}

// finish plays the end of the turn the last message started.
func (f *inboxFixture) finish(result string) { f.svc.TurnEnded(f.conv.ID, false, "", result) }

func TestInboxInteractionsForkOnceAndSendTheSpecifiedMessages(t *testing.T) {
	f := newInboxFixture(t)
	if n := f.get(); !n.CanReply || n.ConversationID != nil || len(n.Interactions) != 0 || len(n.Questions) != 3 || len(n.Actions) != 2 {
		t.Fatalf("fresh note = %+v", n)
	}

	// Answers: one line per question in question order, however the request ordered them; "Other" last.
	r := f.answer(
		map[string]any{"question_id": "checks", "choices": []string{"Lint", "Tests"}, "other": "  and e2e "},
		map[string]any{"question_id": "method", "choices": []string{"Squash"}},
	).want(202)
	var n noteJSON
	r.decode(&n)
	wantMsg := "My answers to your questions:\n- method: Squash\n- checks: Lint, Tests, Other: and e2e"
	if n.ConversationID == nil || *n.ConversationID != f.conv.ID || len(n.Interactions) != 1 || n.Interactions[0].Message != wantMsg ||
		n.Interactions[0].Status != "running" || n.Interactions[0].Summary != "Answers" || n.Interactions[0].Result != nil || n.Interactions[0].ApprovalID != nil {
		t.Fatalf("after answers = %+v", n)
	}
	if a := n.Questions[0].Answer; a == nil || a.QuestionID != "method" || !slices.Equal(a.Choices, []string{"Squash"}) || a.Other != nil {
		t.Errorf("answer of method = %+v", a)
	}
	if a := n.Questions[1].Answer; a == nil || !slices.Equal(a.Choices, []string{"Lint", "Tests"}) || a.Other == nil || *a.Other != "and e2e" {
		t.Errorf("answer of checks = %+v", a)
	}
	if n.Questions[2].Answer != nil {
		t.Errorf("an unanswered question has an answer: %+v", n.Questions[2].Answer)
	}

	// The next interaction waits for the turn.
	f.action(0).want(409)
	f.finish("# Done\nsecond line")
	n = f.get()
	if got := n.Interactions[0]; got.Status != "done" || got.Result == nil || *got.Result != "Done" {
		t.Fatalf("finished answers = %+v", got)
	}

	// A question can be answered once; the unanswered one still can (a free-text answer alone is fine).
	f.answer(map[string]any{"question_id": "method", "choices": []string{"Merge commit"}}).want(409)
	f.answer(map[string]any{"question_id": "notify", "other": "the on-call"}).want(202)
	f.finish("ok")

	// Action: the prompt verbatim; the second use is refused even though the first finished.
	f.action(1).want(202)
	f.finish("Closed.")
	f.action(1).want(409)
	if n = f.get(); n.Actions[0].Used || !n.Actions[1].Used {
		t.Errorf("used flags = %+v", n.Actions)
	}

	// Replies: any number, text verbatim.
	f.reply("  thanks!\nbye ").want(202)
	f.finish("You're welcome")

	if f.forkCount() != 1 {
		t.Errorf("forks = %d, want 1", f.forkCount())
	}
	c := f.conv.ID + ": "
	want := []string{c + wantMsg, c + "My answers to your questions:\n- notify: Other: the on-call", c + "Close the PR.", c + "  thanks!\nbye "}
	if got := f.messages(); !slices.Equal(got, want) {
		t.Errorf("messages = %q, want %q", got, want)
	}
	n = f.get()
	var kinds []string
	for _, in := range n.Interactions {
		kinds = append(kinds, in.Kind+"/"+in.Summary+"/"+in.Status)
	}
	if want := []string{"answers/Answers/done", "answers/Answers/done", "action/Action: Close it/done", "reply/Reply/done"}; !slices.Equal(kinds, want) {
		t.Errorf("interactions = %v, want %v", kinds, want)
	}
	if n.ConversationID == nil || *n.ConversationID != f.conv.ID {
		t.Errorf("conversation id = %v", n.ConversationID)
	}
}

func TestInboxInteractionFollowsTheTurn(t *testing.T) {
	f := newInboxFixture(t)
	f.action(0).want(202)
	approval := f.e.st.NewID()

	f.svc.TurnWaiting(f.conv.ID, approval)
	in := f.get().Interactions[0]
	if in.Status != "waiting_for_approval" || in.ApprovalID == nil || *in.ApprovalID != approval {
		t.Fatalf("waiting = %+v", in)
	}
	f.action(1).want(409) // still the same turn

	f.svc.TurnResumed(f.conv.ID)
	if in = f.get().Interactions[0]; in.Status != "running" || in.ApprovalID != nil {
		t.Fatalf("resumed = %+v", in)
	}

	f.svc.TurnEnded(f.conv.ID, true, "the turn timed out", "")
	if in = f.get().Interactions[0]; in.Status != "failed" || in.Error == nil || *in.Error != "the turn timed out" || in.Result != nil {
		t.Fatalf("failed = %+v", in)
	}
	// A turn nobody asked for (the user chatting in the fork) does not touch the settled interaction.
	f.svc.TurnEnded(f.conv.ID, false, "", "late")
	if in = f.get().Interactions[0]; in.Status != "failed" {
		t.Errorf("a settled interaction changed: %+v", in)
	}
	// A failed interaction does not block the next one, but its action stays used.
	f.action(0).want(409)
	f.reply("try again").want(202)
	f.finish("worked")
	if in = f.get().Interactions[1]; in.Status != "done" || in.Result == nil || *in.Result != "worked" {
		t.Errorf("retry = %+v", in)
	}
}

func TestInboxSimultaneousCallsSendOnce(t *testing.T) {
	f := newInboxFixture(t)
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = f.action(0).Status
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case 202:
			ok++
		case 409:
			conflict++
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Errorf("statuses = %v, want one 202 and %d 409", codes, n-1)
	}
	if f.forkCount() != 1 || len(f.messages()) != 1 {
		t.Errorf("forks = %d, messages = %q, want one each", f.forkCount(), f.messages())
	}
}

func TestInboxInteractionRefusals(t *testing.T) {
	f := newInboxFixture(t)
	bad := func(what string, r *resp, code int) {
		t.Helper()
		if r.Status != code {
			t.Errorf("%s: status %d, want %d: %s", what, r.Status, code, r.Body)
		}
	}
	bad("unknown question", f.answer(map[string]any{"question_id": "nope", "choices": []string{"Squash"}}), 422)
	bad("two choices for a single question", f.answer(map[string]any{"question_id": "method", "choices": []string{"Squash", "Merge commit"}}), 422)
	bad("label that is not an option", f.answer(map[string]any{"question_id": "method", "choices": []string{"squash"}}), 422)
	bad("nothing chosen", f.answer(map[string]any{"question_id": "checks"}), 422)
	bad("blank free text", f.answer(map[string]any{"question_id": "method", "other": "  "}), 422)
	bad("choice and free text for a single question", f.answer(map[string]any{"question_id": "method", "choices": []string{"Squash"}, "other": "x"}), 422)
	bad("question twice", f.answer(map[string]any{"question_id": "method", "other": "a"}, map[string]any{"question_id": "method", "other": "b"}), 422)
	bad("empty reply", f.reply("  \n"), 422)
	bad("action out of range", f.action(2), 404)
	bad("unknown note", f.e.do("POST", "/api/inbox/01JC7Z3K9Q8V5T2M4N6P8R0S1T/reply", map[string]any{"text": "x"}), 404)
	if f.forkCount() != 0 || len(f.messages()) != 0 {
		t.Errorf("a refused call forked or sent: forks %d, messages %q", f.forkCount(), f.messages())
	}

	// A message that cannot be sent leaves the action available.
	f.mu.Lock()
	f.sendErr = fmt.Errorf("%w: the conversation is closed", core.ErrInvalidState)
	f.mu.Unlock()
	bad("closed conversation", f.action(0), 409)
	if n := f.get(); n.Actions[0].Used || len(n.Interactions) != 0 {
		t.Errorf("an unsent action was recorded: %+v", n)
	}
	f.mu.Lock()
	f.sendErr = nil
	f.mu.Unlock()
	bad("action after the failed send", f.action(0), 202)

	// A note without an agent run has nobody to talk to.
	mcp := &store.InboxItem{SourceType: store.SourceMCP, Client: "some-client", Title: "from outside"}
	must(t, f.e.st.CreateInboxItem(ctx(), mcp))
	bad("note of an MCP client", f.e.do("POST", "/api/inbox/"+mcp.ID+"/reply", map[string]any{"text": "hi"}), 409)
	var n noteJSON
	f.e.get("/api/inbox/" + mcp.ID).want(200).decode(&n)
	if n.CanReply || len(n.Questions) != 0 || len(n.Actions) != 0 || len(n.Interactions) != 0 || n.ConversationID != nil {
		t.Errorf("MCP note = %+v", n)
	}

	// Without the service the operations are unavailable.
	newEnv(t).do("POST", "/api/inbox/"+mcp.ID+"/reply", map[string]any{"text": "hi"}).want(503)
}
