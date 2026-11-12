package e2e

import (
	"slices"
	"strings"
	"testing"
)

type inboxNoteJSON struct {
	ConversationID *string `json:"conversation_id"`
	CanReply       bool    `json:"can_reply"`
	Questions      []struct {
		ID     string `json:"id"`
		Answer *struct {
			Choices []string `json:"choices"`
		} `json:"answer"`
	} `json:"questions"`
	Actions []struct {
		Used bool `json:"used"`
	} `json:"actions"`
	Interactions []struct {
		Kind    string  `json:"kind"`
		Message string  `json:"message"`
		Status  string  `json:"status"`
		Result  *string `json:"result"`
	} `json:"interactions"`
}

// A note with questions and an action: answering forks the agent's conversation, the agent gets the answers as one
// message and the interaction ends with a one-line result; the action and a reply go to the same fork.
func TestInboxNoteQuestionsActionsAndReply(t *testing.T) {
	d := newDaemon(t)
	d.InstallCLI()
	d.IdleSensor("src")
	note := `{"title":"Merge PR 7","priority":"normal","summary_md":"Ready.","questions":[{"id":"merge_method","question":"How should I merge?","options":[{"label":"Squash"},{"label":"Merge commit","description":"keeps history"}],"recommended":0}],"actions":[{"type":"prompt","label":"Close it","prompt":"Close the PR instead."}]}`
	d.Runtime("fake", script{LoadSession: true, Turns: []turn{
		{When: when("merge_method: Merge commit$"), Steps: []step{say("**Merged** with a merge commit.\nDetails follow.")}},
		{When: when("Close the PR instead\\.$"), Steps: []step{say("Closed the PR.")}},
		{When: when("any news\\?$"), Steps: []step{say("No news.")}},
		{When: when("Triage"), Steps: []step{submitCall("s1", note), say("Posted the note.")}},
	}})
	d.Flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: work\n    type: agent\n    runtime: fake\n    mode: readonly\n    instructions: Triage it.\n")
	d.Start()
	d.RunOf("src", map[string]any{"id": "7"})
	items := d.Inbox()
	if len(items) != 1 || items[0].AgentRunID == "" || len(items[0].Questions) != 1 || len(items[0].Actions) != 1 {
		t.Fatalf("inbox: %+v", items)
	}
	path := "/api/inbox/" + items[0].ID
	get := func() inboxNoteJSON {
		var n inboxNoteJSON
		d.Get(path).Want(200).JSON(&n)
		return n
	}
	settle := func(n int) inboxNoteJSON {
		t.Helper()
		var got inboxNoteJSON
		d.Eventually("interaction to finish", func() bool {
			got = get()
			return len(got.Interactions) == n && got.Interactions[n-1].Status != "running"
		})
		return got
	}

	d.Post(path+"/answers", map[string]any{"answers": []map[string]any{{"question_id": "merge_method", "choices": []string{"Merge commit"}}}}).Want(202)
	n := settle(1)
	in := n.Interactions[0]
	if in.Kind != "answers" || in.Message != "My answers to your questions:\n- merge_method: Merge commit" || in.Status != "done" || in.Result == nil || *in.Result != "Merged with a merge commit." {
		t.Fatalf("answers interaction = %+v", in)
	}
	if n.ConversationID == nil || n.Questions[0].Answer == nil || !slices.Equal(n.Questions[0].Answer.Choices, []string{"Merge commit"}) {
		t.Fatalf("note after answering = %+v", n)
	}
	fork := *n.ConversationID
	d.Post(path+"/answers", map[string]any{"answers": []map[string]any{{"question_id": "merge_method", "choices": []string{"Squash"}}}}).Want(409)

	d.Post(path+"/actions/0", nil).Want(202)
	n = settle(2)
	if n.Interactions[1].Kind != "action" || n.Interactions[1].Message != "Close the PR instead." || n.Interactions[1].Status != "done" || !n.Actions[0].Used {
		t.Fatalf("action = %+v", n)
	}
	d.Post(path+"/actions/0", nil).Want(409)

	d.Post(path+"/reply", map[string]any{"text": "any news?"}).Want(202)
	n = settle(3)
	if n.Interactions[2].Kind != "reply" || n.Interactions[2].Status != "done" || *n.Interactions[2].Result != "No news." || *n.ConversationID != fork {
		t.Fatalf("reply = %+v", n)
	}

	var forks int
	for _, id := range d.conversationIDs("?source=chat") {
		if c := d.conversation(id); c.ForkedFrom != nil && c.ForkedFrom.AgentRunID == items[0].AgentRunID {
			forks++
		}
	}
	if forks != 1 {
		t.Errorf("forks of the note's agent run = %d, want 1", forks)
	}
	prompts := d.agentPrompts()
	if len(prompts) != 4 || !strings.HasSuffix(prompts[1], "merge_method: Merge commit") {
		t.Errorf("prompts = %q", prompts)
	}
}
