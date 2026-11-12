package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// submitCall has the fake agent call a hal output tool like a runtime would.
func submitCall(id, args string) step { return toolCall("submit_output", id, args) }

func toolCall(tool, id, args string) step {
	var a any
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		panic(err)
	}
	return step{Op: fakeagent.OpMCPCall, ID: id, Tool: tool, Args: a}
}

func atTurn(n int) *fakeagent.When { return &fakeagent.When{SessionTurn: &n} }

func promptsOf(d *Daemon) []string {
	var out []string
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/prompt" {
			out = append(out, c["text"].(string))
		}
	}
	return out
}

// output: note keeps working: a valid submit_output call is used without parsing the text; without the call the final
// message is parsed as before and no follow-up is sent; unparseable text still gives the flagged item.
func TestSubmitOutputNoteModeCompatibility(t *testing.T) {
	note := `{"title":"From the tool","priority":"low","summary_md":"s"}`
	for _, tc := range []struct {
		name     string
		steps    []step
		title    string
		parseErr bool
	}{
		{"valid call is used", []step{submitCall("s1", note), say("no json here")}, "From the tool", false},
		{"invalid call falls back to the text", []step{submitCall("s1", `{"title":"x"}`), say(`final {"title":"From text","priority":"high","summary_md":"t"}`)}, "From text", false},
		{"no call parses the text", []step{say(`final {"title":"From text","priority":"high","summary_md":"t"}`)}, "From text", false},
		{"no call and unparseable text", []step{say("sorry, no JSON")}, "Unstructured agent result: f/work", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDaemon(t)
			d.IdleSensor("src")
			d.Runtime("fake", script{Turns: []turn{{Steps: tc.steps}}})
			d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: Triage it.
`)
			d.Start()
			d.RunOf("src", map[string]any{"id": "1"})
			items := d.Inbox()
			if len(items) != 1 || items[0].Title != tc.title || items[0].ParseError != tc.parseErr {
				t.Fatalf("inbox: %+v", items)
			}
			if got := len(promptsOf(d)); got != 1 {
				t.Errorf("prompts = %d, want 1 (output: note never sends a follow-up)", got)
			}
		})
	}
}

// Behavior change: a readonly agent used to parse its final message as a note whatever its output. Only output: note
// (the default of inline instructions, or a prompt file with output: note) produces an inbox item.
func TestReadonlyForcesNoNoteUnlessOutputNote(t *testing.T) {
	t.Run("inline none", func(t *testing.T) {
		d := newDaemon(t)
		d.IdleSensor("src")
		d.Runtime("fake", script{Turns: []turn{{Steps: []step{say("free text")}}}})
		d.Flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: work\n    type: agent\n    runtime: fake\n    mode: readonly\n    instructions: Look.\n    output: none\n")
		d.Start()
		d.RunOf("src", map[string]any{"id": "1"})
		if items := d.Inbox(); len(items) != 0 {
			t.Fatalf("a readonly agent with output: none made inbox items: %+v", items)
		}
	})
	t.Run("prompt file with output none", func(t *testing.T) {
		d := newDaemon(t)
		d.IdleSensor("src")
		d.Prompt("p", "none", "Look at {{.input.id}}")
		d.Runtime("fake", script{Turns: []turn{{Steps: []step{say("free text")}}}})
		d.Flow("f", readonlyFlow(""))
		d.Start()
		d.RunOf("src", map[string]any{"id": "1"})
		if items := d.Inbox(); len(items) != 0 {
			t.Fatalf("inbox: %+v", items)
		}
	})
}

// submit_output belongs to the run of a node with output: note: a node with output none gets "unknown tool" and its
// prompt says nothing about the tool.
func TestSubmitOutputIsScopedToNoteNodes(t *testing.T) {
	note := `{"title":"T","summary_md":"s"}`
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("noted", script{Turns: []turn{{Steps: []step{submitCall("s1", note), say("ok")}}}})
	d.Runtime("plain", script{Turns: []turn{{Steps: []step{submitCall("s2", note), say("ok")}}}})
	d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: first
    type: agent
    runtime: noted
    mode: readonly
    instructions: One.
  - id: second
    type: agent
    runtime: plain
    mode: readonly
    instructions: Two.
    output: none
edges:
  - {from: first, to: second}
`)
	d.Start()
	run := d.RunOf("src", map[string]any{"id": "1"})
	first := d.AgentRuns(d.Node(run, "first").ID)[0]
	second := d.AgentRuns(d.Node(run, "second").ID)[0]
	if !strings.Contains(string(first.Output), `"title":"T"`) {
		t.Errorf("first output %s", first.Output)
	}
	if tr := d.Transcript(second.ID); !strings.Contains(tr, "mcp[s2] is_error=true") || !strings.Contains(tr, "unknown tool") {
		t.Errorf("output none exposes submit_output:\n%s", tr)
	}
	if string(second.Output) != "null" {
		t.Errorf("second output %s", second.Output)
	}
	if strings.Contains(second.RenderedPrompt, "submit_output") {
		t.Errorf("second prompt mentions submit_output:\n%s", second.RenderedPrompt)
	}
	if !strings.Contains(first.RenderedPrompt, "- submit_output:") {
		t.Errorf("first prompt does not list submit_output:\n%s", first.RenderedPrompt)
	}
}

// A dry run follows the note semantics: the inbox item made from the call is tagged dry-run.
func TestSubmitOutputDryRunItemIsTagged(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: []turn{{Steps: []step{
		submitCall("s1", `{"title":"Would alert","summary_md":"s"}`), say("x"),
	}}}})
	d.Flow("f", `id: f
enabled: true
dry_run: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: auto
    instructions: Triage it.
`)
	d.Start()
	d.RunOf("src", map[string]any{"id": "1"})
	items := d.Inbox()
	if len(items) != 1 || items[0].Title != "Would alert" || !items[0].DryRun || items[0].Priority != "normal" {
		t.Fatalf("inbox: %+v", items)
	}
}

