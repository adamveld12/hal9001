package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

const echoTool = `id: shout
description: Shout a word
params:
  word: {type: string, required: true}
lang: bash
script: |
  echo "{\"word\": \"$ARG_WORD\", \"stdin\": $(cat)}"
approve: never
`

const askTool = `id: risky
description: Needs approval
params:
  word: {type: string, required: true}
lang: bash
script: echo "ran $ARG_WORD"
approve: always
`

func customToolDaemon(t *testing.T, extra string, calls []step) *Daemon {
	t.Helper()
	d := newDaemon(t)
	for name, body := range map[string]string{"shout": echoTool, "risky": askTool} {
		p := d.Path("tools/" + name + ".yaml")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: append(calls, say("finished"))}}})
	d.Flow("f", approvalFlow("per_tool", extra))
	d.Start()
	return d
}

// A custom tool listed on an agent node is callable by the agent: arguments arrive as ARG_ env and as JSON on
// stdin, the stdout is the result, and a tool with approve: never needs no approval.
func TestCustomToolCalledByAgent(t *testing.T) {
	d := customToolDaemon(t, "    tools: [shout]\n", []step{
		{Op: fakeagent.OpMCPCall, ID: "c1", Tool: "shout", Args: map[string]any{"word": "hey"}},
	})
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	done := d.WaitRun(runID, "succeeded")
	run := d.AgentRuns(d.Node(done, "work").ID)[0]
	tr := d.Transcript(run.ID)
	if !strings.Contains(tr, "mcp[c1] is_error=false") || !strings.Contains(tr, `"word":"hey"`) && !strings.Contains(tr, `"word": "hey"`) {
		t.Errorf("tool result not in transcript:\n%s", tr)
	}
	if !strings.Contains(tr, `stdin\":{\"word\":\"hey`) || !strings.Contains(run.RenderedPrompt, "- shout: Shout a word") {
		t.Errorf("stdin result or preamble entry missing:\n%s\n%s", tr, run.RenderedPrompt)
	}
}

// approve: always (the tool's own setting) goes through the Inbox approval; a node override can lift it.
func TestCustomToolApproval(t *testing.T) {
	d := customToolDaemon(t, "    tools: [risky]\n", []step{
		{Op: fakeagent.OpMCPCall, ID: "c1", Tool: "risky", Args: map[string]any{"word": "boom"}},
	})
	runID := d.InjectRun("src", map[string]any{"id": "2"})
	ap := d.WaitApproval("tool_call")
	if ap.PermissionRequest == nil || ap.PermissionRequest.Title != "hal tool: risky" {
		t.Fatalf("approval: %+v", ap.PermissionRequest)
	}
	d.Decide(ap.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	if !strings.Contains(tr, "mcp[c1] is_error=false") || !strings.Contains(tr, "ran boom") {
		t.Errorf("approved tool did not run:\n%s", tr)
	}
}

// A node that lists a tool that does not exist is rejected with a diagnostic.
func TestCustomToolUnknownIDIsDiagnostic(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", script{})
	d.Start()
	var res struct {
		Diagnostics []struct {
			Code string `json:"code"`
			Path string `json:"path"`
		} `json:"diagnostics"`
	}
	d.Post("/api/flows/validate", map[string]any{"yaml": approvalFlow("per_tool", "    tools: [nope]\n")}).Want(200).JSON(&res)
	for _, x := range res.Diagnostics {
		if x.Code == "unknown_tool" && strings.HasSuffix(x.Path, "tools[0]") {
			return
		}
	}
	t.Errorf("no unknown_tool diagnostic: %+v", res.Diagnostics)
}

const sbxTool = `id: note_file
description: Write a note file in the workdir
params:
  text: {type: string, required: true}
lang: bash
runs_in_sandbox: true
script: |
  echo "$ARG_TEXT" > note.txt
  echo "{\"wrote\": \"$ARG_TEXT\", \"pwd_has_note\": $(test -f note.txt && echo true || echo false)}"
approve: never
`

// A runs_in_sandbox tool runs in the node's sandbox workdir: it writes a file there and returns its output.
func TestCustomToolRunsInSandbox(t *testing.T) {
	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: persistent\napproval: auto\n")
	d.Write("tools/note_file.yaml", sbxTool)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		mcp("c1", "note_file", map[string]any{"text": "hi there"}), say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n    tools: [note_file]\n"))
	d.Start()
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	if b, err := os.ReadFile(filepath.Join(root, "note.txt")); err != nil || strings.TrimSpace(string(b)) != "hi there" {
		t.Fatalf("note.txt in the sandbox workdir: %q %v", b, err)
	}
	if !strings.Contains(tr, "mcp[c1] is_error=false") || !strings.Contains(tr, `wrote`) || !strings.Contains(tr, "pwd_has_note") {
		t.Errorf("tool output missing:\n%s", tr)
	}
}
