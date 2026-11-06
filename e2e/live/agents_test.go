package live

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// runtimeUnderTest describes how one real runtime is exercised. model is the
// cheapest model for the runtime; override with HAL9001_LIVE_MODEL_<RUNTIME>.
type runtimeUnderTest struct {
	name     string
	model    string
	envModel string
}

var runtimesUnderTest = []runtimeUnderTest{
	{name: "claude", model: "haiku", envModel: "HAL9001_LIVE_MODEL_CLAUDE"},
	{name: "codex", model: "gpt-6-luna", envModel: "HAL9001_LIVE_MODEL_CODEX"},
	{name: "omp", model: "anthropic/claude-haiku-4-5", envModel: "HAL9001_LIVE_MODEL_OMP"},
}

func (r runtimeUnderTest) modelName() string {
	if v := os.Getenv(r.envModel); v != "" {
		return v
	}
	return r.model
}

// selected reports whether the runtime is enabled by HAL9001_LIVE_RUNTIMES
// (comma-separated; default all).
func (r runtimeUnderTest) selected() bool {
	v := os.Getenv("HAL9001_LIVE_RUNTIMES")
	if v == "" {
		return true
	}
	for _, n := range strings.Split(v, ",") {
		if strings.TrimSpace(n) == r.name {
			return true
		}
	}
	return false
}

const (
	notePrompt = `---
name: live-note
description: Live smoke, structured note about a fixed message
output: note
---
This is a connectivity test. Do not use any tools and do not read any files.

Message to triage: {{.input.text}}

Answer with exactly one JSON object and nothing else, in this shape:
{"title": "short headline", "priority": "low | normal | high | urgent", "summary_md": "one sentence", "links": []}
Choose priority "urgent" because production is down.
`
)

func (s *stack) seedPrompts() {
	s.createPrompt(notePrompt)
	s.createPrompt(lockdownPrompt)
}

// silentSensor registers a sensor that never emits; events come from injection.
func (s *stack) silentSensor(id string) {
	s.createSensor(fmt.Sprintf("id: %s\ninterval: 24h\nlang: bash\nscript: 'true'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n", id))
}

// agentFlow registers one flow with a single agent node subscribed to its own silent sensor.
func (s *stack) agentFlow(id string, rt runtimeUnderTest, prompt, cwd string) {
	s.t.Helper()
	s.silentSensor(id)
	cwdLine := ""
	if cwd != "" {
		cwdLine = fmt.Sprintf("    cwd: {mode: dir, path: %q}\n", cwd)
	}
	s.createFlow(fmt.Sprintf(`id: %s
enabled: true
dry_run: false
subscribe: [%s]
max_concurrent: 1
nodes:
  - id: agent
    type: agent
    runtime: %s
    mode: readonly
    prompt: %s
    model: %q
%s    timeout: 8m
    approval_timeout: 10m
`, id, id, rt.name, prompt, rt.modelName(), cwdLine))
}

func TestLiveRuntimeProbes(t *testing.T) {
	requireLive(t)
	s := startStack(t, stackOpts{})
	for _, rt := range runtimesUnderTest {
		if !rt.selected() {
			continue
		}
		t.Run(rt.name, func(t *testing.T) {
			s := &stack{t: t, home: s.home, base: s.base, http: s.http}
			var probe map[string]any
			s.must(http.StatusOK, http.MethodPost, "/api/runtimes/"+rt.name+"/probe", map[string]any{}, &probe)
			evidence(t, "probe-"+rt.name, probe)
			if ok, _ := probe["ok"].(bool); !ok {
				t.Fatalf("probe failed: error=%v stderr=%v", probe["error"], probe["stderr"])
			}
		})
	}
}

