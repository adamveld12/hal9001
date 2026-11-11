package spec

import (
	"strings"
	"testing"
	"time"
)

// requirementsFlow is the flow from REQUIREMENTS §6, verbatim.
const requirementsFlow = `# flows/triage-github.yaml
id: triage-github
enabled: true
dry_run: false
subscribe: [github-notifications]
max_concurrent: 2
nodes:
  - id: only-review-requests
    type: command
    lang: jq
    script: 'select(.reason == "review_requested") | . + {_reason: "review requested"}'
    ui: {x: 100, y: 80}

  - id: classify
    type: command
    lang: python
    ports: [small, large]
    script: |
      import json, sys, subprocess
      ev = json.load(sys.stdin)
      # ... call gh to get PR size ...
      ev["_port"] = "small" if additions < 200 else "large"
      print(json.dumps(ev))

  - id: review-small
    type: agent
    runtime: claude          # key in config.yaml ` + "`runtimes`" + ` (claude | codex | omp | ...)
    mode: approve            # auto | approve | readonly
    approve: proposal        # proposal | per_tool (approve mode only)
    prompt: pr-review        # prompts/pr-review.md
    extra_prompt: "Focus on tests."
    model: sonnet
    cwd: {mode: worktree, repo: "~/code/{{.input.repository.name}}", ref: "origin/main"}
    timeout: 20m

  - id: note-large
    type: inbox
    title: "Large PR needs you: {{.input.subject.title}}"
    priority: high
    summary_md: "{{.input.subject.url}}"

edges:
  - {from: only-review-requests, to: classify}
  - {from: classify, port: small, to: review-small}
  - {from: classify, port: large, to: note-large}
`

func testRefs() *Refs {
	return &Refs{
		Sensors:  map[string]bool{"github-notifications": true, "other": true},
		Prompts:  map[string]bool{"pr-review": true},
		Runtimes: map[string]Runtime{"claude": {Command: []string{"claude-agent-acp"}}},
	}
}

func TestRequirementsFlowLoadsClean(t *testing.T) {
	f, ds := CheckFlow("triage-github", []byte(requirementsFlow), testRefs())
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds)
	}
	if f.ID != "triage-github" || !f.Enabled || len(f.Nodes) != 4 || len(f.Edges) != 3 {
		t.Fatalf("unexpected flow: %+v", f)
	}
	if got := f.Nodes[0].UI; got.X != 100 || got.Y != 80 {
		t.Errorf("ui = %+v", got)
	}
	cwd := f.Nodes[2].Cwd
	if cwd == nil || cwd.Mode != CwdWorktree || cwd.Repo != "~/code/{{.input.repository.name}}" || cwd.Ref != "origin/main" {
		t.Errorf("agent cwd = %+v", cwd)
	}
	if got := f.EntryNodes(); len(got) != 1 || got[0] != "only-review-requests" {
		t.Errorf("entry nodes = %v", got)
	}
}

func TestFlowDefaults(t *testing.T) {
	f, _ := ParseFlow("", []byte(requirementsFlow))
	filter, classify, review, note := f.Nodes[0], f.Nodes[1], f.Nodes[2], f.Nodes[3]
	if filter.Timeout.Std() != 60*time.Second {
		t.Errorf("command timeout = %v, want 60s", filter.Timeout)
	}
	if got := strings.Join(filter.Ports.Names(), ","); got != "out" {
		t.Errorf("default ports = %q, want out", got)
	}
	if got := strings.Join(classify.OutPorts(), ","); got != "small,large,error" {
		t.Errorf("classify ports = %q: implicit error port missing or declared ports lost", got)
	}
	if review.Timeout.Std() != 20*time.Minute {
		t.Errorf("explicit agent timeout overridden: %v", review.Timeout)
	}
	if got := strings.Join(review.OutPorts(), ","); got != "out,denied,error" {
		t.Errorf("approve agent ports = %q, want out,denied,error", got)
	}
	if got := strings.Join(note.OutPorts(), ","); got != "out,error" {
		t.Errorf("inbox ports = %q", got)
	}
	if f.Edges[0].Port != PortOut {
		t.Errorf("edge port default = %q", f.Edges[0].Port)
	}

	g, _ := ParseFlow("", []byte(`
id: g
subscribe: [s]
nodes:
  - {id: a, type: agent, runtime: claude, mode: approve, prompt: p}
  - {id: b, type: agent, runtime: claude, mode: readonly, prompt: p}
  - {id: c, type: inbox, title: t}
`))
	a, b, c := g.Nodes[0], g.Nodes[1], g.Nodes[2]
	if a.Timeout.Std() != 30*time.Minute || a.Approve != ApproveProposal {
		t.Errorf("agent defaults: timeout %v approve %q", a.Timeout, a.Approve)
	}
	if b.Approve != "" || strings.Contains(strings.Join(b.OutPorts(), ","), PortDenied) {
		t.Errorf("readonly agent must not get approve style or denied port: %+v", b.OutPorts())
	}
	if a.ApprovalTimeout != 0 {
		t.Errorf("node approval_timeout must stay unset so the config default applies, got %v", a.ApprovalTimeout)
	}
	if c.Priority != PriorityNormal || g.MaxConcurrent != 2 || g.Enabled {
		t.Errorf("inbox priority %q, max_concurrent %d, enabled %v", c.Priority, g.MaxConcurrent, g.Enabled)
	}
}

