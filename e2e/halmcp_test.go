package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent/fakeagent"
)

// halToolScript has the agent call hal's MCP tools like a real runtime: a read (gh_api GET), a write (gh_api POST,
// which hal holds for approval) and the run's context.
func halToolScript() script {
	return script{Turns: []turn{{When: inMode("default"), Steps: []step{
		{Op: fakeagent.OpMCPCall, ID: "c1", Tool: "gh_api", Args: map[string]any{"path": "/repos/o/r/pulls/1", "params": map[string]any{"state": "open"}}},
		{Op: fakeagent.OpMCPCall, ID: "c2", Tool: "gh_api", Args: map[string]any{"method": "POST", "path": "/repos/o/r/issues/1/comments", "params": map[string]any{"body": "hi"}}},
		{Op: fakeagent.OpMCPCall, ID: "c3", Tool: "run_context", Args: map[string]any{}},
		say("finished"),
	}}}}
}

func halToolDaemon(t *testing.T, extra string, opts ...Option) (*Daemon, string) {
	t.Helper()
	d := newDaemon(t, opts...)
	ghLog := filepath.Join(d.Bin, "gh.log")
	d.Script("gh", `printf '%s\n' "$*" >> `+shq(ghLog)+`
case "$*" in *--input*) cat >> `+shq(ghLog)+`; echo >> `+shq(ghLog)+` ;; esac
echo '{"gh":"ok"}'`)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", halToolScript())
	d.Flow("f", approvalFlow("per_tool", extra))
	d.Start()
	return d, ghLog
}

func readLog(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

var tokenRE = regexp.MustCompile(`"--token","(hal_mcp_[A-Za-z0-9_-]+)"`)

// The agent session gets hal's MCP server; gh_api GET runs without a prompt, a POST waits for an approval and runs
// on allow, the preamble leads the prompt, and the token stops working when the run ends.
func TestHalMCPToolsAllow(t *testing.T) {
	d, ghLog := halToolDaemon(t, "")
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("tool_call")
	if ap.PermissionRequest == nil || ap.PermissionRequest.Title != "hal tool: gh_api" ||
		!strings.Contains(string(ap.PermissionRequest.RawInput), "POST") || !strings.Contains(string(ap.PermissionRequest.RawInput), "/issues/1/comments") {
		t.Fatalf("approval does not show the tool and arguments: %+v", ap.PermissionRequest)
	}
	if log := readLog(t, ghLog); !strings.Contains(log, "api -X GET -f state=open -- /repos/o/r/pulls/1") || strings.Contains(log, "POST") {
		t.Fatalf("before approval gh log = %q", log)
	}
	d.Decide(ap.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")

	log := readLog(t, ghLog)
	if !strings.Contains(log, "api -X POST --input - -- /repos/o/r/issues/1/comments") || !strings.Contains(log, `{"body":"hi"}`) {
		t.Errorf("approved POST did not run: %q", log)
	}
	work := d.Node(done, "work")
	run := d.AgentRuns(work.ID)[0]
	tr := d.Transcript(run.ID)
	for _, want := range []string{"mcp[c1] is_error=false {", "mcp[c2] is_error=false {", `mcp[c3] is_error=false {`} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}

	// Every hal call is in the trace; the agent layer let them through without a second prompt.
	var halCalls int
	for _, p := range d.Permissions(run.ID) {
		if strings.HasPrefix(p.Title, "hal tool: ") {
			halCalls++
			if p.Decision != "allow" || !strings.Contains(p.Reason, "ok") {
				t.Errorf("hal call record: %+v", p)
			}
		} else if p.Decision != "allow" {
			t.Errorf("agent-layer request not allowed by policy: %+v", p)
		}
	}
	if halCalls != 3 {
		t.Errorf("hal tool records = %d, want 3", halCalls)
	}

	// Preamble.
	for _, want := range []string{"<hal_context>", "flow: f", "node: work", "flow_run: " + runID, "agent_run: " + run.ID, "time: ", "run_kind: ", "sensor: src", "event: ", `"id": "7"`, "- gh_api:", "- run_context:", "go 7"} {
		if !strings.Contains(run.RenderedPrompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, run.RenderedPrompt)
		}
	}
	if !strings.HasPrefix(run.RenderedPrompt, "<hal_context>") {
		t.Errorf("preamble does not start the prompt:\n%s", run.RenderedPrompt)
	}

	// The token is revoked with the run.
	var calls []byte
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/new" {
			calls, _ = json.Marshal(c["mcp_servers"])
		}
	}
	m := tokenRE.FindSubmatch(calls)
	if m == nil {
		t.Fatalf("session/new carried no hal MCP server token: %s", calls)
	}
	auth := withHeader("Authorization", "Bearer "+string(m[1]))
	d.Get("/api/mcp/tools", auth, withoutCookie()).Want(401)
	d.Post("/api/mcp/call", map[string]any{"name": "now"}, auth, withoutCookie()).Want(401)
	d.Get("/api/mcp/tools", withHeader("Authorization", "Bearer hal_mcp_nope"), withoutCookie()).Want(401)
	if !strings.Contains(string(calls), fmt.Sprintf("http://127.0.0.1:%d", d.Port())) {
		t.Errorf("MCP server does not point at the daemon's own URL: %s", calls)
	}
}

