package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

const reviewedPortSchema = `{type: object, required: [priority, body], properties: {priority: {enum: [urgent, normal]}, body: {type: string}}}`

// twoPortAgent is the ports block of node `work` (YAML under the node): reviewed and needs_human, both with a schema.
func twoPortAgent(extra string) string {
	return `    ports:
      reviewed:
        description: Reviewed, nothing for a person to do.
        schema: ` + reviewedPortSchema + `
      needs_human:
        description: Needs a person to look.
        schema: ` + reviewedPortSchema + `
` + extra
}

// portsFlow is a readonly agent node `work` with the given node lines, inbox nodes behind both ports and its error port.
func portsFlow(agentLines string) string {
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
` + agentLines + `  - {id: shown, type: inbox, title: "Reviewed {{.input.agent.output.body}}", priority: "{{.input.agent.output.priority}}"}
  - {id: asked, type: inbox, title: "Needs {{.input.agent.output.body}}"}
  - {id: failed, type: inbox, title: "failed: {{.input.error}}", summary_md: "{{.input.final_message}}"}
edges:
  - {from: work, port: reviewed, to: shown}
  - {from: work, port: needs_human, to: asked}
  - {from: work, port: error, to: failed}
`
}

func emitDaemon(t *testing.T, flow string, turns ...turn) *Daemon {
	t.Helper()
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: turns})
	d.Flow("f", flow)
	d.Start()
	return d
}

func titles(items []InboxItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

func sameTitles(t *testing.T, got []InboxItem, want ...string) {
	t.Helper()
	g := titles(got)
	if len(g) != len(want) {
		t.Fatalf("inbox titles %q, want %q", g, want)
	}
	seen := map[string]int{}
	for _, s := range g {
		seen[s]++
	}
	for _, s := range want {
		if seen[s]--; seen[s] < 0 {
			t.Fatalf("inbox titles %q, want %q", g, want)
		}
	}
}

func ranNode(det FlowRunDetail, id string) bool {
	for _, n := range det.NodeRuns {
		if n.NodeID == id {
			return true
		}
	}
	return false
}

// One call on a declared port: the emission is the node's output on that port, downstream templates read the fields, the
// payload has the established fields plus port and output, the rendered prompt ends with the generated Outputs section,
// and a readonly agent makes no inbox note of its own.
func TestPortsOneCallRoutesWithPayload(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{Steps: []step{toolCall("emit_reviewed", "e1", `{"priority":"urgent","body":"Looks fine"}`), say("done, see the tool call")}})
	run := d.RunOf("src", map[string]any{"id": "7"})

	items := d.Inbox()
	sameTitles(t, items, "Reviewed Looks fine")
	if items[0].Priority != "urgent" {
		t.Errorf("the templated priority was not applied: %+v", items[0])
	}
	if ranNode(run, "asked") {
		t.Error("the other port was routed")
	}
	work := d.Node(run, "work")
	if work.Decision != "emitted" || len(work.Ports) != 1 || work.Ports[0] != "reviewed" {
		t.Fatalf("work node: %+v", work)
	}
	ar := d.AgentRuns(work.ID)[0]
	if len(ar.Emissions) != 1 || ar.Emissions[0].Port != "reviewed" || !strings.Contains(string(ar.Emissions[0].Output), `"body":"Looks fine"`) || ar.FinalMessage != "done, see the tool call" || ar.ParseError {
		t.Fatalf("agent run emissions %+v final %q parse_error %v", ar.Emissions, ar.FinalMessage, ar.ParseError)
	}
	if string(ar.Output) != "null" {
		t.Errorf("output (submit_output) = %s, want null: only emit tools were available", ar.Output)
	}
	var payload struct {
		Input map[string]any `json:"input"`
		Agent map[string]any `json:"agent"`
	}
	if err := json.Unmarshal([]byte(d.NodeDetail(d.Node(run, "shown").ID).Stdin.Text), &payload); err != nil {
		t.Fatal(err)
	}
	out, _ := payload.Agent["output"].(map[string]any)
	if payload.Input["id"] != "7" || payload.Agent["port"] != "reviewed" || payload.Agent["run_id"] != ar.ID || payload.Agent["status"] != "succeeded" ||
		payload.Agent["final_message"] != "done, see the tool call" || out["body"] != "Looks fine" {
		t.Errorf("payload: %+v", payload)
	}
	for _, k := range []string{"note", "proposal", "comment"} {
		if _, ok := payload.Agent[k]; !ok {
			t.Errorf("agent payload lacks %q: %v", k, payload.Agent)
		}
	}
	// The Outputs section is appended after the instructions and stored with the rendered prompt.
	i, j := strings.Index(ar.RenderedPrompt, "Review 7"), strings.Index(ar.RenderedPrompt, "## Outputs\nFinish by calling exactly one of these tools:")
	if i < 0 || j < i {
		t.Fatalf("rendered prompt lacks the Outputs section after the instructions:\n%s", ar.RenderedPrompt)
	}
	for _, want := range []string{
		"- emit_reviewed: Reviewed, nothing for a person to do.\n  Leads to: inbox note \"Reviewed ...\"\n",
		"- emit_needs_human: Needs a person to look.\n  Leads to: inbox note \"Needs ...\"\n",
		"Do not put the output in your final message; it is ignored.",
	} {
		if !strings.Contains(ar.RenderedPrompt, want) {
			t.Errorf("rendered prompt lacks %q:\n%s", want, ar.RenderedPrompt)
		}
	}
	if tr := d.Transcript(ar.ID); !strings.Contains(tr, "mcp[e1] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
}

// Two ports: the port of the call decides which branch runs.
func TestPortsPickByCall(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{Steps: []step{toolCall("emit_needs_human", "e1", `{"priority":"normal","body":"deploy"}`), say("x")}})
	run := d.RunOf("src", map[string]any{"id": "7"})
	sameTitles(t, d.Inbox(), "Needs deploy")
	if ranNode(run, "shown") {
		t.Error("the reviewed branch ran")
	}
}

// emit: one is last-valid-wins until the turn ends, whatever port the earlier valid call used; an invalid later call
// does not discard it.
func TestPortsEmitOneLastValidCallWins(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{Steps: []step{
			toolCall("emit_reviewed", "e1", `{"priority":"urgent","body":"first"}`),
			toolCall("emit_needs_human", "e2", `{"priority":"normal","body":"second"}`),
			toolCall("emit_reviewed", "e3", `{"priority":"bogus"}`),
			say("x"),
		}})
	run := d.RunOf("src", map[string]any{"id": "7"})
	sameTitles(t, d.Inbox(), "Needs second")
	if ranNode(run, "shown") {
		t.Error("the first call's branch ran: the last valid call must win")
	}
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	if len(ar.Emissions) != 1 || ar.Emissions[0].Port != "needs_human" {
		t.Errorf("emissions: %+v", ar.Emissions)
	}
}

// emit: many: every valid call is its own emission and downstream runs once per emission.
func TestPortsEmitManyFansOut(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("    emit: many\n")),
		turn{Steps: []step{
			toolCall("emit_reviewed", "e1", `{"priority":"urgent","body":"a"}`),
			toolCall("emit_reviewed", "e2", `{"priority":"bogus"}`),
			toolCall("emit_reviewed", "e3", `{"priority":"normal","body":"b"}`),
			toolCall("emit_needs_human", "e4", `{"priority":"normal","body":"c"}`),
			say("x"),
		}})
	run := d.RunOf("src", map[string]any{"id": "7"})
	sameTitles(t, d.Inbox(), "Reviewed a", "Reviewed b", "Needs c")
	shown := 0
	for _, n := range run.NodeRuns {
		if n.NodeID == "shown" {
			shown++
		}
	}
	if shown != 2 {
		t.Errorf("the reviewed branch ran %d times, want once per emission (2)", shown)
	}
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	if len(ar.Emissions) != 3 {
		t.Errorf("emissions: %+v", ar.Emissions)
	}
	if p := promptsOf(d); len(p) != 1 || !strings.Contains(p[0], "Finish by calling one or more of these tools (each call is a separate output):") {
		t.Errorf("prompts: %q", p)
	}
}

// A call that does not match the schema is a tool error naming the path and reason; the agent calls again and the
// corrected call is the emission. One prompt only.
func TestPortsInvalidCallThenCorrected(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{Steps: []step{
			toolCall("emit_reviewed", "bad", `{"priority":"meh"}`),
			toolCall("emit_reviewed", "good", `{"priority":"urgent","body":"fixed"}`),
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
	sameTitles(t, d.Inbox(), "Reviewed fixed")
	if len(promptsOf(d)) != 1 {
		t.Errorf("prompts: %q", promptsOf(d))
	}
}

// A turn that ends without a call gets one follow-up in the same session; calling then succeeds.
func TestPortsFollowUpThenSuccess(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{When: atTurn(0), Steps: []step{say("It looks fine to me.")}},
		turn{When: atTurn(1), Steps: []step{toolCall("emit_reviewed", "e1", `{"priority":"normal","body":"late"}`), say("submitted")}})
	d.RunOf("src", map[string]any{"id": "7"})
	prompts := promptsOf(d)
	if len(prompts) != 2 || prompts[1] != "You must finish by calling one of: emit_reviewed, emit_needs_human." {
		t.Fatalf("prompts: %q", prompts)
	}
	sameTitles(t, d.Inbox(), "Reviewed late")
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

// Two turns without a valid call fail the node with the no_output class, routed to its error port with the final
// message; there is exactly one follow-up.
func TestPortsMissingTwiceFailsNoOutputOnErrorPort(t *testing.T) {
	d := emitDaemon(t, portsFlow(twoPortAgent("")),
		turn{When: atTurn(0), Steps: []step{say("I will not call it.")}},
		turn{When: atTurn(1), Steps: []step{toolCall("emit_reviewed", "bad", `{"priority":"high"}`), say("still not.")}})
	id := d.InjectRun("src", map[string]any{"id": "7"})
	run := d.WaitRun(id, "succeeded", "failed")
	work := d.Node(run, "work")
	if work.Error != "no_output" || work.Decision != "error" {
		t.Fatalf("work node: status %q decision %q error %q reason %q", work.Status, work.Decision, work.Error, work.Reason)
	}
	if len(promptsOf(d)) != 2 {
		t.Errorf("prompts: %q", promptsOf(d))
	}
	items := d.Inbox()
	if len(items) != 1 || items[0].Title != "failed: no_output" || items[0].SummaryMD != "still not." {
		t.Fatalf("the error port was not routed with the final message: %+v", items)
	}
	if !strings.Contains(work.Reason, "emit_<port>") || !strings.Contains(work.Reason, "missing property 'body'") {
		t.Errorf("reason should say what was missing and the last refusal: %q", work.Reason)
	}
}

// Approve mode with a proposal: phase 1 only plans (no emit tools, no Outputs section); the tools and the section arrive
// with the approval, and the branch runs after phase 2.
func TestPortsApproveProposalEmitsOnlyAfterApproval(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{LoadSession: true, Turns: []turn{
		{When: inMode("default"), Steps: []step{toolCall("emit_reviewed", "early", `{"priority":"urgent","body":"too early"}`), say(proposalPlan)}},
		{When: inMode("bypass"), Steps: []step{toolCall("emit_reviewed", "late", `{"priority":"normal","body":"after approval"}`), say("executed")}},
	}})
	flow := strings.Replace(portsFlow(twoPortAgent("")), "    mode: readonly\n", "    mode: approve\n    approve: proposal\n", 1)
	d.Flow("f", flow)
	d.Start()

	id := d.InjectRun("src", map[string]any{"id": "7"})
	parked := d.WaitRun(id, "awaiting_approval")
	ap := d.WaitApproval("proposal")
	p1 := d.AgentRuns(d.Node(parked, "work").ID)[0]
	if tr := d.Transcript(p1.ID); !strings.Contains(tr, "mcp[early] is_error=true") || !strings.Contains(tr, "unknown tool") {
		t.Errorf("the planning phase must not have emit tools:\n%s", tr)
	}
	if strings.Contains(p1.RenderedPrompt, "## Outputs") {
		t.Errorf("the planning prompt has an Outputs section:\n%s", p1.RenderedPrompt)
	}
	if items := d.Inbox(); len(items) != 0 {
		t.Fatalf("a branch ran before the approval: %+v", items)
	}

	d.Decide(ap.ID, true, map[string]any{"comment": "go"})
	done := d.WaitRun(id, "succeeded")
	runs := d.AgentRuns(d.Node(done, "work").ID)
	if len(runs) != 2 {
		t.Fatalf("agent runs: %d", len(runs))
	}
	p2 := runs[1]
	if !strings.HasPrefix(p2.RenderedPrompt, "Approved. Execute the plan.") || !strings.Contains(p2.RenderedPrompt, "## Outputs\nFinish by calling exactly one of these tools:") ||
		!strings.Contains(p2.RenderedPrompt, "- emit_reviewed:") {
		t.Errorf("execute prompt:\n%s", p2.RenderedPrompt)
	}
	if len(p2.Emissions) != 1 || p2.Emissions[0].Port != "reviewed" {
		t.Errorf("phase 2 emissions: %+v", p2.Emissions)
	}
	sameTitles(t, d.Inbox(), "Reviewed after approval")
}

// A port without a schema whose edges all go directly to inbox nodes uses the inbox note schema: the inbox node needs no
// title, takes the note from the emission, and its own templates win.
func TestPortsInboxShortcut(t *testing.T) {
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
      custom: {description: 'Tell me, my way.'}
  - {id: note, type: inbox}
  - {id: mine, type: inbox, title: "Mine: {{.input.agent.output.title}}", priority: low}
edges:
  - {from: work, port: plain, to: note}
  - {from: work, port: custom, to: mine}
`
	t.Run("plain", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{
			toolCall("emit_plain", "bad2", `{"summary_md":"no title"}`),
			toolCall("emit_plain", "ok", `{"title":"Deploy failing","summary_md":"Roll **back**.","priority":"high","suggested_reply":"On it","links":[{"label":"PR","url":"https://example.com/1"}]}`),
			say("x"),
		}})
		run := d.RunOf("src", map[string]any{"id": "7"})
		items := d.Inbox()
		if len(items) != 1 {
			t.Fatalf("inbox: %+v", items)
		}
		it := items[0]
		if it.Title != "Deploy failing" || it.SummaryMD != "Roll **back**." || it.Priority != "high" || it.SuggestedReply != "On it" || len(it.Links) != 1 {
			t.Errorf("item: %+v", it)
		}
		ar := d.AgentRuns(d.Node(run, "work").ID)[0]
		if tr := d.Transcript(ar.ID); !strings.Contains(tr, "mcp[bad2] is_error=true") || !strings.Contains(tr, "missing property 'title'") {
			t.Errorf("the note schema must refuse a note without a title:\n%s", tr)
		}
		if !strings.Contains(ar.RenderedPrompt, "Leads to: inbox note\n") {
			t.Errorf("a titleless inbox node leads to plain `inbox note`:\n%s", ar.RenderedPrompt)
		}
	})
	t.Run("node templates override", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{
			toolCall("emit_custom", "ok", `{"title":"Disk full","summary_md":"s","priority":"urgent"}`), say("x"),
		}})
		d.RunOf("src", map[string]any{"id": "7"})
		items := d.Inbox()
		if len(items) != 1 || items[0].Title != "Mine: Disk full" || items[0].Priority != "low" || items[0].SummaryMD != "s" {
			t.Fatalf("inbox: %+v", items)
		}
	})
}