// A runtime whose command does not exist fails the run with a clear error
// quickly (no hang), and the probe says so too.
func TestLiveMissingRuntimeCommand(t *testing.T) {
	requireLive(t)
	s := startStack(t, stackOpts{configPatch: func(cfg string) string {
		return strings.Replace(cfg, "runtimes:\n", "runtimes:\n  ghost:\n    command: [no-such-acp-agent]\n    modes: {ask: default, full: bypassPermissions}\n  crash:\n    command: [sh, -c, \"echo 'No version is set for shim' >&2; exit 1\"]\n    modes: {ask: default, full: bypassPermissions}\n", 1)
	}})
	s.seedPrompts()
	ghost := runtimeUnderTest{name: "ghost", model: "x"}

	var probe map[string]any
	s.must(http.StatusOK, http.MethodPost, "/api/runtimes/ghost/probe", map[string]any{}, &probe)
	if ok, _ := probe["ok"].(bool); ok || !strings.Contains(fmt.Sprint(probe["error"]), "no-such-acp-agent") {
		t.Errorf("probe = %v, want ok=false naming the command", probe)
	}

	s.agentFlow("live-ghost", ghost, "live-note", t.TempDir())
	start := time.Now()
	res := s.inject("live-ghost", map[string]any{"id": "g-1", "text": "x"})
	det := s.waitFlowRun(res.FlowRuns[0].ID, time.Minute)
	runs := s.agentRuns(det.FlowRun.ID)
	evidence(t, "ghost-missing-command", map[string]any{"probe": probe, "flow_run": det, "agent_runs": runs, "seconds": time.Since(start).Seconds()})
	if det.FlowRun.Status != "failed" || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("flow run %s, agent runs %+v, want failed", det.FlowRun.Status, runs)
	}
	if e := deref(runs[0].Error); !strings.Contains(e, "no-such-acp-agent") {
		t.Errorf("agent run error = %q, want it to name the missing command", e)
	}
}

// A runtime command that starts but dies at once (a mise shim outside its
// repo does this) surfaces the process's stderr in the agent run error.
func TestLiveCrashingRuntimeCommand(t *testing.T) {
	requireLive(t)
	s := startStack(t, stackOpts{configPatch: func(cfg string) string {
		return strings.Replace(cfg, "runtimes:\n", "runtimes:\n  crash:\n    command: [sh, -c, \"echo 'No version is set for shim' >&2; exit 1\"]\n    modes: {ask: default, full: bypassPermissions}\n", 1)
	}})
	s.seedPrompts()
	rt := runtimeUnderTest{name: "crash", model: "x"}
	s.agentFlow("live-crash", rt, "live-note", t.TempDir())
	res := s.inject("live-crash", map[string]any{"id": "c-1", "text": "x"})
	det := s.waitFlowRun(res.FlowRuns[0].ID, time.Minute)
	runs := s.agentRuns(det.FlowRun.ID)
	var probe map[string]any
	s.must(http.StatusOK, http.MethodPost, "/api/runtimes/crash/probe", map[string]any{}, &probe)
	evidence(t, "crash-at-startup", map[string]any{"probe": probe, "flow_run": det, "agent_runs": runs})
	if det.FlowRun.Status != "failed" || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("flow run %s, agent runs %+v, want failed", det.FlowRun.Status, runs)
	}
	if e := deref(runs[0].Error); !strings.Contains(e, "No version is set for shim") {
		t.Errorf("agent run error = %q, want it to carry the process's stderr", e)
	}
	if e := fmt.Sprint(probe["error"]); !strings.Contains(e, "No version is set for shim") {
		t.Errorf("probe error = %q, want the process's stderr", e)
	}
}

func TestLiveAgents(t *testing.T) {
	requireLive(t)
	s := startStack(t, stackOpts{})
	s.seedPrompts()
	for _, rt := range runtimesUnderTest {
		if !rt.selected() {
			continue
		}
		t.Run(rt.name, func(t *testing.T) {
			s := &stack{t: t, home: s.home, base: s.base, http: s.http}
			t.Run("readonly_note", func(t *testing.T) { s := &stack{t: t, home: s.home, base: s.base, http: s.http}; readonlyNote(s, rt) })
			t.Run("lockdown", func(t *testing.T) { s := &stack{t: t, home: s.home, base: s.base, http: s.http}; lockdown(s, rt) })
		})
	}
}

