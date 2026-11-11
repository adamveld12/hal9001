package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Lifecycle of continued chats around the daemon: the window while the user types, forks of one run, shutdown with held
// sandboxes and open forks, secrets in the new messages.

// agentProcesses returns the processes started from the daemon's home (the fake agents carry their script path).
func (d *Daemon) agentProcesses() []string {
	d.t.Helper()
	out, err := exec.CommandContext(d.t.Context(), "pgrep", "-fl", d.Path("fake")).Output()
	if err != nil && len(out) == 0 {
		return nil // pgrep exits 1 when nothing matches
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

func (e *ucEnv) workspaceDirs() []string {
	entries, _ := os.ReadDir(filepath.Join(e.root, "runs"))
	var out []string
	for _, en := range entries {
		out = append(out, en.Name())
	}
	return out
}

// Forking takes the sandbox at once, so the window of the original run cannot lapse while the user is still typing the
// first message; the timer starts when the chat replies.
func TestUnifiedChatForkHoldsTheSandboxWhileTheUserTypes(t *testing.T) {
	e := newUC(t, ucOpts{load: true, clock: true})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	d.Clock.Advance(10 * time.Minute) // far past keep_alive (1m) of the run
	time.Sleep(100 * time.Millisecond)
	if len(e.workspaces("report.txt")) != 1 {
		t.Fatal("the sandbox was removed while the fork waited for its first message")
	}
	_, tr := e.ucTurn(fork.ID, "read the file")
	wantContains(t, "first turn", tr, "mcp[c1] is_error=false "+ucReportText)
	if n := ucNotices(d.conversationMessages(fork.ID)); len(n) != 0 {
		t.Errorf("notices: %q", n)
	}
	if got := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log"); len(got.Sessions) != 1 || got.find("lifecycle", "Teardown") != nil {
		t.Errorf("conversation log: %+v %+v", got.Sessions, got.Entries)
	}
}

// A second fork of the same run cannot reattach the sandbox the first one holds: it gets its own and a notice, and the
// first chat keeps its files.
func TestUnifiedChatSecondForkGetsItsOwnSandbox(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	first := d.fork(orig.ID)
	second := d.fork(orig.ID)
	if ids := d.conversationIDs("?forked_from=" + orig.ID); len(ids) != 2 {
		t.Fatalf("forks = %v", ids)
	}
	if n := ucNotices(d.conversationMessages(first.ID)); len(n) != 0 {
		t.Errorf("the first fork got notices: %q", n)
	}
	n := ucNotices(d.conversationMessages(second.ID))
	if len(n) != 1 {
		t.Fatalf("the second fork's notices = %q, want one", n)
	}
	wantContains(t, "notice", n[0], "The sandbox of run "+orig.ID+" is no longer available (keep_alive is 1m)", "a new one was created", "Changes made by the earlier run are gone")
	e.ucTurn(second.ID, "write notes")
	e.ucTurn(first.ID, "read notes")
	if got := e.workspaceDirs(); len(got) != 2 {
		t.Errorf("workspaces = %v, want one per chat", got)
	}
	_, tr := e.ucTurn(first.ID, "read the file")
	wantContains(t, "first fork", tr, "mcp[c1] is_error=false "+ucReportText)
	_, tr = e.ucTurn(second.ID, "read notes")
	wantContains(t, "second fork", tr, "mcp[c1] is_error=false chat notes", "mcp[c2] is_error=true")
}

// Stopping the daemon removes every held sandbox (of runs and of chats) and every agent process; the forks are still
// there on the next start and recreate their sandbox with a notice.
func TestUnifiedChatDaemonShutdownWithHeldSandboxes(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	second := d.RunOf("src", map[string]any{"id": "8"})
	secondRun := d.AgentRuns(d.Node(second, "work").ID)[0]
	fork := d.fork(orig.ID) // takes over the first run's sandbox
	e.ucTurn(fork.ID, "write notes")
	e.d.Eventually("both sandboxes to be held", func() bool { return len(e.workspaceDirs()) == 2 })
	if len(d.agentProcesses()) == 0 {
		t.Fatal("the chat's agent process is not running before the shutdown")
	}

	d.Stop()
	if left := e.workspaceDirs(); len(left) != 0 {
		t.Errorf("workspaces left after the shutdown: %v", left)
	}
	d.Eventually("the agent processes to exit", func() bool { return len(d.agentProcesses()) == 0 })

	d.Start()
	if log := d.sandboxLogAt("/api/agent-runs/" + secondRun.ID + "/sandbox-log"); len(log.Sessions) != 1 || log.Sessions[0].Status != "closed" || log.find("lifecycle", "Teardown: run succeeded") == nil {
		t.Errorf("log of the run whose sandbox was held at the shutdown: %+v %+v", log.Sessions, log.Entries)
	}
	log := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
	if len(log.Sessions) != 1 || log.Sessions[0].Status != "closed" {
		t.Errorf("log of the chat after the shutdown: %+v", log.Sessions)
	}
	if c := d.conversation(fork.ID); c.Status != "idle" || c.ForkedFrom == nil || c.ForkedFrom.AgentRunID != orig.ID {
		t.Fatalf("fork after the restart: %+v", c)
	}
	_, tr := e.ucTurn(fork.ID, "read notes")
	wantContains(t, "turn after the restart", tr, "mcp[c1] is_error=true", "mcp[c2] is_error=true", "Tried both.")
	if n := ucNotices(d.conversationMessages(fork.ID)); len(n) != 1 || !strings.Contains(n[0], "The sandbox of run "+orig.ID+" is no longer available") {
		t.Errorf("notices after the restart: %q", n)
	}
	log = d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
	if len(log.Sessions) != 2 {
		t.Errorf("sessions of the chat after the restart: %+v", log.Sessions)
	}
	// The chat's own ACP session is loaded again, not the run's.
	var loads []string
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" {
			loads = append(loads, c["session_id"].(string))
		}
	}
	if len(loads) == 0 {
		t.Error("no session/load after the restart")
	}
}