func TestApprovalTimeoutFallsBackToConfig(t *testing.T) {
	cfg, _ := ParseConfig([]byte("approval_timeout: 2h\n"))
	if got := cfg.ApprovalTimeoutFor(&Node{}); got != 2*time.Hour {
		t.Errorf("config default = %v, want 2h", got)
	}
	if got := cfg.ApprovalTimeoutFor(&Node{ApprovalTimeout: Duration(5 * time.Minute)}); got != 5*time.Minute {
		t.Errorf("node override = %v, want 5m", got)
	}
	def, _ := ParseConfig(nil)
	if got := def.ApprovalTimeoutFor(nil); got != time.Hour {
		t.Errorf("built-in default = %v, want 1h", got)
	}
}

// flowWith wraps nodes and edges (already indented YAML) in a valid flow.
func flowWith(nodes, edges string) string {
	return "id: t\nenabled: true\nsubscribe: [github-notifications]\nnodes:\n" + nodes + "edges:\n" + edges
}

const cmdNode = "  - {id: %s, type: command, lang: jq, script: '.'}\n"

func cmd(id string) string { return strings.Replace(cmdNode, "%s", id, 1) }

func TestFlowValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		code    string
		path    string
		node    string
		msg     string
		wantErr int // total error diagnostics; 0 means exactly one
	}{
		{
			name: "cycle reports its path",
			yaml: flowWith(cmd("entry")+cmd("a")+cmd("b")+cmd("c"),
				"  - {from: entry, to: a}\n  - {from: a, to: b}\n  - {from: b, to: c}\n  - {from: c, to: a}\n"),
			code: CodeCycle, path: "edges[3]", node: "a", msg: "a -> b -> c -> a",
		},
		{
			name: "self loop is a cycle",
			yaml: flowWith(cmd("entry")+cmd("a"), "  - {from: entry, to: a}\n  - {from: a, to: a}\n"),
			code: CodeCycle, path: "edges[1]", node: "a", msg: "a -> a",
		},
		{
			name: "edge from unknown node",
			yaml: flowWith(cmd("a"), "  - {from: ghost, to: a}\n"),
			code: CodeUnknownNode, path: "edges[0].from", node: "ghost", msg: `unknown node "ghost"`,
		},
		{
			name: "edge to unknown node",
			yaml: flowWith(cmd("a"), "  - {from: a, to: ghost}\n"),
			code: CodeUnknownNode, path: "edges[0].to", node: "ghost", msg: `unknown node "ghost"`,
		},
		{
			name: "edge from undeclared port",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.', ports: [small]}\n"+cmd("b"),
				"  - {from: a, port: large, to: b}\n"),
			code: CodeUndeclaredPort, path: "edges[0].port", node: "a", msg: `no port "large" (ports: small, error)`,
		},
		{
			name: "denied port exists only on approve agents",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: readonly, prompt: pr-review}\n"+cmd("b"),
				"  - {from: a, port: denied, to: b}\n"),
			code: CodeUndeclaredPort, path: "edges[0].port", node: "a", msg: `no port "denied"`,
		},
		{
			name: "denied port needs requires_approval",
			yaml: flowWith(cmd("a")+cmd("b"), "  - {from: a, to: b, port: denied}\n"),
			code: CodeUndeclaredPort, path: "edges[0].port", node: "a", msg: `no port "denied"`,
		},
		{
			name: "approval node cannot also require approval",
			yaml: flowWith("  - {id: a, type: approval, title: ok, requires_approval: true}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].requires_approval", node: "a", msg: `field "requires_approval" is not valid on approval nodes`,
		},
		{
			name: "approval_timeout needs requires_approval",
			yaml: flowWith("  - {id: a, type: inbox, title: t, approval_timeout: 1h}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].approval_timeout", node: "a", msg: "approval_timeout is only valid with requires_approval",
		},
		{
			name: "duplicate node id",
			yaml: flowWith(cmd("a")+cmd("a"), ""),
			code: CodeDuplicateID, path: "nodes[1].id", node: "a", msg: "first used by nodes[0]",
		},
		{
			name: "duplicate edge",
			yaml: flowWith(cmd("a")+cmd("b"), "  - {from: a, to: b}\n  - {from: a, to: b}\n"),
			code: CodeDuplicateEdge, path: "edges[1]", msg: "duplicate of edges[0]",
		},
		{
			name: "unknown sensor",
			yaml: strings.Replace(flowWith(cmd("a"), "  []\n"), "[github-notifications]", "[github-notifications, nope]", 1),
			code: CodeUnknownSensor, path: "subscribe[1]", msg: `unknown sensor "nope" (available: github-notifications, other)`,
		},
		{
			name: "unknown prompt",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, prompt: missing}\n", "  []\n"),
			code: CodeUnknownPrompt, path: "nodes[0].prompt", node: "a", msg: `unknown prompt "missing" (available: pr-review)`,
		},
		{
			name: "unknown runtime",
			yaml: flowWith("  - {id: a, type: agent, runtime: gemini, mode: auto, prompt: pr-review}\n", "  []\n"),
			code: CodeUnknownRuntime, path: "nodes[0].runtime", node: "a", msg: `unknown runtime "gemini" (available: claude)`,
		},
		{
			name: "invalid node type",
			yaml: flowWith("  - {id: a, type: webhook}\n", "  []\n"),
			code: CodeInvalidEnum, path: "nodes[0].type", node: "a", msg: `type "webhook" is not valid`,
		},
		{
			name: "invalid agent mode",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: yolo, prompt: pr-review}\n", "  []\n"),
			code: CodeInvalidEnum, path: "nodes[0].mode", node: "a", msg: `mode "yolo" is not valid (one of auto | approve | readonly)`,
		},
		{
			name: "agent mode is required",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, prompt: pr-review}\n", "  []\n"),
			code: CodeRequired, path: "nodes[0].mode", node: "a", msg: "mode is required",
		},
		{
			name: "agent with both prompt and instructions",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, prompt: pr-review, instructions: hi}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].instructions", node: "a", msg: "set either prompt (a file in prompts/) or instructions (inline), not both",
		},
		{
			name: "agent with neither prompt nor instructions",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, extra_prompt: hi}\n", "  []\n"),
			code: CodeRequired, path: "nodes[0].instructions", node: "a", msg: "set instructions (inline prompt) or prompt (a file in prompts/)",
		},
		{
			name: "unknown output",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, instructions: hi, output: json}\n", "  []\n"),
			code: CodeInvalidEnum, path: "nodes[0].output", node: "a", msg: "note | none",
		},
		{
			name: "invalid lang",
			yaml: flowWith("  - {id: a, type: command, lang: perl, script: x}\n", "  []\n"),
			code: CodeInvalidEnum, path: "nodes[0].lang", node: "a", msg: `lang "perl" is not valid`,
		},
		{
			name: "invalid inbox priority",
			yaml: flowWith("  - {id: a, type: inbox, title: t, priority: asap}\n", "  []\n"),
			code: CodeInvalidEnum, path: "nodes[0].priority", node: "a", msg: `priority "asap" is not valid (one of low | normal | high | urgent)`,
		},
		{
			name: "approve style needs approve mode",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, approve: proposal, prompt: pr-review}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].approve", node: "a", msg: "only valid with mode approve",
		},
		{
			name: "invalid allow regex",
			yaml: flowWith("  - id: a\n    type: agent\n    runtime: claude\n    mode: readonly\n    prompt: pr-review\n    allow:\n      tools: ['ok', '^gh (pr']\n", "  []\n"),
			code: CodeInvalidRegex, path: "nodes[0].allow.tools[1]", node: "a", msg: "missing closing )",
		},
		{
			name: "template syntax error in inbox title",
			yaml: flowWith("  - {id: a, type: inbox, title: 'PR {{.input.title'}\n", "  []\n"),
			code: CodeInvalidTemplate, path: "nodes[0].title", node: "a", msg: "unclosed action",
		},
		{
			name: "template syntax error in cwd",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.', cwd: '/x/{{.input.a | nosuchfn}}'}\n", "  []\n"),
			code: CodeInvalidTemplate, path: "nodes[0].cwd.path", node: "a", msg: `function "nosuchfn" not defined`,
		},
		{
			name: "template syntax error in extra_prompt",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, prompt: pr-review, extra_prompt: '{{if}}'}\n", "  []\n"),
			code: CodeInvalidTemplate, path: "nodes[0].extra_prompt", node: "a", msg: "missing value for if",
		},
		{
			name: "command fields not allowed on inbox",
			yaml: flowWith("  - {id: a, type: inbox, title: t, script: x}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].script", node: "a", msg: `field "script" is not valid on inbox nodes`,
		},
		{
			name: "ports only on command nodes",
			yaml: flowWith("  - {id: a, type: inbox, title: t, ports: [x]}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].ports", node: "a", msg: `field "ports" is not valid on inbox nodes`,
		},
		{
			name: "implicit ports cannot be declared",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.', ports: [out, error]}\n", "  []\n"),
			code: CodeReservedPort, path: "nodes[0].ports[1]", node: "a", msg: `port "error" is implicit`,
		},
		{
			name: "exec needs argv",
			yaml: flowWith("  - {id: a, type: command, lang: exec}\n", "  []\n"),
			code: CodeRequired, path: "nodes[0].command", node: "a", msg: "command (argv) is required when lang is exec",
		},
		{
			name: "script needs a non-exec lang",
			yaml: flowWith("  - {id: a, type: command, lang: bash, script: x, command: [ls]}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].command", node: "a", msg: "only valid when lang is exec",
		},
		{
			name: "worktree cwd needs repo and ref",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, prompt: pr-review, cwd: {mode: worktree, repo: x}}\n", "  []\n"),
			code: CodeRequired, path: "nodes[0].cwd.ref", node: "a", msg: "cwd.ref is required",
		},
		{
			name: "command nodes cannot use worktrees",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.', cwd: {mode: worktree, repo: x, ref: y}}\n", "  []\n"),
			code: CodeFieldNotAllowed, path: "nodes[0].cwd.mode", node: "a", msg: "only valid on agent nodes",
		},
		{
			name: "mcp server shape",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, prompt: pr-review, mcp_servers: [{name: m, type: http}]}\n", "  []\n"),
			code: CodeRequired, path: "nodes[0].mcp_servers[0].url", node: "a", msg: "url is required for a http mcp server",
		},
		{
			name: "invalid node id",
			yaml: flowWith("  - {id: 'a b', type: inbox, title: t}\n", "  []\n"),
			code: CodeInvalidID, path: "nodes[0].id", node: "a b", msg: `node id "a b" is invalid`,
		},
		{
			name: "unknown field is reported with its location",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.', timout: 5s}\n", "  []\n"),
			code: CodeUnknownField, path: "nodes[0].timout", msg: `unknown field "timout"`,
		},
		{
			name: "bad duration is a type error with a line",
			yaml: flowWith("  - {id: a, type: command, lang: jq, script: '.', timeout: soon}\n", "  []\n"),
			code: CodeYAMLType, path: "nodes[0].timeout", msg: `invalid duration "soon"`,
		},
		{
			name: "unknown cwd key",
			yaml: flowWith("  - {id: a, type: agent, runtime: claude, mode: auto, prompt: pr-review, cwd: {mode: dir, path: x, dir: y}}\n", "  []\n"),
			code: CodeUnknownField, path: "nodes[0].cwd.dir", msg: `unknown field "dir"`,
		},
		{
			name: "negative max_concurrent",
			yaml: strings.Replace(flowWith(cmd("a"), "  []\n"), "enabled: true\n", "enabled: true\nmax_concurrent: -1\n", 1),
			code: CodeInvalidValue, path: "max_concurrent", msg: "must be positive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ds := CheckFlow("t", []byte(tt.yaml), testRefs())
			var errs []Diagnostic
			for _, d := range ds {
				if d.Severity == SeverityError {
					errs = append(errs, d)
				}
			}
			if len(errs) != max(1, tt.wantErr) {
				t.Fatalf("want %d error(s), got %d: %v", max(1, tt.wantErr), len(errs), errs)
			}
			var got *Diagnostic
			for i := range errs {
				if errs[i].Code == tt.code {
					got = &errs[i]
				}
			}
			if got == nil {
				t.Fatalf("no %s diagnostic in %v", tt.code, errs)
			}
			if got.Path != tt.path || got.NodeID != tt.node || !strings.Contains(got.Message, tt.msg) {
				t.Errorf("got path=%q node=%q msg=%q\nwant path=%q node=%q msg containing %q", got.Path, got.NodeID, got.Message, tt.path, tt.node, tt.msg)
			}
			if got.File != "flows/t.yaml" {
				t.Errorf("file = %q, want flows/t.yaml", got.File)
			}
		})
	}
}