// A templated inbox priority must render to a priority; anything else fails the inbox node with class template.
func TestInboxTemplatedPriority(t *testing.T) {
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
      r:
        schema: {type: object, required: [level], properties: {level: {type: string}}}
  - {id: shown, type: inbox, title: "Level {{.input.agent.output.level}}", priority: "{{.input.agent.output.level}}"}
edges:
  - {from: work, port: r, to: shown}
`
	t.Run("a priority name renders, in any case", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{toolCall("emit_r", "e1", `{"level":"High"}`), say("x")}})
		run := d.RunOf("src", map[string]any{"id": "1"})
		if items := d.Inbox(); len(items) != 1 || items[0].Priority != "high" || d.Node(run, "shown").Status != "done" {
			t.Fatalf("inbox: %+v", items)
		}
	})
	t.Run("anything else fails the node with class template", func(t *testing.T) {
		d := emitDaemon(t, flow, turn{Steps: []step{toolCall("emit_r", "e2", `{"level":"loud"}`), say("x")}})
		id := d.InjectRun("src", map[string]any{"id": "2"})
		bad := d.WaitRun(id, "succeeded", "failed")
		shown := d.Node(bad, "shown")
		if shown.Status != "failed" || shown.Error != "template" || !strings.Contains(shown.Reason, `"loud"`) {
			t.Errorf("shown: status %q error %q reason %q", shown.Status, shown.Error, shown.Reason)
		}
		if len(d.Inbox()) != 0 {
			t.Errorf("an invalid priority created an item: %+v", d.Inbox())
		}
	})
}

// A prompt-file node works the same: the Outputs section is appended after the rendered prompt and extra_prompt.
func TestPortsOnPromptFileNode(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}} from the file")
	d.Runtime("fake", script{Turns: []turn{{Steps: []step{toolCall("emit_reviewed", "e1", `{"priority":"urgent","body":"from file"}`), say("x")}}}})
	flow := strings.Replace(portsFlow(twoPortAgent("    extra_prompt: Be brief.\n")), "    instructions: |\n      Review {{.input.id}}\n", "    prompt: p\n", 1)
	d.Flow("f", flow)
	d.Start()
	run := d.RunOf("src", map[string]any{"id": "7"})
	ar := d.AgentRuns(d.Node(run, "work").ID)[0]
	a, b, c := strings.Index(ar.RenderedPrompt, "Review 7 from the file"), strings.Index(ar.RenderedPrompt, "Be brief."), strings.Index(ar.RenderedPrompt, "## Outputs")
	if a < 0 || b < a || c < b {
		t.Fatalf("order must be prompt, extra_prompt, Outputs:\n%s", ar.RenderedPrompt)
	}
	sameTitles(t, d.Inbox(), "Reviewed from file")
}

// emit tools belong to one run of one node: another node's agent gets "unknown tool", and a declared port without an
// outgoing edge has no tool at all.
func TestPortsToolsAreScopedToTheirNode(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("first", script{Turns: []turn{{Steps: []step{toolCall("emit_reviewed", "a1", `{"priority":"urgent","body":"one"}`), toolCall("emit_spare", "a2", `{"priority":"urgent","body":"x"}`), say("ok")}}}})
	d.Runtime("second", script{Turns: []turn{{Steps: []step{toolCall("emit_reviewed", "b1", `{"priority":"urgent","body":"two"}`), say("ok")}}}})
	d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: first
    type: agent
    runtime: first
    mode: readonly
    instructions: One.
    ports:
      reviewed: {schema: `+reviewedPortSchema+`}
      spare: {schema: `+reviewedPortSchema+`}
  - id: second
    type: agent
    runtime: second
    mode: readonly
    instructions: Two.
    output: none
  - {id: shown, type: inbox, title: "Reviewed {{.input.agent.output.body}}"}
edges:
  - {from: first, port: reviewed, to: shown}
  - {from: first, port: reviewed, to: second}
`)
	d.Start()
	run := d.RunOf("src", map[string]any{"id": "1"})
	first := d.AgentRuns(d.Node(run, "first").ID)[0]
	second := d.AgentRuns(d.Node(run, "second").ID)[0]
	if tr := d.Transcript(second.ID); !strings.Contains(tr, "mcp[b1] is_error=true") || !strings.Contains(tr, "unknown tool") {
		t.Errorf("the next node's agent can call emit_reviewed:\n%s", tr)
	}
	if tr := d.Transcript(first.ID); !strings.Contains(tr, "mcp[a1] is_error=false") || !strings.Contains(tr, "mcp[a2] is_error=true") {
		t.Errorf("first transcript:\n%s", tr)
	}
	if strings.Contains(first.RenderedPrompt, "emit_spare") || strings.Contains(second.RenderedPrompt, "emit_") {
		t.Errorf("prompts mention tools that do not exist:\nfirst:\n%s\nsecond:\n%s", first.RenderedPrompt, second.RenderedPrompt)
	}
	if len(second.Emissions) != 0 {
		t.Errorf("second emissions %+v", second.Emissions)
	}
}

// Routing through a port never skips an approval: an emission into an approval node still parks the run until someone decides.
func TestPortsEmissionStillMeetsApprovalNode(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Runtime("fake", script{Turns: []turn{{Steps: []step{toolCall("emit_r", "e1", `{"priority":"urgent","body":"ship it"}`), say("x")}}}})
	d.Flow("f", `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    instructions: One.
    ports:
      r: {schema: `+reviewedPortSchema+`}
  - {id: confirm, type: approval, title: "Ship {{.input.agent.output.body}}?", description: "Asks whether to ship the agent output"}
  - {id: sent, type: inbox, title: sent}
edges:
  - {from: work, port: r, to: confirm}
  - {from: confirm, port: approved, to: sent}
`)
	d.Start()
	id := d.InjectRun("src", map[string]any{"id": "1"})
	d.WaitRun(id, "awaiting_approval")
	if items := d.Inbox(); len(items) != 0 {
		t.Fatalf("the branch ran without the approval: %+v", items)
	}
	ap := d.WaitApproval("request")
	d.Decide(ap.ID, true, nil)
	d.WaitRun(id, "succeeded")
	sameTitles(t, d.Inbox(), "sent")
}
