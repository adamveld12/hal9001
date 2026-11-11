package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// reviewSchema is the output_schema of the review example in docs/flows.md.
const reviewSchema = `    output: tool
    output_schema:
      type: object
      required: [priority, verdict, body]
      properties:
        priority: {enum: [urgent, high, normal, low]}
        linear_urls: {type: array, items: {type: string}}
        verdict: {type: string}
        body: {type: string}
`

const validReview = `{"priority":"urgent","verdict":"block","body":"Rollback needed.","linear_urls":["https://linear.app/x/1","https://linear.app/x/2"]}`

// submitCall has the fake agent call hal's submit_output tool like a runtime would.
func submitCall(id, args string) step {
	var a any
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		panic(err)
	}
	return step{Op: fakeagent.OpMCPCall, ID: id, Tool: "submit_output", Args: a}
}

// sameReview compares got with validReview by value (the agent's arguments pass through a map on their way to hal).
func sameReview(t *testing.T, got json.RawMessage) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(validReview), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	return string(gb) == string(wb)
}

func atTurn(n int) *fakeagent.When { return &fakeagent.When{SessionTurn: &n} }

// agentOutputFlow is one readonly agent node `work` (inline instructions, the review output_schema) with an `error` port
// routed to the inbox, and a command node `fmt` that reads .agent.output before an inbox note.
func agentOutputFlow() string {
	return `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: |
      Review {{.input.id}}
` + reviewSchema + `
  - id: fmt
    type: command
    lang: jq
    script: |
      {line: "\(.agent.output.priority) \(.agent.output.verdict): \(.agent.output.linear_urls | length) links"}
  - {id: shown, type: inbox, title: "{{.input.line}}"}
  - {id: failed, type: inbox, title: "failed: {{.input.error}}"}
edges:
  - {from: work, to: fmt}
  - {from: fmt, to: shown}
  - {from: work, port: error, to: failed}
`
}

func submitDaemon(t *testing.T, flow string, turns ...turn) *Daemon {
	t.Helper()
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: turns})
	d.Flow("f", flow)
	d.Start()
	return d
}

func submitPrompts(d *Daemon) []string {
	var out []string
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/prompt" {
			out = append(out, c["text"].(string))
		}
	}
	return out
}

// output: tool: the valid call is the node's .agent.output, a downstream command reads its fields, the agent's text
// stays in final_message, and a readonly agent produces no note of its own (no forced "unparsed" item).
func TestSubmitOutputToolReachesDownstream(t *testing.T) {
	d := submitDaemon(t, agentOutputFlow(),
		turn{Steps: []step{submitCall("s1", validReview), say("Reviewed, see the output.")}})
	run := d.RunOf("src", map[string]any{"id": "7"})

	work := d.Node(run, "work")
	ar := d.AgentRuns(work.ID)[0]
	if !sameReview(t, ar.Output) || ar.FinalMessage != "Reviewed, see the output." || ar.ParseError {
		t.Fatalf("agent run output %s final %q parse_error %v", ar.Output, ar.FinalMessage, ar.ParseError)
	}
	items := d.Inbox()
	if len(items) != 1 || items[0].Title != "urgent block: 2 links" || items[0].ParseError {
		t.Fatalf("inbox: %+v (a custom output schema makes no note; the formatter's item is the only one)", items)
	}
	if tr := d.Transcript(ar.ID); !strings.Contains(tr, "mcp[s1] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
	// The payload keeps the established fields next to output.
	var payload struct {
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(d.NodeDetail(d.Node(run, "fmt").ID).Stdin.Text), &payload); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"run_id", "status", "final_message", "note", "output", "proposal", "comment"} {
		if _, ok := payload.Agent[k]; !ok {
			t.Errorf("agent payload lacks %q: %v", k, payload.Agent)
		}
	}
}

