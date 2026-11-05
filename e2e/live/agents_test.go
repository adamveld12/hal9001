package live

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runtimeUnderTest describes how one real runtime is exercised. model is the
// cheapest model for the runtime; override with HAL9001_LIVE_MODEL_<RUNTIME>.
type runtimeUnderTest struct {
	name     string
	model    string
	perTool  bool // the gated policy is exercised only where the gate proved deny-and-continue works
	envModel string
}

var runtimesUnderTest = []runtimeUnderTest{
	{name: "claude", model: "haiku", perTool: true, envModel: "HAL9001_LIVE_MODEL_CLAUDE"},
	{name: "codex", model: "gpt-6-luna", perTool: false, envModel: "HAL9001_LIVE_MODEL_CODEX"},
	{name: "omp", model: "anthropic/claude-haiku-4-5", perTool: true, envModel: "HAL9001_LIVE_MODEL_OMP"},
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
	// writePrompt asks for a file write with the agent's file-writing tool.
	writePrompt = `---
name: live-write
description: Live smoke, ask the agent to create a file
output: none
---
Create a file named {{.input.file}} in your current working directory whose entire content is the single line: {{.input.content}}
Use your file-writing/editing tool (not a shell command). Do nothing else. If the tool is refused, say so in one sentence and stop; do not retry or use another way to write it.
`
)

func (s *stack) seedPrompts() {
	s.createPrompt(notePrompt)
	s.createPrompt(writePrompt)
}

// silentSensor registers a sensor that never emits; events come from injection.
func (s *stack) silentSensor(id string) {
	s.createSensor(fmt.Sprintf("id: %s\ninterval: 24h\nlang: bash\nscript: 'true'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n", id))
}