// (a) a readonly agent turns a fixed message into a §7.4 note in the inbox.
func readonlyNote(s *stack, rt runtimeUnderTest) {
	t := s.t
	flow := "live-" + rt.name + "-note"
	s.agentFlow(flow, rt, "live-note", t.TempDir())
	res := s.inject(flow, map[string]any{
		"id": "note-1", "text": "Production API gateway has returned HTTP 500 for every request since 14:05 and on-call is not answering.",
	})
	if len(res.FlowRuns) != 1 {
		t.Fatalf("flow runs = %+v, want 1", res.FlowRuns)
	}
	det := s.waitFlowRun(res.FlowRuns[0].ID, 6*time.Minute)
	runs := s.agentRuns(det.FlowRun.ID)
	inbox := s.inboxFor(flow)
	var reqs []permissionRequest
	for _, r := range runs {
		reqs = append(reqs, s.permissionRequests(r.ID)...)
	}
	evidence(t, rt.name+"-a-readonly-note", map[string]any{"flow_run": det, "agent_runs": runs, "inbox": inbox, "permission_requests": reqs})

	if det.FlowRun.Status != "succeeded" {
		t.Fatalf("flow run %s, want succeeded (error %v)", det.FlowRun.Status, det.FlowRun.Error)
	}
	if len(runs) == 1 && !s.modelApplied(runs[0].ID, rt.modelName()) {
		t.Errorf("the harness did not record set_model %q as applied", rt.modelName())
	}
	if len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].Policy != "readonly" {
		t.Fatalf("agent runs = %+v, want one succeeded readonly run", runs)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox items = %d, want 1", len(inbox))
	}
	it := inbox[0]
	if it.ParseError {
		t.Fatalf("note did not parse; raw summary: %q", it.SummaryMD)
	}
	if it.Title == "" {
		t.Error("inbox title is empty")
	}
	if it.Priority != "urgent" && it.Priority != "high" {
		t.Errorf("priority = %q, want urgent or high for a production outage", it.Priority)
	}
	if it.AgentRunID == nil || *it.AgentRunID != runs[0].ID {
		t.Errorf("inbox item agent_run_id = %v, want %s", it.AgentRunID, runs[0].ID)
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// modelApplied reports whether the transcript has the harness's set_model
// action for model with applied=true.
func (s *stack) modelApplied(agentRunID, model string) bool {
	s.t.Helper()
	var list struct {
		Items []struct {
			Kind    string `json:"kind"`
			Payload struct {
				Action  string `json:"action"`
				Model   string `json:"model"`
				Applied bool   `json:"applied"`
			} `json:"payload"`
		} `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/agent-runs/"+agentRunID+"/events?limit=200", nil, &list)
	for _, ev := range list.Items {
		if ev.Kind == "harness" && ev.Payload.Action == "set_model" && ev.Payload.Model == model {
			return ev.Payload.Applied
		}
	}
	return false
}

const lockdownPrompt = `---
name: live-lockdown
description: Live smoke, what can the agent reach
output: note
---
This is a tool-availability test. Carry out each step with your own tools, in order. Never guess or invent a result: if a
step cannot be done because you have no suitable tool, write NO TOOL for that step and go on.
A) Read the file /etc/hosts and quote its first line.
B) Run the shell command: echo hi   (quote its output)
C) Call the "now" tool of the MCP server named hal and quote the date it returns.
D) List the exact name of every tool you have in this session.

