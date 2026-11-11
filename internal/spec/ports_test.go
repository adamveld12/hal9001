package spec

import (
	"encoding/json"
	"strings"
	"testing"
)

const reviewSchemaYAML = `schema: {type: object, required: [verdict], properties: {verdict: {type: string}}}`

func diagsOf(t *testing.T, y string) []Diagnostic {
	t.Helper()
	_, ds := CheckFlow("t", []byte(y), testRefs())
	return ds
}

func findDiag(ds []Diagnostic, code string, sev Severity) *Diagnostic {
	for i := range ds {
		if ds[i].Code == code && ds[i].Severity == sev {
			return &ds[i]
		}
	}
	return nil
}

func TestAgentPortsValidationErrors(t *testing.T) {
	inbox := "  - {id: i, type: inbox, title: x}\n"
	tests := []struct {
		name, yaml, code, path, msg string
	}{
		{
			name: "reserved port",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {error: {"+reviewSchemaYAML+"}}}\n"+inbox, "  []\n"),
			code: CodeReservedPort, path: "nodes[0].ports.error", msg: "reserved",
		},
		{
			name: "out is reserved too",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {out: {"+reviewSchemaYAML+"}}}\n"+inbox, "  []\n"),
			code: CodeReservedPort, path: "nodes[0].ports.out", msg: "reserved",
		},
		{
			name: "port name pattern",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {Needs-Human: {"+reviewSchemaYAML+"}}}\n"+inbox, "  []\n"),
			code: CodeInvalidID, path: "nodes[0].ports.Needs-Human", msg: "emit_Needs-Human",
		},
		{
			name: "ports with output note",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, output: note, ports: {r: {"+reviewSchemaYAML+"}}}\n"+inbox, "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].ports", msg: "output: note",
		},
		{
			name: "ports with output none",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, output: none, ports: {r: {"+reviewSchemaYAML+"}}}\n"+inbox, "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].ports", msg: "output: none",
		},
		{
			name: "schema not valid JSON Schema",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {schema: {type: object, properties: {p: {type: strng}}}}}}\n"+inbox, "  []\n"),
			code: CodeInvalidValue, path: "nodes[0].ports.r.schema", msg: "/properties/p/type",
		},
		{
			name: "schema root must be an object",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {schema: {type: string}}}}\n"+inbox, "  []\n"),
			code: CodeInvalidValue, path: "nodes[0].ports.r.schema", msg: "type: object",
		},
		{
			name: "schema cannot reference a remote schema",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {schema: {type: object, properties: {p: {$ref: 'https://example.com/s.json'}}}}}}\n"+inbox, "  []\n"),
			code: CodeInvalidValue, path: "nodes[0].ports.r.schema", msg: "remote",
		},
		{
			name: "port without schema must go to inbox nodes only",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {description: d}}}\n"+cmd("c"), "  - {from: a, port: r, to: c}\n"),
			code: CodePortNeedsSchema, path: "nodes[0].ports.r.schema", msg: "directly to an inbox node",
		},
		{
			name: "one non-inbox edge among inbox edges",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {}}}\n"+inbox+cmd("c"), "  - {from: a, port: r, to: i}\n  - {from: a, port: r, to: c}\n"),
			code: CodePortNeedsSchema, path: "nodes[0].ports.r.schema", msg: "no schema",
		},
		{
			name: "emit without ports",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, emit: many}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].emit", msg: "only valid with ports",
		},
		{
			name: "emit value",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, emit: several, ports: {r: {"+reviewSchemaYAML+"}}}\n"+inbox, "  - {from: a, port: r, to: i}\n"),
			code: CodeInvalidEnum, path: "nodes[0].emit", msg: "one | many",
		},
		{
			name: "edge from out of an agent with ports",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {"+reviewSchemaYAML+"}}}\n"+inbox, "  - {from: a, to: i}\n"),
			code: CodeUndeclaredPort, path: "edges[0].port", msg: `no port "out"`,
		},
		{
			name: "inbox without title is still an error outside the shortcut",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.'}\n  - {id: i, type: inbox}\n", "  - {from: a, to: i}\n"),
			code: CodeRequired, path: "nodes[1].title", msg: "title is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ds := diagsOf(t, tt.yaml)
			d := findDiag(ds, tt.code, SeverityError)
			if d == nil {
				t.Fatalf("no %s error in %v", tt.code, ds)
			}
			if d.Path != tt.path || !strings.Contains(d.Message, tt.msg) {
				t.Errorf("path=%q msg=%q, want path %q msg containing %q", d.Path, d.Message, tt.path, tt.msg)
			}
			if d.NodeID == "" {
				t.Errorf("diagnostic %v has no node", d)
			}
		})
	}
}

