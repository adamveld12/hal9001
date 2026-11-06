package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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

func halToolDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	d := newDaemon(t)
	ghLog := filepath.Join(d.Bin, "gh.log")
	d.Script("gh", `printf '%s\n' "$*" >> `+shq(ghLog)+`
case "$*" in *--input*) cat >> `+shq(ghLog)+`; echo >> `+shq(ghLog)+` ;; esac
echo '{"gh":"ok"}'`)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", halToolScript())
	d.Flow("f", approvalFlow("per_tool", ""))
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
	d, ghLog := halToolDaemon(t)
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
	d, ghLog := halToolDaemon(t)
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