// Denying the POST returns a tool error to the agent and gh is not called.
func TestHalMCPToolsDeny(t *testing.T) {
	d, ghLog := halToolDaemon(t, "")
	runID := d.InjectRun("src", map[string]any{"id": "8"})
	ap := d.WaitApproval("tool_call")
	d.Decide(ap.ID, false, map[string]any{"comment": "nope"})
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	if !strings.Contains(tr, "mcp[c2] is_error=true") || !strings.Contains(tr, "denied by the user: nope") {
		t.Errorf("deny did not reach the agent as a tool error:\n%s", tr)
	}
	if !strings.Contains(tr, "mcp[c3] is_error=false") {
		t.Errorf("the agent did not continue after the denial:\n%s", tr)
	}
	if log := readLog(t, ghLog); strings.Contains(log, "POST") {
		t.Errorf("denied POST ran: %q", log)
	}
}

// A call that waits past approval_timeout fails as a tool error, the approval and its record expire, and the agent
// carries on.
func TestHalMCPApprovalTimeout(t *testing.T) {
	d, ghLog := halToolDaemon(t, "    timeout: 1h\n    approval_timeout: 1s\n", withClock())
	runID := d.InjectRun("src", map[string]any{"id": "9"})
	ap := d.WaitApproval("tool_call")
	// Pending: the src sensor's schedule (1h), the run timeout and the approval timeout.
	if !d.Clock.WaitTimers(3, 30*time.Second) {
		t.Fatal("the schedule, run and approval timers were not registered")
	}
	d.Clock.Advance(2 * time.Second)
	done := d.WaitRun(runID, "succeeded")

	run := d.AgentRuns(d.Node(done, "work").ID)[0]
	var got Approval
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&got)
	var perm PermissionRequest
	for _, p := range d.Permissions(run.ID) {
		if p.ID == got.PermissionRequest.ID {
			perm = p
		}
	}
	if got.Status != "expired" || perm.Decision != "expired" || perm.DecidedBy != "timeout" {
		t.Errorf("approval %+v permission %+v", got, perm)
	}
	if tr := d.Transcript(run.ID); !strings.Contains(tr, "mcp[c2] is_error=true") || !strings.Contains(tr, "approval timeout") || !strings.Contains(tr, "mcp[c3] is_error=false") {
		t.Errorf("transcript:\n%s", tr)
	}
	if strings.Contains(readLog(t, ghLog), "POST") {
		t.Error("the expired POST ran")
	}
	if r := d.Post("/api/approvals/"+ap.ID+"/approve", nil).Want(409); r.Problem() != "invalid-state" {
		t.Errorf("approving an expired approval: %s", r)
	}
}

// A runtime of a known kind is locked down on every session start: Claude's options travel on session/new and on the
// session/load that resumes an approved proposal, and the user's own session_meta keys survive next to them.
func TestRuntimeLockdownIsSentOnNewAndLoad(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "/review {{.input.id}}") // a prompt that would be a slash command
	d.Runtime("fake", script{LoadSession: true, Turns: []turn{
		{When: when("Approved"), Steps: []step{say("executed")}},
		{Steps: []step{say(proposalPlan)}},
	}})
	d.RuntimeKind("fake", "claude")
	d.Flow("f", approvalFlow("proposal", ""))
	d.Start()

	runID := d.InjectRun("src", map[string]any{"id": "5"})
	d.WaitRun(runID, "awaiting_approval")
	d.Decide(d.WaitApproval("proposal").ID, true, nil)
	d.WaitRun(runID, "succeeded")

	var metas []string
	for _, c := range d.AgentCalls("fake") {
		if m := fmt.Sprint(c["method"]); m == "session/new" || m == "session/load" {
			b, _ := json.Marshal(c["meta"])
			metas = append(metas, m+" "+string(b))
		}
	}
	if len(metas) != 2 {
		t.Fatalf("session calls: %v", metas)
	}
	for _, m := range metas {
		for _, want := range []string{`"tools":[]`, `"strictMcpConfig":true`, `"settingSources":[]`, `"allowedTools":["mcp__hal"]`} {
			if !strings.Contains(m, want) {
				t.Errorf("%s lacks %s", m, want)
			}
		}
	}
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/prompt" {
			if text := fmt.Sprint(c["text"]); strings.HasPrefix(strings.TrimLeft(text, " \n"), "/") {
				t.Errorf("a prompt starts with a slash: %q", text)
			}
		}
	}

}