// A daemon stopped while an emit waits for approval: the approval expires, nothing starts, and the chat can be used
// after the restart.
func TestUnifiedChatRestartWithPendingEmitApproval(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	d.assistSend(fork.ID, "emit reviewed", nil)
	ap := d.WaitApproval("tool_call")
	d.assistWait(fork.ID, "awaiting_approval")

	d.Restart()
	var got Approval
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&got)
	if got.Status != "expired" {
		t.Errorf("approval after the restart: %+v", got)
	}
	if n := len(d.continuationRuns(orig.FlowRunID)); n != 0 {
		t.Errorf("a continuation run started: %d", n)
	}
	if c := d.conversation(fork.ID); c.Running || c.Status == "running" || c.Status == "awaiting_approval" {
		t.Errorf("the chat is stuck after the restart: %+v", c)
	}
	_, tr := e.ucTurn(fork.ID, "read the file") // the shutdown removed the run's files: the chat's sandbox is new
	wantContains(t, "turn after the restart", tr, "mcp[c1] is_error=true", "mcp[c2] is_error=false", "mcp[c4] is_error=false", "Read it.")
	if n := ucNotices(d.conversationMessages(fork.ID)); len(n) != 1 {
		t.Errorf("notices after the restart: %q", n)
	}
	// And the emit works after it: the tool is still offered and the run starts on approval.
	d.assistSend(fork.ID, "emit reviewed", nil)
	ap2 := d.WaitApproval("tool_call")
	d.Decide(ap2.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	d.Eventually("the continuation run", func() bool { return len(d.continuationRuns(orig.FlowRunID)) == 1 })
}

// A cancelled turn withdraws the pending emit approval; nothing starts.
func TestUnifiedChatCancelWithdrawsTheEmitApproval(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	d.assistSend(fork.ID, "emit reviewed", nil)
	ap := d.WaitApproval("tool_call")
	d.assistWait(fork.ID, "awaiting_approval")
	d.Post("/api/assist/sessions/"+fork.ID+"/cancel", nil).Want(200)
	d.assistWait(fork.ID, "idle", "failed")
	var got Approval
	d.Eventually("the approval of the cancelled turn to be withdrawn", func() bool {
		d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&got)
		return got.Status != "pending"
	})
	if got.Status == "approved" {
		t.Errorf("approval after the cancel: %+v", got)
	}
	if n := len(d.pendingApprovals()); n != 0 {
		t.Errorf("pending approvals after the cancel: %d", n)
	}
	if n := len(d.continuationRuns(orig.FlowRunID)); n != 0 {
		t.Errorf("a continuation run started: %d", n)
	}
	if r := d.Post("/api/approvals/"+ap.ID+"/approve", nil); r.Status != 409 {
		t.Errorf("approving after the cancel: %s", r)
	}
	if n := len(d.continuationRuns(orig.FlowRunID)); n != 0 {
		t.Errorf("a late approval started a continuation run: %d", n)
	}
}