// A call that does not match the schema is a tool error naming the path and reason; the agent calls again and the
// corrected call is the output. One prompt only.
func TestSubmitOutputInvalidCallThenCorrected(t *testing.T) {
	d := submitDaemon(t, agentOutputFlow(),
		turn{Steps: []step{
			submitCall("bad", `{"priority":"meh","verdict":"block"}`),
			submitCall("good", validReview),
			say("done"),
		}})
	run := d.RunOf("src", map[string]any{"id": "7"})
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	tr := d.Transcript(ar.ID)
	for _, want := range []string{"mcp[bad] is_error=true", "/priority:", "missing property 'body'", "mcp[good] is_error=false"} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}
	if !sameReview(t, ar.Output) || len(submitPrompts(d)) != 1 {
		t.Errorf("output %s prompts %d", ar.Output, len(submitPrompts(d)))
	}
}

// A turn that ends without a call gets one follow-up in the same session; calling then succeeds.
func TestSubmitOutputFollowUpThenSuccess(t *testing.T) {
	d := submitDaemon(t, agentOutputFlow(),
		turn{When: atTurn(0), Steps: []step{say("It looks fine to me.")}},
		turn{When: atTurn(1), Steps: []step{submitCall("s1", validReview), say("submitted")}})
	run := d.RunOf("src", map[string]any{"id": "7"})
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	prompts := submitPrompts(d)
	if len(prompts) != 2 || !strings.Contains(prompts[1], "You must call submit_output") || !strings.Contains(prompts[1], `"verdict"`) {
		t.Fatalf("prompts: %q", prompts)
	}
	if !sameReview(t, ar.Output) || ar.Status != "succeeded" {
		t.Errorf("run %+v", ar)
	}
	sessions := 0
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/new" {
			sessions++
		}
	}
	if sessions != 1 {
		t.Errorf("the follow-up must reuse the session, got %d session/new", sessions)
	}
}

// Two turns without a valid call fail the node with the no_output class, routed to its error port.
func TestSubmitOutputMissingFailsNoOutputOnErrorPort(t *testing.T) {
	d := submitDaemon(t, agentOutputFlow(),
		turn{When: atTurn(0), Steps: []step{say("I will not call it.")}},
		turn{When: atTurn(1), Steps: []step{submitCall("bad", `{"priority":"high"}`), say("still not.")}})
	id := d.InjectRun("src", map[string]any{"id": "7"})
	run := d.WaitRun(id, "succeeded", "failed")
	work := d.Node(run, "work")
	if work.Error != "no_output" || work.Decision != "error" {
		t.Fatalf("work node: status %q decision %q error %q reason %q", work.Status, work.Decision, work.Error, work.Reason)
	}
	if len(submitPrompts(d)) != 2 {
		t.Errorf("prompts: %q", submitPrompts(d))
	}
	items := d.Inbox()
	if len(items) != 1 || !strings.HasPrefix(items[0].Title, "failed:") {
		t.Fatalf("the error port was not routed: %+v", items)
	}
	if !strings.Contains(work.Reason, "submit_output") || !strings.Contains(work.Reason, "missing properties") {
		t.Errorf("reason should say what was missing and the last refusal: %q", work.Reason)
	}
}

// output: tool without output_schema uses the note schema: the validated call is the inbox item, no text parsing.
func TestSubmitOutputDefaultNoteSchemaCreatesInboxItem(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: []turn{{Steps: []step{
		submitCall("s1", `{"title":"Deploy is failing","priority":"high","summary_md":"Roll back **now**.","suggested_reply":"On it","links":[{"label":"PR","url":"https://example.com/pr/1"}]}`),
		say("not json, and not parsed"),
	}}}})
	d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: Triage it.
    output: tool