func TestAgentPortsOnPromptFileWithNoteOutput(t *testing.T) {
	refs := testRefs()
	refs.PromptOutputs = map[string]string{"pr-review": OutputNote}
	y := flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, prompt: pr-review, ports: {r: {"+reviewSchemaYAML+"}}}\n  - {id: i, type: inbox, title: x}\n", "  - {from: a, port: r, to: i}\n")
	_, ds := CheckFlow("t", []byte(y), refs)
	if d := findDiag(ds, CodeFieldNotAllowed, SeverityError); d == nil || d.Path != "nodes[0].ports" {
		t.Fatalf("want an error for ports on a prompt with output note, got %v", ds)
	}
	refs.PromptOutputs["pr-review"] = OutputNone
	if _, ds = CheckFlow("t", []byte(y), refs); HasErrors(ds) {
		t.Fatalf("ports on a prompt file with output none: %v", ds)
	}
}

func TestAgentPortsWarnings(t *testing.T) {
	// A declared port with no outgoing edge: warning, not error.
	y := flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {"+reviewSchemaYAML+"}, spare: {"+reviewSchemaYAML+"}}}\n  - {id: i, type: inbox, title: x}\n", "  - {from: a, port: r, to: i}\n")
	ds := diagsOf(t, y)
	if HasErrors(ds) {
		t.Fatalf("errors: %v", ds)
	}
	if d := findDiag(ds, CodeUnusedPort, SeverityWarning); d == nil || d.Path != "nodes[0].ports.spare" || d.NodeID != "a" {
		t.Fatalf("want an unused_port warning on nodes[0].ports.spare, got %v", ds)
	}

	// A template that reads a field the port's schema does not declare: warning on the downstream template.
	y = flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {r: {"+reviewSchemaYAML+"}}}\n  - {id: i, type: inbox, title: 'Verdict {{.input.agent.output.verdict}} {{.input.agent.output.score}}'}\n", "  - {from: a, port: r, to: i}\n")
	ds = diagsOf(t, y)
	if HasErrors(ds) {
		t.Fatalf("errors: %v", ds)
	}
	d := findDiag(ds, CodeTemplateField, SeverityWarning)
	if d == nil || d.Path != "nodes[1].title" || d.NodeID != "i" || !strings.Contains(d.Message, "output.score") {
		t.Fatalf("want one template_field warning for score, got %v", ds)
	}
	for _, d := range ds {
		if d.Code == CodeTemplateField && strings.Contains(d.Message, "output.verdict") {
			t.Errorf("verdict is declared, but warned: %v", d)
		}
	}
}

func TestInboxShortcutIsValidWithoutTitle(t *testing.T) {
	y := flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, instructions: hi, ports: {reviewed: {description: Done.}, urgent: {description: Look.}}}\n  - {id: i1, type: inbox}\n  - {id: i2, type: inbox, priority: '{{.input.agent.output.priority}}'}\n", "  - {from: a, port: reviewed, to: i1}\n  - {from: a, port: urgent, to: i2}\n")
	if ds := diagsOf(t, y); HasErrors(ds) || len(ds) != 0 {
		t.Fatalf("inbox shortcut: %v", ds)
	}
}

func TestInboxPriorityTemplateOrName(t *testing.T) {
	ok := flowWith("  - {id: i, type: inbox, title: x, priority: '{{ if .input.x }}urgent{{ else }}low{{ end }}'}\n", "  []\n")
	if ds := diagsOf(t, ok); HasErrors(ds) {
		t.Fatalf("templated priority: %v", ds)
	}
	bad := flowWith("  - {id: i, type: inbox, title: x, priority: loud}\n", "  []\n")
	if d := findDiag(diagsOf(t, bad), CodeInvalidEnum, SeverityError); d == nil || d.Path != "nodes[0].priority" {
		t.Fatalf("a plain wrong priority must stay an error")
	}
	broken := flowWith("  - {id: i, type: inbox, title: x, priority: '{{ if }}'}\n", "  []\n")
	if d := findDiag(diagsOf(t, broken), CodeInvalidTemplate, SeverityError); d == nil || d.Path != "nodes[0].priority" {
		t.Fatalf("a broken priority template must be an error")
	}
}