func TestYAMLTypeErrorCarriesLine(t *testing.T) {
	_, ds := ParseFlow("", []byte("id: t\nnodes:\n  - {id: a, type: command, lang: jq, script: '.',\n     timeout: soon}\n"))
	if len(ds) != 1 || ds[0].Line != 4 {
		t.Fatalf("want one diagnostic on line 4, got %+v", ds)
	}
}

func TestCycleWithoutEntryAlsoMakesNodesUnreachable(t *testing.T) {
	_, ds := CheckFlow("t", []byte(flowWith(cmd("a")+cmd("b"), "  - {from: a, to: b}\n  - {from: b, to: a}\n")), testRefs())
	codes := map[string][]string{}
	for _, d := range ds {
		codes[d.Code] = append(codes[d.Code], d.NodeID)
	}
	if len(codes[CodeCycle]) != 1 || len(codes[CodeUnreachable]) != 2 {
		t.Fatalf("want 1 cycle and 2 unreachable, got %v", codes)
	}
	for _, d := range ds {
		if d.Code == CodeCycle && !strings.Contains(d.Message, "a -> b -> a") {
			t.Errorf("cycle message = %q", d.Message)
		}
	}
}

func TestFlowWarnings(t *testing.T) {
	_, ds := CheckFlow("t", []byte("id: t\nsubscribe: []\nnodes: []\nedges: []\n"), nil)
	got := map[string]Severity{}
	for _, d := range ds {
		got[d.Code] = d.Severity
	}
	if got[CodeNoSubscription] != SeverityWarning || got[CodeEmptyFlow] != SeverityWarning || HasErrors(ds) {
		t.Errorf("want warnings only for empty flow, got %v", ds)
	}
}