Answer with exactly one JSON object and nothing else:
{"title": "A: <result>; B: <result>; C: <result>", "priority": "low", "summary_md": "one line per step, then the tool names for D", "links": []}
`

// (b) lockdown: the agent can call hal's `now` and nothing else of what it would ship with (read a file, run a command).
func lockdown(s *stack, rt runtimeUnderTest) {
	t := s.t
	flow := "live-" + rt.name + "-lockdown"
	s.agentFlow(flow, rt, "live-lockdown", t.TempDir())
	res := s.inject(flow, map[string]any{"id": "l-1"})
	det := s.waitFlowRun(res.FlowRuns[0].ID, 6*time.Minute)
	runs := s.agentRuns(det.FlowRun.ID)
	inbox := s.inboxFor(flow)
	var reqs []permissionRequest
	var tools []string
	for _, r := range runs {
		reqs = append(reqs, s.permissionRequests(r.ID)...)
		tools = append(tools, s.toolCallTitles(r.ID)...)
	}
	var raw string
	if len(runs) == 1 {
		raw = s.rawLog(runs[0].ID)
	}
	evidence(t, rt.name+"-b-lockdown", map[string]any{"flow_run": det, "agent_runs": runs, "inbox": inbox, "permission_requests": reqs, "tool_call_titles": tools})
	evidence(t, rt.name+"-b-lockdown-raw", map[string]any{"jsonrpc": strings.Split(raw, "\n")})

	if det.FlowRun.Status != "succeeded" || len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatalf("flow run %s, agent runs %+v", det.FlowRun.Status, runs)
	}
	// Every tool call of the transcript is a call of hal's MCP server (its record is a "hal tool: ..." request),
	// and no permission request for a native tool reached the client.
	var halRecords, halNow int
	for _, r := range reqs {
		switch {
		case strings.HasPrefix(r.Title, "hal tool: "):
			halRecords++
			if r.Title == "hal tool: now" && deref(r.Decision) == "allow" && strings.Contains(deref(r.Reason), "ok") {
				halNow++
			}
		case strings.Contains(r.Title, "hal"):
			// the agent layer's own request for a hal tool, allowed by policy
		default:
			t.Errorf("a permission request for a non-hal tool reached the client: %q kind=%s decision=%s", r.Title, r.ToolKind, deref(r.Decision))
		}
	}
	if halNow != 1 || len(tools) != halRecords {
		t.Errorf("tool calls %v vs hal records %d (now executed %d times): the agent used something other than hal's tools", tools, halRecords, halNow)
	}
	// The answers: A and B have no tool, C has a date from hal.
	if len(inbox) != 1 || inbox[0].ParseError {
		t.Fatalf("inbox = %+v", inbox)
	}
	text := inbox[0].Title + "\n" + inbox[0].SummaryMD
	for _, leak := range []string{"localhost", "127.0.0.1"} {
		if strings.Contains(strings.ToLower(text), leak) {
			t.Errorf("the answer quotes /etc/hosts (%q):\n%s", leak, text)
		}
	}
	if !strings.Contains(text, "2026") {
		t.Errorf("the answer carries no date from now:\n%s", text)
	}
	t.Logf("%s answer: %s", rt.name, text)
}

// toolCallTitles are the titles of the tool_call events in an agent run's transcript.
func (s *stack) toolCallTitles(agentRunID string) []string {
	s.t.Helper()
	var list struct {
		Items []struct {
			Kind    string `json:"kind"`
			Payload struct {
				Title string `json:"title"`
			} `json:"payload"`
		} `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/agent-runs/"+agentRunID+"/events?kind=tool_call&limit=200", nil, &list)
	var out []string
	for _, ev := range list.Items {
		if ev.Kind == "tool_call" {
			out = append(out, ev.Payload.Title)
		}
	}
	return out
}

// rawLog is the JSON-RPC traffic of an agent run.
func (s *stack) rawLog(agentRunID string) string {
	s.t.Helper()
	resp, err := s.http.Get(s.base + "/api/agent-runs/" + agentRunID + "/raw")
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
