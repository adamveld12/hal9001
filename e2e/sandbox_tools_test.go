package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

func sandboxDaemon(t *testing.T, approval, lifecycle string, steps []step) (*Daemon, string) {
	t.Helper()
	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: "+lifecycle+"\napproval: "+approval+"\n")
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: append(steps, say("finished"))}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n"))
	d.Start()
	return d, root
}

func mcp(id, tool string, args map[string]any) step {
	return step{Op: fakeagent.OpMCPCall, ID: id, Tool: tool, Args: args}
}

// With approval: auto the agent's sandbox tools run without a prompt inside the workdir; paths that leave the
// workdir are rejected.
func TestSandboxToolsAutoAndEscapes(t *testing.T) {
	d, root := sandboxDaemon(t, "auto", "persistent", []step{
		mcp("c1", "fs_write", map[string]any{"path": "notes/ok.txt", "content": "hello"}),
		mcp("c2", "shell", map[string]any{"command": "echo hi; cat notes/ok.txt"}),
		mcp("c3", "fs_read", map[string]any{"path": "../outside.txt"}),
		mcp("c4", "fs_write", map[string]any{"path": "../evil.txt", "content": "x"}),
		mcp("c5", "fs_list", map[string]any{"path": "notes"}),
	})
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)

	if got, err := os.ReadFile(filepath.Join(root, "notes", "ok.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("fs_write did not create the file in the workdir: %q %v", got, err)
	}
	for _, want := range []string{"mcp[c1] is_error=false wrote 5 bytes", "mcp[c2] is_error=false exit: 0", "stdout:\\nhi\\nhello", "mcp[c3] is_error=true", "mcp[c4] is_error=true", "ok.txt"} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); err == nil {
		t.Error("a write escaped the workdir")
	}
	if n := len(d.Approvals("")); n != 0 {
		t.Error("auto policy asked for approval")
	}
	// input.json is seeded.
	if b, err := os.ReadFile(filepath.Join(root, "input.json")); err != nil || !strings.Contains(string(b), `"id"`) {
		t.Errorf("input.json: %q %v", b, err)
	}
}

// With approval: always fs_write waits in the Inbox with a diff and runs once allowed; a successful per_run workspace
// is removed afterwards.
func TestSandboxToolsApprovalAlways(t *testing.T) {
	d, root := sandboxDaemon(t, "always", "per_run", []step{
		mcp("c1", "fs_write", map[string]any{"path": "report.md", "content": "line one\nline two\n"}),
	})
	runID := d.InjectRun("src", map[string]any{"id": "2"})
	ap := d.WaitApproval("tool_call")
	raw := string(ap.PermissionRequest.RawInput)
	if ap.PermissionRequest.Title != "hal tool: fs_write" || !strings.Contains(raw, "create report.md") || !strings.Contains(raw, "+line one") {
		t.Fatalf("approval does not show the write and its diff: %s", raw)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "runs", "*", "report.md")); len(matches) != 0 {
		t.Fatal("file written before approval")
	}
	d.Decide(ap.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	if !strings.Contains(tr, "mcp[c1] is_error=false wrote 18 bytes") {
		t.Errorf("transcript:\n%s", tr)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "runs")); len(entries) != 0 {
		t.Errorf("per_run workspace kept after a successful run: %v", entries)
	}
}