const interactiveNote = `{"title":"Pick a fix","summary_md":"s",` +
	`"questions":[{"id":"strategy","header":"Strategy","question":"Which fix?","options":[{"label":"Revert"},{"label":"Patch","description":"Smaller"}],"recommended":1}],` +
	`"actions":[{"label":"Draft PR","prompt":"Draft the PR."}]}`

func checkInteractiveItem(t *testing.T, it InboxItem) {
	t.Helper()
	if it.AgentRunID == "" {
		t.Errorf("the item has no agent run: %+v", it)
	}
	if len(it.Questions) != 1 || it.Questions[0].ID != "strategy" || it.Questions[0].Header != "Strategy" || len(it.Questions[0].Options) != 2 ||
		it.Questions[0].Recommended == nil || *it.Questions[0].Recommended != 1 {
		t.Errorf("questions: %+v", it.Questions)
	}
	if len(it.Actions) != 1 || it.Actions[0].Type != "prompt" || it.Actions[0].Label != "Draft PR" || it.Actions[0].Prompt != "Draft the PR." {
		t.Errorf("actions: %+v", it.Actions)
	}
}

// A note with questions and actions: submit_output refuses an invalid one with the reason, the corrected call is the
// inbox item, which holds them and the agent run.
func TestSubmitOutputNoteWithQuestionsAndActions(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: []turn{{Steps: []step{
		submitCall("bad", `{"title":"T","summary_md":"s","questions":[{"id":"a","question":"Q?","options":[{"label":"x"},{"label":"y"}],"recommended":2}]}`),
		submitCall("good", interactiveNote),
		say("done"),
	}}}})
	d.Flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: work\n    type: agent\n    runtime: fake\n    mode: readonly\n    instructions: Triage it.\n")
	d.Start()
	run := d.RunOf("src", map[string]any{"id": "1"})
	items := d.Inbox()
	if len(items) != 1 || items[0].Title != "Pick a fix" {
		t.Fatalf("inbox: %+v", items)
	}
	checkInteractiveItem(t, items[0])
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	if items[0].AgentRunID != ar.ID {
		t.Errorf("agent run %q, want %q", items[0].AgentRunID, ar.ID)
	}
	if tr := d.Transcript(ar.ID); !strings.Contains(tr, "mcp[bad] is_error=true") || !strings.Contains(tr, "recommended") || !strings.Contains(tr, "mcp[good] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
}