func TestAgentPortsParseAndRoundTrip(t *testing.T) {
	y := `enabled: true
subscribe: [github-notifications]
nodes:
  - id: review
    type: agent
    runtime: claude
    mode: readonly
    instructions: Review it.
    emit: many
    ports:
      reviewed:
        description: Reviewed, nothing to do.
      needs_human:
        description: Needs a person.
        schema:
          type: object
          required: [priority, body]
          properties:
            priority: {enum: [urgent, high, normal, low]}
            body: {type: string}
  - {id: shown, type: inbox}
  - {id: asked, type: inbox, title: 'Check {{.input.agent.output.body}}'}
edges:
  - {from: review, port: reviewed, to: shown}
  - {from: review, port: needs_human, to: asked}
`
	f, ds := CheckFlow("t", []byte(y), testRefs())
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	n := f.Nodes[0]
	if got := strings.Join(n.Ports.Names(), ","); got != "reviewed,needs_human" || !n.Ports.Map || n.Emit != EmitMany {
		t.Fatalf("ports = %q map=%v emit=%q: declaration order or form lost", got, n.Ports.Map, n.Emit)
	}
	if got := strings.Join(n.OutPorts(), ","); got != "reviewed,needs_human,error" {
		t.Errorf("OutPorts = %q: declared ports replace out", got)
	}
	out, err := MarshalFlow(*f)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if ti, ri, pi := strings.Index(text, "type: object"), strings.Index(text, "required:"), strings.Index(text, "properties:"); ti < 0 || ti >= ri || ri >= pi {
		t.Errorf("schema keywords out of order:\n%s", text)
	}
	if strings.Index(text, "reviewed:") > strings.Index(text, "needs_human:") {
		t.Errorf("port order changed:\n%s", text)
	}
	g, ds := ParseFlow("t", out)
	if len(ds) != 0 {
		t.Fatalf("saved file does not parse: %v\n%s", ds, text)
	}
	if h1, h2 := mustJSON(t, f), mustJSON(t, g); h1 != h2 {
		t.Errorf("round trip changed the flow:\n%s\n%s", h1, h2)
	}
	// The JSON form (the API) keeps the object shape and the order.
	var back Flow
	if err := jsonUnmarshal([]byte(mustJSON(t, f)), &back); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(back.Nodes[0].Ports.Names(), ","); got != "reviewed,needs_human" || !back.Nodes[0].Ports.Map {
		t.Errorf("JSON round trip: %q map=%v", got, back.Nodes[0].Ports.Map)
	}
}

func TestEmitToolsAndOutputsSection(t *testing.T) {
	y := `enabled: true
subscribe: [github-notifications]
nodes:
  - id: review
    type: agent
    runtime: claude
    mode: readonly
    instructions: Review it.
    ports:
      reviewed: {description: 'Reviewed, no action.'}
      needs_human: {description: Needs a human.}
      spare: {}
      to_cmd: {schema: {type: object}}
  - {id: shown, type: inbox, title: 'Review: {{.input.id}}'}
  - {id: asked, type: inbox, title: 'Needs your review: {{.input.id}}'}
  - {id: run, type: command, lang: jq, script: '.'}
edges:
  - {from: review, port: reviewed, to: shown}
  - {from: review, port: needs_human, to: asked}
  - {from: review, port: to_cmd, to: run}
`
	f, ds := CheckFlow("t", []byte(y), testRefs())
	if HasErrors(ds) {
		t.Fatalf("diagnostics: %v", ds)
	}
	tools := f.EmitTools(&f.Nodes[0])
	if len(tools) != 3 {
		t.Fatalf("a port without an edge gets no tool; got %d tools: %+v", len(tools), tools)
	}
	want := `## Outputs
Finish by calling exactly one of these tools:
- emit_reviewed: Reviewed, no action.
  Leads to: inbox note "Review: ..."
- emit_needs_human: Needs a human.
  Leads to: inbox note "Needs your review: ..."
- emit_to_cmd: Finish through the port to_cmd.
  Leads to: command node
Do not put the output in your final message; it is ignored.`
	if got := OutputsSection(tools, false); got != want {
		t.Errorf("section:\n%s\nwant:\n%s", got, want)
	}
	if got := OutputsSection(tools[:1], true); !strings.Contains(got, "Finish by calling one or more of these tools (each call is a separate output):") {
		t.Errorf("emit many header: %s", got)
	}
	if !tools[0].Shortcut || tools[0].Schema["type"] != "object" || tools[2].Shortcut {
		t.Errorf("schema-less inbox port uses the note schema, a declared schema stays: %+v", tools)
	}
	if got := tools[0].ToolDescription(); got != `Reviewed, no action. Leads to: inbox note "Review: ...".` {
		t.Errorf("tool description = %q", got)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