func TestNilRefsSkipReferenceChecks(t *testing.T) {
	_, ds := CheckFlow("t", []byte(flowWith("  - {id: a, type: agent, runtime: x, mode: auto, prompt: y}\n", "  []\n")), nil)
	if HasErrors(ds) {
		t.Fatalf("nil refs must not check references: %v", ds)
	}
	_, ds = CheckFlow("t", []byte(flowWith("  - {id: a, type: agent, runtime: x, mode: auto, prompt: y}\n", "  []\n")), &Refs{Sensors: map[string]bool{}})
	if !HasErrors(ds) {
		t.Fatal("empty non-nil refs must reject the subscribed sensor")
	}
}

func TestMarshalFlowRoundTrip(t *testing.T) {
	f, ds := ParseFlow("triage-github", []byte(requirementsFlow))
	if len(ds) != 0 {
		t.Fatal(ds)
	}
	out, err := MarshalFlow(*f)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, bad := range []string{"\nid:", "id: triage-github", "timeout: 1m", "max_concurrent", "approve: proposal", "port: out"} {
		if strings.Contains(text, bad) {
			t.Errorf("defaults leaked into saved file (%q):\n%s", bad, text)
		}
	}
	for _, want := range []string{"ui: {x: 100, y: 80}", "cwd: {mode: worktree, repo:", "- {from: classify, port: small, to: review-small}", "timeout: 20m", "script: |"} {
		if !strings.Contains(text, want) {
			t.Errorf("saved file lacks %q:\n%s", want, text)
		}
	}
	// Field order: enabled, dry_run, subscribe, nodes, edges.
	last := -1
	for _, key := range []string{"\nenabled:", "\ndry_run:", "\nsubscribe:", "\nnodes:", "\nedges:"} {
		i := strings.Index("\n"+text, key)
		if i <= last {
			t.Errorf("key %q out of order in:\n%s", key, text)
		}
		last = i
	}
	g, ds := ParseFlow("triage-github", out)
	if len(ds) != 0 {
		t.Fatalf("saved file does not parse: %v\n%s", ds, text)
	}
	if h1, h2 := mustJSON(t, f), mustJSON(t, g); h1 != h2 {
		t.Errorf("round trip changed the flow:\n%s\n%s", h1, h2)
	}
}

