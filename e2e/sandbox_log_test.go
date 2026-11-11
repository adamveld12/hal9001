package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

type sandboxLogEntry struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Tool     string `json:"tool"`
	Approval string `json:"approval"`
	Command  string `json:"command"`
	Path     string `json:"path"`
	ExitCode *int   `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
}

type sandboxLog struct {
	Sessions []struct {
		ID        string   `json:"id"`
		Backend   string   `json:"backend"`
		Status    string   `json:"status"`
		Workdir   string   `json:"workdir"`
		Lifecycle string   `json:"lifecycle"`
		EnvNames  []string `json:"env_names"`
		ReadyAt   *string  `json:"ready_at"`
		EndedAt   *string  `json:"ended_at"`
	} `json:"sessions"`
	Entries []sandboxLogEntry `json:"entries"`
	HasMore bool              `json:"has_more"`
}

func (l sandboxLog) find(kind, titlePart string) *sandboxLogEntry {
	for i := range l.Entries {
		if l.Entries[i].Kind == kind && strings.Contains(l.Entries[i].Title, titlePart) {
			return &l.Entries[i]
		}
	}
	return nil
}

// A sandboxed agent run leaves a log on its agent run and node run: provisioning, every command with exit code and
// output, a policy-denied command, file operations and teardown. Env var names are listed, values never appear.
func TestSandboxRunLogIsRecordedAndServed(t *testing.T) {
	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: per_run\napproval: rules\nkeep_alive: 0s\napproval_rules:\n  shell:\n    - {match: '^rm ', action: deny}\n    - {match: '^echo ', action: auto}\n  fs_write:\n    - {path: '**', action: auto}\n")
	d.Write("env.d/sandboxes/local.env", "LOG_SECRET=topsecretvalue\n")
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		mcp("c1", "shell", map[string]any{"command": "echo hello; echo warn >&2; echo $LOG_SECRET; exit 4"}),
		mcp("c2", "shell", map[string]any{"command": "rm -f nothing"}),
		mcp("c3", "fs_write", map[string]any{"path": "notes/a.txt", "content": "hello"}),
		mcp("c4", "fs_read", map[string]any{"path": "missing.txt"}),
		say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n"))
	d.Start()

	done := d.WaitRun(d.InjectRun("src", map[string]any{"id": "1"}), "succeeded")
	node := d.Node(done, "work")
	run := d.AgentRuns(node.ID)[0]

	var log sandboxLog
	d.Get("/api/agent-runs/" + run.ID + "/sandbox-log").Want(200).JSON(&log)
	if len(log.Sessions) != 1 {
		t.Fatalf("sessions = %+v", log.Sessions)
	}
	s := log.Sessions[0]
	if s.Backend != "local" || s.Status != "closed" || s.ReadyAt == nil || s.EndedAt == nil || s.Lifecycle != "per_run" {
		t.Errorf("session = %+v", s)
	}
	if strings.Join(s.EnvNames, ",") == "" || !strings.Contains(strings.Join(s.EnvNames, ","), "LOG_SECRET") {
		t.Errorf("env names = %v", s.EnvNames)
	}
	for _, want := range [][2]string{
		{"lifecycle", "Provisioning local sandbox"}, {"lifecycle", "Sandbox ready"}, {"lifecycle", "Wrote input.json"}, {"lifecycle", "Teardown: run succeeded"},
	} {
		if log.find(want[0], want[1]) == nil {
			t.Errorf("no %s entry %q: %+v", want[0], want[1], log.Entries)
		}
	}

	sh := log.find("exec", "echo hello")
	if sh == nil || sh.ExitCode == nil || *sh.ExitCode != 4 || sh.Tool != "shell" || !strings.HasPrefix(sh.Approval, "auto") {
		t.Fatalf("shell entry = %+v", sh)
	}
	if !strings.Contains(sh.Stdout, "hello") || !strings.Contains(sh.Stderr, "warn") || strings.Contains(sh.Stdout, "topsecretvalue") || !strings.Contains(sh.Stdout, "«redacted»") {
		t.Errorf("shell output: stdout=%q stderr=%q", sh.Stdout, sh.Stderr)
	}
	if den := log.find("denied", "rm -f nothing"); den == nil || den.Level != "warn" || !strings.Contains(den.Approval, "denied by policy") || den.Command != "rm -f nothing" {
		t.Errorf("denied entry = %+v", den)
	}
	if w := log.find("fs", "write notes/a.txt"); w == nil || w.Tool != "fs_write" || w.Path != "notes/a.txt" || !strings.HasPrefix(w.Approval, "auto") {
		t.Errorf("fs_write entry = %+v", w)
	}
	if r := log.find("fs", "read missing.txt"); r == nil || r.Level != "warn" || r.Error == "" {
		t.Errorf("fs_read entry = %+v", r)
	}

	// The node run serves the same entries; the download is plain text with the same content.
	var byNode sandboxLog
	d.Get("/api/node-runs/" + node.ID + "/sandbox-log").Want(200).JSON(&byNode)
	if len(byNode.Entries) != len(log.Entries) {
		t.Errorf("node run entries = %d, agent run entries = %d", len(byNode.Entries), len(log.Entries))
	}
	raw := d.Get("/api/agent-runs/" + run.ID + "/sandbox-log/raw").Want(200)
	if !strings.Contains(string(raw.Body), "echo hello") || !strings.Contains(raw.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("raw download: %v\n%s", raw.Header, raw.Body)
	}
	if strings.Contains(string(raw.Body), "topsecretvalue") {
		t.Error("the raw log contains the secret value")
	}

	d.Get("/api/agent-runs/01JZZZZZZZZZZZZZZZZZZZZZZZ/sandbox-log").Want(404)
}

// A user's decision on a sandbox call is part of the log: approved calls say so, denied ones are logged as denied.
func TestSandboxRunLogRecordsHumanDecisions(t *testing.T) {
	d, _ := sandboxDaemon(t, "always", "per_run", []step{
		mcp("c1", "fs_write", map[string]any{"path": "ok.txt", "content": "one"}),
		mcp("c2", "shell", map[string]any{"command": "echo should-not-run"}),
	})
	runID := d.InjectRun("src", map[string]any{"id": "7"})
	d.Decide(d.WaitApproval("tool_call").ID, true, nil)
	d.Decide(d.WaitApproval("tool_call").ID, false, map[string]any{"comment": "no shell today"})
	done := d.WaitRun(runID, "succeeded")
	run := d.AgentRuns(d.Node(done, "work").ID)[0]

	var log sandboxLog
	d.Get("/api/agent-runs/" + run.ID + "/sandbox-log").Want(200).JSON(&log)
	if w := log.find("fs", "write ok.txt"); w == nil || w.Approval != "approved by user" {
		t.Errorf("approved write entry = %+v", w)
	}
	den := log.find("denied", "echo should-not-run")
	if den == nil || !strings.Contains(den.Approval, "denied by user") || !strings.Contains(den.Approval, "no shell today") {
		t.Errorf("denied entry = %+v", den)
	}
	if log.find("exec", "should-not-run") != nil {
		t.Error("the denied command ran")
	}
}
