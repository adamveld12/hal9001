package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent/fakeagent"
	"hal9001/internal/hashline"
)

func sandboxDaemon(t *testing.T, approval, lifecycle string, steps []step) (*Daemon, string) {
	t.Helper()
	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: "+lifecycle+"\napproval: "+approval+"\nkeep_alive: 0s\n")
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
		mcp("c1", "fs_write", map[string]any{"path": "notes/ok.txt", "expected_hash": "", "content": "hello"}),
		mcp("c2", "shell", map[string]any{"command": "echo hi; cat notes/ok.txt"}),
		mcp("c3", "fs_read", map[string]any{"path": "../outside.txt"}),
		mcp("c4", "fs_write", map[string]any{"path": "../evil.txt", "expected_hash": "", "content": "x"}),
		mcp("c5", "fs_list", map[string]any{"path": "notes"}),
	})
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)

	if got, err := os.ReadFile(filepath.Join(root, "notes", "ok.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("fs_write did not create the file in the workdir: %q %v", got, err)
	}
	for _, want := range []string{"mcp[c1] is_error=false created notes/ok.txt: 5 bytes", "mcp[c2] is_error=false exit: 0", "stdout:\\nhi\\nhello", "mcp[c3] is_error=true", "mcp[c4] is_error=true", "ok.txt"} {
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
		mcp("c1", "fs_write", map[string]any{"path": "report.md", "expected_hash": "", "content": "line one\nline two\n"}),
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
	if !strings.Contains(tr, "mcp[c1] is_error=false created report.md: 18 bytes") {
		t.Errorf("transcript:\n%s", tr)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "runs")); len(entries) != 0 {
		t.Errorf("per_run workspace kept after a successful run: %v", entries)
	}
}

// A policy deny rule refuses the call: the agent gets a tool error, nothing runs, and the trace records a denied
// permission request decided by policy (not an allowed one).
func TestSandboxPolicyDenyIsRecordedAsDenied(t *testing.T) {
	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: persistent\napproval: rules\napproval_rules:\n  shell:\n    - {match: '^rm ', action: deny}\n    - {match: '^echo ', action: auto}\n")
	d.IdleSensor("src")
	d.Prompt("p", "none", "go")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		mcp("c1", "shell", map[string]any{"command": "echo keep > keep.txt"}),
		mcp("c2", "shell", map[string]any{"command": "rm -f keep.txt"}),
		say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n"))
	d.Start()
	done := d.WaitRun(d.InjectRun("src", map[string]any{"id": "3"}), "succeeded")
	run := d.AgentRuns(d.Node(done, "work").ID)[0]
	tr := d.Transcript(run.ID)
	if !strings.Contains(tr, "mcp[c1] is_error=false") || !strings.Contains(tr, "mcp[c2] is_error=true") || !strings.Contains(tr, "denied by policy") {
		t.Errorf("transcript:\n%s", tr)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Error("the denied rm ran")
	}
	var denied int
	for _, p := range d.Permissions(run.ID) {
		if p.Title == "hal tool: shell" && p.Decision == "reject" && p.DecidedBy == "policy" {
			denied++
		}
	}
	if denied != 1 {
		t.Errorf("denied-by-policy records = %d, want 1: %+v", denied, d.Permissions(run.ID))
	}
}