func TestMarshalFlowKeepsDirCwdAsString(t *testing.T) {
	f := Flow{ID: "x", Nodes: []Node{{ID: "a", Type: NodeCommand, Lang: LangBash, Script: "echo hi\n", Cwd: &Cwd{Mode: CwdDir, Path: "~/code"}}}}
	out, err := MarshalFlow(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "cwd: ~/code") {
		t.Errorf("dir cwd should be a plain string:\n%s", out)
	}
	g, ds := ParseFlow("", out)
	if len(ds) != 0 || g.Nodes[0].Cwd == nil || *g.Nodes[0].Cwd != (Cwd{Mode: CwdDir, Path: "~/code"}) {
		t.Errorf("round trip: %v %+v", ds, g)
	}
}

func TestFlowIDFromFileName(t *testing.T) {
	f, ds := CheckFlow("renamed", []byte(requirementsFlow), nil)
	if f.ID != "renamed" {
		t.Errorf("id = %q, want the file name", f.ID)
	}
	var warn bool
	for _, d := range ds {
		if d.Code == CodeIDMismatch && d.Severity == SeverityWarning && d.Path == "id" {
			warn = true
		}
		if d.Severity == SeverityError {
			t.Errorf("unexpected error %v", d)
		}
	}
	if !warn {
		t.Errorf("want an id_mismatch warning for the legacy id field, got %v", ds)
	}
	if out, _ := MarshalFlow(*f); strings.Contains(string(out), "\nid:") || strings.HasPrefix(string(out), "id:") {
		t.Errorf("marshal wrote an id line:\n%s", out)
	}
}