`)
	d.Start()
	run := d.RunOf("src", map[string]any{"id": "1"})
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	items := d.Inbox()
	if len(items) != 1 {
		t.Fatalf("inbox: %+v", items)
	}
	it := items[0]
	if it.Title != "Deploy is failing" || it.Priority != "high" || it.SummaryMD != "Roll back **now**." || it.SuggestedReply != "On it" || len(it.Links) != 1 ||
		it.ParseError || it.Source != "agent_run" || it.AgentRunID != ar.ID {
		t.Errorf("inbox item: %+v", it)
	}
	if !strings.Contains(string(ar.Output), "Deploy is failing") {
		t.Errorf("output %s", ar.Output)
	}
}

// output: note keeps working: a valid submit_output call is used without parsing the text; without the call the final
// message is parsed as before and no follow-up is sent; unparseable text still gives the flagged item.
func TestSubmitOutputNoteModeCompatibility(t *testing.T) {
	note := `{"title":"From the tool","priority":"low","summary_md":"s"}`
	for _, tc := range []struct {
		name      string
		steps     []step
		title     string
		parseErr  bool
		wantCalls int
	}{
		{"valid call is used", []step{submitCall("s1", note), say("no json here")}, "From the tool", false, 1},
		{"invalid call falls back to the text", []step{submitCall("s1", `{"title":"x"}`), say(`final {"title":"From text","priority":"high","summary_md":"t"}`)}, "From text", false, 1},
		{"no call parses the text", []step{say(`final {"title":"From text","priority":"high","summary_md":"t"}`)}, "From text", false, 1},
		{"no call and unparseable text", []step{say("sorry, no JSON")}, "Unstructured agent result: f/work", true, 1},
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
			if got := len(submitPrompts(d)); got != tc.wantCalls {
				t.Errorf("prompts = %d, want %d (output: note never sends a follow-up)", got, tc.wantCalls)
			}
		})
	}
}

// Regression: a readonly agent with output none or tool made a note anyway (an "Unstructured agent result" item when
// the text was not JSON). Only output: note produces one.
func TestReadonlyForcesNoNoteUnlessOutputNote(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		steps []step
	}{
		{"inline none", "    output: none\n", []step{say("free text")}},
		{"inline tool with schema", reviewSchema, []step{submitCall("s1", validReview), say("free text")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDaemon(t)
			d.IdleSensor("src")
			d.Runtime("fake", script{Turns: []turn{{Steps: tc.steps}}})
			d.Flow("f", "id: f\nenabled: true\nsubscribe: [src]\nnodes:\n  - id: work\n    type: agent\n    runtime: fake\n    mode: readonly\n    instructions: Look.\n"+tc.extra)
			d.Start()
			d.RunOf("src", map[string]any{"id": "1"})
			if items := d.Inbox(); len(items) != 0 {
				t.Fatalf("a readonly agent without output: note made inbox items: %+v", items)
			}
		})
	}
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

// submit_output belongs to the run of one node with output tool or note: another node's agent, and a node with
// output none, get "unknown tool".
func TestSubmitOutputIsScopedToItsNode(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("tooled", script{Turns: []turn{{Steps: []step{submitCall("s1", validReview), say("ok")}}}})
	d.Runtime("plain", script{Turns: []turn{{Steps: []step{submitCall("s2", validReview), say("ok")}}}})
	d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: first
    type: agent
    runtime: tooled
    mode: readonly
    instructions: One.
`+reviewSchema+`
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
	if !sameReview(t, first.Output) {
		t.Errorf("first output %s", first.Output)
	}
	if tr := d.Transcript(second.ID); !strings.Contains(tr, "mcp[s2] is_error=true") || !strings.Contains(tr, "unknown tool") {
		t.Errorf("output none exposes submit_output:\n%s", tr)
	}
	if string(second.Output) != "null" {
		t.Errorf("second output %s", second.Output)
	}
	// The preamble of a run with output none says nothing about the tool.
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
    output: tool
`)
	d.Start()
	d.RunOf("src", map[string]any{"id": "1"})
	items := d.Inbox()
	if len(items) != 1 || items[0].Title != "Would alert" || !items[0].DryRun || items[0].Priority != "normal" {
		t.Fatalf("inbox: %+v", items)
	}
}