// The propose and execute phases of a proposal-style approve node share one per_run workspace, which is removed
// only when the node run has finished successfully.
func TestSandboxWorkspaceSurvivesProposalPhases(t *testing.T) {
	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: per_run\napproval: auto\nkeep_alive: 0s\n")
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", script{LoadSession: true, Turns: []turn{
		{When: inMode("default"), Steps: []step{
			mcp("p1", "fs_write", map[string]any{"path": "plan.txt", "expected_hash": "", "content": "from propose"}),
			say(proposalPlan),
		}},
		{When: inMode("bypass"), Steps: []step{
			mcp("e1", "shell", map[string]any{"command": "cat plan.txt"}),
			say("executed"),
		}},
	}})
	d.Flow("f", approvalFlow("proposal", "    sandbox: local\n"))
	d.Start()

	runID := d.InjectRun("src", map[string]any{"id": "4"})
	parked := d.WaitRun(runID, "awaiting_approval")
	if matches, _ := filepath.Glob(filepath.Join(root, "runs", "*", "plan.txt")); len(matches) != 1 {
		t.Fatalf("the proposal phase's workspace was not kept: %v (%s)", matches, d.Transcript(d.AgentRuns(d.Node(parked, "work").ID)[0].ID))
	}
	d.Decide(d.WaitApproval("proposal").ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	var seen bool
	for _, r := range d.AgentRuns(d.Node(done, "work").ID) {
		seen = seen || strings.Contains(d.Transcript(r.ID), "stdout:\\nfrom propose")
	}
	if !seen {
		t.Error("the execute phase did not see the file written in the propose phase")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "runs")); len(entries) != 0 {
		t.Errorf("workspace kept after the node run succeeded: %v", entries)
	}
}

// The local sandbox needs no config file: it is always on, rooted in <home>/data/sandboxes/local, and
// fs_write inside the workdir runs without approval.
func TestLocalSandboxWorksWithNoConfig(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		mcp("c1", "fs_write", map[string]any{"path": "notes/ok.txt", "expected_hash": "", "content": "hello"}),
		say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n"))
	d.Start()
	if _, err := os.Stat(filepath.Join(d.Home, "sandboxes", "local.yaml")); err == nil {
		t.Fatal("test setup wrote a sandbox config")
	}
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	tr := d.Transcript(d.AgentRuns(d.Node(d.WaitRun(runID, "succeeded"), "work").ID)[0].ID)
	if n := len(d.Approvals("")); n != 0 {
		t.Errorf("fs_write asked for approval: %d", n)
	}
	if !strings.Contains(tr, "mcp[c1] is_error=false created notes/ok.txt: 5 bytes") {
		t.Errorf("fs_write did not succeed:\n%s", tr)
	}
	if _, err := os.Stat(filepath.Join(d.Home, "data", "sandboxes", "local")); err != nil {
		t.Errorf("default root was not created: %v", err)
	}
}