// The text-parse path takes questions and actions too.
func TestNoteQuestionsFromFinalMessage(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: []turn{{Steps: []step{say("final " + interactiveNote)}}}})
	d.Flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: work\n    type: agent\n    runtime: fake\n    mode: readonly\n    instructions: Triage it.\n")
	d.Start()
	d.RunOf("src", map[string]any{"id": "1"})
	items := d.Inbox()
	if len(items) != 1 || items[0].ParseError {
		t.Fatalf("inbox: %+v", items)
	}
	checkInteractiveItem(t, items[0])
}

// An inbox shortcut note node takes questions and actions and the agent run; a titled inbox node and an inbox node
// without an agent never get them; a port with its own schema may use a field called questions for anything.
func TestShortcutNoteQuestionsAndActions(t *testing.T) {
	flow := `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: Triage {{.input.id}}
    ports:
      plain: {description: Tell me.}
      mine: {description: Tell me my way.}
      custom:
        description: Own schema.
        schema: {type: object, required: [title], properties: {title: {type: string}, questions: {type: string}}}
  - {id: note, type: inbox}
  - {id: titled, type: inbox, title: "Mine: {{.input.agent.output.title}}"}
  - {id: own, type: inbox, title: "Own: {{.input.agent.output.questions}}"}
edges:
  - {from: work, port: plain, to: note}
  - {from: work, port: mine, to: titled}
  - {from: work, port: custom, to: own}
`
	t.Run("note node", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{
			toolCall("emit_plain", "bad", `{"title":"T","summary_md":"s","actions":[{"label":"x","prompt":""}]}`),
			toolCall("emit_plain", "ok", interactiveNote),
			say("x"),
		}})
		run := d.RunOf("src", map[string]any{"id": "7"})
		items := d.Inbox()
		if len(items) != 1 || items[0].Title != "Pick a fix" {
			t.Fatalf("inbox: %+v", items)
		}
		checkInteractiveItem(t, items[0])
		ar := d.AgentRuns(d.Node(run, "work").ID)[0]
		if items[0].AgentRunID != ar.ID || items[0].Source != "node_run" {
			t.Errorf("source %q agent run %q, want node_run and %q", items[0].Source, items[0].AgentRunID, ar.ID)
		}
		if tr := d.Transcript(ar.ID); !strings.Contains(tr, "mcp[bad] is_error=true") || !strings.Contains(tr, "mcp[ok] is_error=false") {
			t.Errorf("transcript:\n%s", tr)
		}
	})
	t.Run("titled node", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{toolCall("emit_mine", "ok", interactiveNote), say("x")}})
		d.RunOf("src", map[string]any{"id": "7"})
		items := d.Inbox()
		if len(items) != 1 || items[0].Title != "Mine: Pick a fix" || len(items[0].Questions) != 0 || len(items[0].Actions) != 0 || items[0].AgentRunID != "" {
			t.Fatalf("inbox: %+v", items)
		}
	})
	t.Run("custom schema is not checked", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{toolCall("emit_custom", "ok", `{"title":"T","questions":"free text"}`), say("x")}})
		d.RunOf("src", map[string]any{"id": "7"})
		items := d.Inbox()
		if len(items) != 1 || items[0].Title != "Own: free text" || len(items[0].Questions) != 0 {
			t.Fatalf("inbox: %+v", items)
		}
	})
}