func TestRequiresApprovalAddsDeniedPort(t *testing.T) {
	f, ds := ParseFlow("t", []byte(flowWith(
		"  - {id: gated, type: command, lang: jq, script: '.', ports: [big], requires_approval: true, approval_timeout: 30m}\n"+
			"  - {id: review, type: agent, runtime: claude, mode: approve, prompt: pr-review, requires_approval: true}\n"+
			"  - {id: plain, type: transform, steps: [{type: filter, conditions: [{field: x, op: exists}]}], requires_approval: true}\n"+
			cmd("denied-end")+cmd("big-end")+cmd("ok-end"),
		"  - {from: gated, port: denied, to: denied-end}\n  - {from: gated, port: big, to: big-end}\n"+
			"  - {from: review, port: denied, to: ok-end}\n  - {from: plain, port: denied, to: ok-end}\n")))
	if f == nil {
		t.Fatalf("parse failed: %v", ds)
	}
	for _, d := range ValidateFlow(f, testRefs()) {
		if d.Severity == SeverityError {
			t.Errorf("unexpected error: %+v", d)
		}
	}
	if got := strings.Join(f.NodeByID("gated").OutPorts(), ","); got != "big,denied,error" {
		t.Errorf("gated ports = %q, want big,denied,error", got)
	}
	if got := strings.Join(f.NodeByID("review").OutPorts(), ","); got != "out,denied,error" {
		t.Errorf("approve agent with requires_approval must list denied once, got %q", got)
	}
}