// A secret of the sandbox's environment never reaches the chat's messages, the conversation's sandbox log or the agent
// events, even when a command prints it.
func TestUnifiedChatRedactsSecrets(t *testing.T) {
	const secret = "chat-secret-value-9191"
	e := newUCWith(t, ucOpts{load: true}, func(d *Daemon) { d.Write("env.d/sandboxes/local.env", "CHAT_SECRET="+secret+"\n") })
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	_, tr := e.ucTurn(fork.ID, "print secret")
	if !strings.Contains(tr, "mcp[c1] is_error=false") {
		t.Fatalf("the command did not run:\n%s", tr)
	}
	for _, p := range []string{
		"/api/conversations/" + fork.ID + "/messages", "/api/conversations/" + fork.ID, "/api/conversations/" + fork.ID + "/sandbox-log",
		"/api/assist/sessions/" + fork.ID, "/api/agent-runs?assist_session_id=" + fork.ID, "/api/approvals", "/api/conversations/" + orig.ID + "/messages",
	} {
		if body := d.Get(p).Want(200).Body; strings.Contains(string(body), secret) {
			t.Errorf("%s contains the secret", p)
		}
	}
	runs := d.assistRuns(fork.ID)
	for _, ev := range d.AgentEvents(runs[len(runs)-1].ID) {
		if strings.Contains(string(ev.Payload), secret) {
			t.Errorf("an agent event contains the secret: %s", ev.Payload)
		}
	}
	if !strings.Contains(string(d.Get("/api/conversations/"+fork.ID+"/sandbox-log").Body), "«redacted»") {
		t.Error("the sandbox log shows no redaction marker: the command's output was not checked")
	}
}

// The divider update and the notices of a continued chat reach the stream as assist.message events, so an open chat
// shows them without a reload.
func TestUnifiedChatStreamsDividerAndNotices(t *testing.T) {
	e := newUC(t, ucOpts{load: true, keepAlive: "0s"})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	s := d.openAssistStream(fork.ID, "assist.message,assist.updated")
	e.ucTurn(fork.ID, "write notes")
	e.ucTurn(fork.ID, "read notes") // keep_alive 0: the sandbox of the first reply is gone, so this turn posts a notice
	has := func(kind, textPart string) bool {
		for _, ev := range s.of("assist.message") {
			if ev.Data["kind"] == kind && strings.Contains(fmt.Sprint(ev.Data["text"]), textPart) {
				return true
			}
		}
		return false
	}
	d.Eventually("the divider update and the notice on the stream", func() bool {
		return has("divider", "Continued from run "+orig.ID+"; session resumed") && has("notice", "keep_alive is 0")
	})
	if len(s.of("assist.updated")) == 0 {
		t.Error("no assist.updated event")
	}
}

// With a persistent local sandbox the files stay when the sandbox is recreated, and the notice does not say they are gone.
func TestUnifiedChatPersistentSandboxKeepsFiles(t *testing.T) {
	e := newUC(t, ucOpts{load: true, keepAlive: "0s", persistent: true})
	d, orig := e.d, e.orig
	if _, err := os.Stat(filepath.Join(e.root, "report.txt")); err != nil {
		t.Fatalf("the run's file is not in the persistent workdir: %v", err)
	}
	fork := d.fork(orig.ID)
	n := ucNotices(d.conversationMessages(fork.ID))
	if len(n) != 1 || !strings.Contains(n[0], "keep_alive is 0") || strings.Contains(n[0], "are gone") {
		t.Errorf("notices = %q", n)
	}
	_, tr := e.ucTurn(fork.ID, "read the file")
	wantContains(t, "turn", tr, "mcp[c1] is_error=false "+ucReportText)
}

// Suspending the idle agent process does not end the chat's sandbox: the next message resumes the agent session in the
// run's working directory and finds the files, without a notice.
func TestUnifiedChatSuspendedAgentKeepsTheSandbox(t *testing.T) {
	e := newUCWith(t, ucOpts{load: true}, func(d *Daemon) { d.AssistIdle = 300 * time.Millisecond })
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	e.ucTurn(fork.ID, "write notes")
	d.Eventually("the idle agent process to be suspended", func() bool { return len(d.agentProcesses()) == 0 })
	_, tr := e.ucTurn(fork.ID, "read notes")
	wantContains(t, "turn after the suspension", tr, "mcp[c1] is_error=false chat notes", "mcp[c2] is_error=false "+ucReportText)
	if n := ucNotices(d.conversationMessages(fork.ID)); len(n) != 0 {
		t.Errorf("notices after a suspension: %q", n)
	}
	if got := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log"); len(got.Sessions) != 1 {
		t.Errorf("sessions of the chat: %+v", got.Sessions)
	}
	var loads int
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" {
			loads++
			if c["cwd"] != orig.Cwd {
				t.Errorf("session/load in %v, want the run's directory %s", c["cwd"], orig.Cwd)
			}
		}
	}
	if loads < 2 {
		t.Errorf("session/load calls = %d, want the first turn's and the resume's", loads)
	}
}