// After a sandboxed run ends its sandbox stays for keep_alive (default 1m) and is then torn down; the log says both.
func TestSandboxStaysForKeepAliveAfterTheRun(t *testing.T) {
	d := newDaemon(t, withClock())
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: per_run\napproval: auto\n")
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		mcp("c1", "fs_write", map[string]any{"path": "report.md", "expected_hash": "", "content": "kept"}),
		say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n"))
	d.Start()

	done := d.WaitRun(d.InjectRun("src", map[string]any{"id": "5"}), "succeeded")
	run := d.AgentRuns(d.Node(done, "work").ID)[0]
	report := func() []string { m, _ := filepath.Glob(filepath.Join(root, "runs", "*", "report.md")); return m }
	if len(report()) != 1 {
		t.Fatalf("the workspace was removed when the run ended: %v", report())
	}
	var log sandboxLog
	d.Get("/api/agent-runs/" + run.ID + "/sandbox-log").Want(200).JSON(&log)
	if len(log.Sessions) != 1 || log.Sessions[0].Status != "ready" || log.find("lifecycle", "Run ended; sandbox kept for 1m") == nil || log.find("lifecycle", "Teardown") != nil {
		t.Fatalf("log right after the run: %+v %+v", log.Sessions, log.Entries)
	}

	d.Clock.Advance(59 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if len(report()) != 1 {
		t.Fatal("the sandbox was removed before keep_alive passed")
	}
	d.Clock.Advance(2 * time.Second)
	d.Eventually("the sandbox to be removed after keep_alive", func() bool { return len(report()) == 0 })
	d.Eventually("the teardown to be logged", func() bool {
		var l sandboxLog
		d.Get("/api/agent-runs/" + run.ID + "/sandbox-log").Want(200).JSON(&l)
		return len(l.Sessions) == 1 && l.Sessions[0].Status == "closed" && l.find("lifecycle", "Teardown: run succeeded") != nil
	})
}

// seedSandboxFile puts a file in the workdir of a local sandbox before the agent runs.
func seedSandboxFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hashlineReplace is the edits of an fs_write call that replaces the line with the anchor.
func hashlineReplace(anchor, content string) []any {
	return []any{map[string]any{"op": hashline.OpReplace, "start": anchor, "content": content}}
}

func hashlineView(t *testing.T, content string) hashline.View {
	t.Helper()
	v, err := hashline.Snapshot(content, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The agent reads a file as anchored lines, changes two of them by anchor and gets the new revision back. The same call
// again names a revision the file no longer has and writes nothing, and a create-only call does not overwrite the file.
func TestSandboxHashlineAgentEditsAFileBySurgicalEdits(t *testing.T) {
	const original = "alpha\nbeta\ngamma\n"
	const edited = "alpha\nBETA\ngamma\nomega\n"
	view, after := hashlineView(t, original), hashlineView(t, edited)
	edit := map[string]any{"path": "notes.txt", "expected_hash": view.Hash, "edits": []any{
		map[string]any{"op": hashline.OpReplace, "start": view.Lines[1].Anchor, "content": "BETA"},
		map[string]any{"op": hashline.OpInsertAfter, "start": view.Lines[2].Anchor, "content": "omega"},
	}}
	d, root := sandboxDaemon(t, "auto", "persistent", []step{
		mcp("c1", "fs_read", map[string]any{"path": "notes.txt"}),
		mcp("c2", "fs_write", edit),
		mcp("c3", "fs_write", edit),
		mcp("c4", "fs_write", map[string]any{"path": "notes.txt", "expected_hash": "", "content": "overwritten"}),
	})
	notes := filepath.Join(root, "notes.txt")
	seedSandboxFile(t, notes, original)

	done := d.WaitRun(d.InjectRun("src", map[string]any{"id": "hl1"}), "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	conflict := func(expected, current string) string {
		return (&hashline.ConflictError{Expected: expected, Current: current}).Error()
	}
	for _, want := range []string{
		"mcp[c1] is_error=false notes.txt: 3 lines, revision " + view.Hash,
		view.Lines[1].Anchor + "|beta",
		"mcp[c2] is_error=false edited notes.txt: 2 edits",
		"revision: " + after.Hash,
		after.Lines[1].Anchor + "|BETA",
		after.Lines[3].Anchor + "|omega",
		"mcp[c3] is_error=true notes.txt: " + conflict(view.Hash, after.Hash),
		"mcp[c4] is_error=true notes.txt: " + conflict("", after.Hash),
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}
	if got, err := os.ReadFile(notes); err != nil || string(got) != edited {
		t.Errorf("file = %q, %v; want %q", got, err, edited)
	}
	if n := len(d.Approvals("")); n != 0 {
		t.Errorf("auto policy asked for approval: %d", n)
	}
}

// With approval, the Inbox shows the diff of what the edit really does to the file. When the file changes while the
// approval is pending, approving does not write: the agent gets an error and the other change is kept.
func TestSandboxHashlineChangedFileDuringApprovalIsNotWritten(t *testing.T) {
	const original = "alpha\nbeta\ngamma\n"
	view := hashlineView(t, original)
	d, root := sandboxDaemon(t, "always", "persistent", []step{
		mcp("c1", "fs_write", map[string]any{"path": "notes.txt", "expected_hash": view.Hash, "edits": hashlineReplace(view.Lines[1].Anchor, "BETA")}),
	})
	notes := filepath.Join(root, "notes.txt")
	seedSandboxFile(t, notes, original)

	runID := d.InjectRun("src", map[string]any{"id": "hl2"})
	ap := d.WaitApproval("tool_call")
	raw := string(ap.PermissionRequest.RawInput)
	for _, want := range []string{"edit notes.txt (1 edit)", "-beta", "+BETA"} {
		if !strings.Contains(raw, want) {
			t.Errorf("the approval lacks %q: %s", want, raw)
		}
	}
	if got, _ := os.ReadFile(notes); string(got) != original {
		t.Fatalf("the file was written before the approval: %q", got)
	}

	const changed = "alpha\nbeta, changed by someone else\ngamma\n"
	seedSandboxFile(t, notes, changed)
	d.Decide(ap.ID, true, nil)
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	if !strings.Contains(tr, "mcp[c1] is_error=true") || !strings.Contains(tr, "while the approval was pending") {
		t.Errorf("transcript:\n%s", tr)
	}
	if got, _ := os.ReadFile(notes); string(got) != changed {
		t.Errorf("file = %q, want the other change and nothing of the approved edit", got)
	}
}

// A secret value of the sandbox never reaches the agent or the log: the line that holds it is read as redacted and marked,
// an edit of it is refused, and an edit of another line leaves the secret in the file.
func TestSandboxHashlineSecretValuesStayHiddenFromTheAgentAndTheLog(t *testing.T) {
	const secret = "topsecretvalue"
	const original = "name = demo\ntoken = " + secret + "\nmode = a\n"
	const edited = "name = demo\ntoken = " + secret + "\nmode = b\n"
	view, after := hashlineView(t, original), hashlineView(t, edited)

	d := newDaemon(t)
	root := filepath.Join(t.TempDir(), "sbx")
	d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+root+"\nlifecycle: persistent\napproval: auto\nkeep_alive: 0s\n")
	d.Write("env.d/sandboxes/local.env", "APP_TOKEN="+secret+"\n")
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		mcp("c1", "fs_read", map[string]any{"path": "config.ini"}),
		mcp("c2", "fs_write", map[string]any{"path": "config.ini", "expected_hash": view.Hash, "edits": hashlineReplace(view.Lines[2].Anchor, "mode = b")}),
		mcp("c3", "fs_write", map[string]any{"path": "config.ini", "expected_hash": after.Hash, "edits": hashlineReplace(after.Lines[1].Anchor, "token = guessed")}),
		mcp("c4", "fs_write", map[string]any{"path": "config.ini", "expected_hash": after.Hash, "content": "token = x\n"}),
		mcp("c5", "fs_search", map[string]any{"pattern": "token"}),
		say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", "    sandbox: local\n"))
	d.Start()
	config := filepath.Join(root, "config.ini")
	seedSandboxFile(t, config, original)

	done := d.WaitRun(d.InjectRun("src", map[string]any{"id": "hl3"}), "succeeded")
	run := d.AgentRuns(d.Node(done, "work").ID)[0]
	tr := d.Transcript(run.ID)
	if strings.Contains(tr, secret) {
		t.Errorf("the transcript shows the secret value:\n%s", tr)
	}
	for _, want := range []string{
		"mcp[c1] is_error=false config.ini: 3 lines, revision " + view.Hash,
		view.Lines[1].Anchor + "|token = «redacted»",
		"[secret: read-only]",
		"mcp[c2] is_error=false edited config.ini: 1 edit",
		"mcp[c3] is_error=true",
		"holds a secret value",
		"mcp[c4] is_error=true",
		"mcp[c5] is_error=false config.ini (revision " + after.Hash + ")",
		after.Lines[1].Anchor + "|token = «redacted»",
	} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}
	if got, err := os.ReadFile(config); err != nil || string(got) != edited {
		t.Errorf("file = %q, %v; want the secret kept and only the mode line changed: %q", got, err, edited)
	}

	var log sandboxLog
	d.Get("/api/agent-runs/" + run.ID + "/sandbox-log").Want(200).JSON(&log)
	if w := log.find("fs", "write config.ini ("); w == nil || w.Tool != "fs_write" || w.Level != "info" || !strings.Contains(w.Title, "1 edit") {
		t.Errorf("edit entry = %+v", w)
	}
	if r := log.find("fs", "config.ini (not written)"); r == nil || r.Level != "warn" || r.Error == "" {
		t.Errorf("refused edit entry = %+v", r)
	}
	if raw := d.Get("/api/agent-runs/" + run.ID + "/sandbox-log/raw").Want(200); strings.Contains(string(raw.Body), secret) {
		t.Error("the sandbox log shows the secret value")
	}
}