// agentFlow registers one flow with a single agent node subscribed to its own silent sensor.
func (s *stack) agentFlow(id string, rt runtimeUnderTest, mode, approve, prompt, cwd string) {
	s.t.Helper()
	s.silentSensor(id)
	approveLine := ""
	if approve != "" {
		approveLine = "    approve: " + approve + "\n"
	}
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
    mode: %s
%s    prompt: %s
    model: %q
%s    timeout: 8m
    approval_timeout: 10m
`, id, id, rt.name, mode, approveLine, prompt, rt.modelName(), cwdLine))
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

	s.agentFlow("live-ghost", ghost, "readonly", "", "live-note", t.TempDir())
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
	s.agentFlow("live-crash", rt, "readonly", "", "live-note", t.TempDir())
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
			t.Run("readonly_rejects_write", func(t *testing.T) {
				s := &stack{t: t, home: s.home, base: s.base, http: s.http}
				readonlyRejectsWrite(s, rt)
			})
			t.Run("approve_proposal", func(t *testing.T) {
				s := &stack{t: t, home: s.home, base: s.base, http: s.http}
				approveProposal(s, rt)
			})
			if rt.perTool {
				t.Run("approve_per_tool", func(t *testing.T) { s := &stack{t: t, home: s.home, base: s.base, http: s.http}; approvePerTool(s, rt) })
			}
		})
	}
}

// (a) a readonly agent turns a fixed message into a §7.4 note in the inbox.
func readonlyNote(s *stack, rt runtimeUnderTest) {
	t := s.t
	flow := "live-" + rt.name + "-note"
	s.agentFlow(flow, rt, "readonly", "", "live-note", t.TempDir())
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

// (b) a readonly agent asked to create a file: the write is refused by policy and the file does not exist.
func readonlyRejectsWrite(s *stack, rt runtimeUnderTest) {
	t := s.t
	dir := t.TempDir()
	flow := "live-" + rt.name + "-ro-write"
	s.agentFlow(flow, rt, "readonly", "", "live-write", dir)
	res := s.inject(flow, map[string]any{"id": "w-1", "file": "ro-should-not-exist.txt", "content": "readonly-leak"})
	det := s.waitFlowRun(res.FlowRuns[0].ID, 6*time.Minute)
	runs := s.agentRuns(det.FlowRun.ID)
	var reqs []permissionRequest
	for _, r := range runs {
		reqs = append(reqs, s.permissionRequests(r.ID)...)
	}
	_, statErr := os.Stat(filepath.Join(dir, "ro-should-not-exist.txt"))
	evidence(t, rt.name+"-b-readonly-write", map[string]any{"flow_run": det, "agent_runs": runs, "permission_requests": reqs, "file_exists": statErr == nil})

	if statErr == nil {
		t.Fatal("readonly agent created the file")
	}
	if len(runs) != 1 {
		t.Fatalf("agent runs = %d, want 1", len(runs))
	}
	rejected := 0
	for _, r := range reqs {
		if r.ToolKind == "read" || r.ToolKind == "search" || r.ToolKind == "fetch" || r.ToolKind == "think" {
			continue
		}
		if r.DecidedBy == nil || *r.DecidedBy != "policy" || r.Decision == nil || (*r.Decision != "reject" && *r.Decision != "cancelled") {
			t.Errorf("non-read request %q (%s) decision=%v by=%v, want rejected by policy", r.Title, r.ToolKind, deref(r.Decision), deref(r.DecidedBy))
			continue
		}
		rejected++
	}
	if rejected == 0 {
		t.Logf("NOTE: no permission request reached the client (the runtime refused or sandboxed the write itself); final=%q", deref(runs[0].FinalMessage))
	}
}

// (c) approve/proposal: a proposal waits in the queue, the file appears only after approval, and the same session resumes.
func approveProposal(s *stack, rt runtimeUnderTest) {
	t := s.t
	dir := t.TempDir()
	flow := "live-" + rt.name + "-proposal"
	s.agentFlow(flow, rt, "approve", "proposal", "live-write", dir)
	res := s.inject(flow, map[string]any{"id": "p-1", "file": "approved.txt", "content": "hello from " + rt.name})
	frID := res.FlowRuns[0].ID
	det := s.waitFlowRun(frID, 6*time.Minute)
	if det.FlowRun.Status != "awaiting_approval" {
		runs := s.agentRuns(frID)
		evidence(t, rt.name+"-c-proposal-unexpected", map[string]any{"flow_run": det, "agent_runs": runs})
		t.Fatalf("flow run %s, want awaiting_approval (error %v)", det.FlowRun.Status, deref(det.FlowRun.Error))
	}
	pending := s.approvals("status=pending&flow_run_id=" + frID)
	if len(pending) != 1 || pending[0].Type != "proposal" || pending[0].ProposalMD == nil || strings.TrimSpace(*pending[0].ProposalMD) == "" {
		t.Fatalf("pending approvals = %+v, want exactly one non-empty proposal", pending)
	}
	if _, err := os.Stat(filepath.Join(dir, "approved.txt")); err == nil {
		t.Fatal("file exists before approval")
	}
	phase1 := s.agentRuns(frID)

	var decided approval
	s.must(http.StatusOK, http.MethodPost, "/api/approvals/"+pending[0].ID+"/approve", map[string]string{"comment": "Go ahead."}, &decided)
	det = s.waitFlowRun(frID, 8*time.Minute)
	runs := s.agentRuns(frID)
	var reqs []permissionRequest
	for _, r := range runs {
		reqs = append(reqs, s.permissionRequests(r.ID)...)
	}
	body, readErr := os.ReadFile(filepath.Join(dir, "approved.txt"))
	evidence(t, rt.name+"-c-proposal", map[string]any{
		"proposal": pending[0], "decision": decided, "phase1_runs": phase1, "flow_run": det, "agent_runs": runs,
		"permission_requests": reqs, "file": string(body), "file_error": fmt.Sprint(readErr),
	})

	if decided.Status != "approved" {
		t.Errorf("approval status = %q, want approved", decided.Status)
	}
	if det.FlowRun.Status != "succeeded" {
		t.Fatalf("flow run %s after approval, want succeeded (error %v)", det.FlowRun.Status, deref(det.FlowRun.Error))
	}
	if readErr != nil {
		t.Fatalf("approved.txt was not created in the dir cwd: %v", readErr)
	}
	if !strings.Contains(string(body), "hello from "+rt.name) {
		t.Errorf("approved.txt = %q", body)
	}
	var exec *agentRun
	for i := range runs {
		if runs[i].Phase == "execute" {
			exec = &runs[i]
		}
	}
	if exec == nil {
		t.Fatalf("no execute-phase agent run in %+v", runs)
	}
	t.Logf("%s resumed_via=%s fallback=%s status=%s policy=%s", rt.name, deref(exec.ResumedVia), deref(exec.Fallback), exec.Status, exec.Policy)
	if exec.ResumedVia == nil {
		t.Error("execute phase has no resumed_via")
	}
	if exec.Policy != "auto" {
		t.Errorf("execute-phase policy = %q, want auto", exec.Policy)
	}
}

// (d) approve/per_tool: reads pass, the edit becomes a tool_call approval, allow applies it.
func approvePerTool(s *stack, rt runtimeUnderTest) {
	t := s.t
	dir := t.TempDir()
	flow := "live-" + rt.name + "-per-tool"
	s.agentFlow(flow, rt, "approve", "per_tool", "live-write", dir)
	res := s.inject(flow, map[string]any{"id": "t-1", "file": "per-tool.txt", "content": "per-tool from " + rt.name})
	frID := res.FlowRuns[0].ID

	var allowed []approval
	deadline := time.Now().Add(10 * time.Minute)
	sawEditPending := false
	for {
		var det flowRunDetail
		s.must(http.StatusOK, http.MethodGet, "/api/flow-runs/"+frID+"?wait=5s", nil, &det)
		for _, ap := range s.approvals("status=pending&flow_run_id=" + frID) {
			if ap.Type != "tool_call" || ap.PermissionRequest == nil {
				t.Fatalf("unexpected approval %+v", ap)
			}
			if _, err := os.Stat(filepath.Join(dir, "per-tool.txt")); err == nil && ap.PermissionRequest.ToolKind == "edit" {
				t.Fatal("file exists while its edit approval is still pending")
			}
			if ap.PermissionRequest.ToolKind == "edit" {
				sawEditPending = true
			}
			var got approval
			s.must(http.StatusOK, http.MethodPost, "/api/approvals/"+ap.ID+"/approve", map[string]string{"comment": "live smoke allow"}, &got)
			allowed = append(allowed, ap)
		}
		if terminal(det.FlowRun.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("flow run still %q", det.FlowRun.Status)
		}
	}
	var det flowRunDetail
	s.must(http.StatusOK, http.MethodGet, "/api/flow-runs/"+frID, nil, &det)
	runs := s.agentRuns(frID)
	var reqs []permissionRequest
	for _, r := range runs {
		reqs = append(reqs, s.permissionRequests(r.ID)...)
	}
	body, readErr := os.ReadFile(filepath.Join(dir, "per-tool.txt"))
	evidence(t, rt.name+"-d-per-tool", map[string]any{"approvals": allowed, "flow_run": det, "agent_runs": runs, "permission_requests": reqs, "file": string(body), "file_error": fmt.Sprint(readErr)})

	if det.FlowRun.Status != "succeeded" {
		t.Fatalf("flow run %s, want succeeded (error %v)", det.FlowRun.Status, deref(det.FlowRun.Error))
	}
	if !sawEditPending {
		t.Fatalf("no edit tool_call approval was queued; approvals=%+v", allowed)
	}
	if readErr != nil || !strings.Contains(string(body), "per-tool from "+rt.name) {
		t.Fatalf("per-tool.txt = %q, err %v", body, readErr)
	}
	for _, r := range reqs {
		if r.ToolKind == "read" && (r.DecidedBy == nil || *r.DecidedBy != "policy") {
			t.Errorf("read request %q was not allowed by policy: by=%v", r.Title, deref(r.DecidedBy))
		}
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
