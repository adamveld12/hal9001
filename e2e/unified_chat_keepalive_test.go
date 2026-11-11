package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// keep_alive in a continued chat: the timer restarts after every reply; once it passes between messages the next message
// recreates the sandbox with a notice, and the conversation's sandbox log gets a second session.

func ucNotices(msgs []convMsgJSON) []string {
	var out []string
	for _, m := range msgs {
		if m.Kind == "notice" {
			out = append(out, m.text())
		}
	}
	return out
}

// waitKept waits until the conversation's log says the reply finished and the sandbox went to the Keeper, so that the
// timer exists before the test moves the clock.
func (e *ucEnv) waitKept(chat string, replies int) {
	e.d.t.Helper()
	e.d.Eventually("the sandbox to be held after the reply", func() bool {
		n := 0
		for _, en := range e.d.sandboxLogAt("/api/conversations/" + chat + "/sandbox-log").Entries {
			if en.Kind == "lifecycle" && strings.Contains(en.Title, "Reply finished; sandbox kept for") {
				n++
			}
		}
		return n >= replies
	})
}

func TestUnifiedChatSandboxExpiresBetweenMessages(t *testing.T) {
	e := newUC(t, ucOpts{load: true, clock: true})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	if n := ucNotices(d.conversationMessages(fork.ID)); len(n) != 0 {
		t.Fatalf("notices after a reattach: %v", n)
	}

	// Every reply restarts the timer: 45s after the first reply and 45s after the second are both inside the window.
	e.ucTurn(fork.ID, "write notes")
	e.waitKept(fork.ID, 1)
	d.Clock.Advance(45 * time.Second)
	_, tr := e.ucTurn(fork.ID, "read notes")
	wantContains(t, "second turn", tr, "mcp[c1] is_error=false chat notes", "mcp[c2] is_error=false "+ucReportText)
	e.waitKept(fork.ID, 2)
	d.Clock.Advance(45 * time.Second)
	time.Sleep(100 * time.Millisecond)
	if len(e.workspaces("notes.txt")) != 1 {
		t.Fatal("the sandbox was removed 45s after the last reply")
	}
	if got := ucNotices(d.conversationMessages(fork.ID)); len(got) != 0 {
		t.Fatalf("notices inside the window: %v", got)
	}

	// Past keep_alive without a message the sandbox is torn down, and the conversation's log says so.
	d.Clock.Advance(20 * time.Second)
	d.Eventually("the idle sandbox to be removed", func() bool { return len(e.workspaces("notes.txt")) == 0 && len(e.workspaces("report.txt")) == 0 })
	d.Eventually("the teardown in the conversation's log", func() bool {
		l := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
		return len(l.Sessions) == 1 && l.Sessions[0].Status == "closed" && l.find("lifecycle", "Teardown") != nil
	})

	// The next message recreates the sandbox: a notice, empty workspace, second session, and the chat goes on.
	_, tr = e.ucTurn(fork.ID, "read notes")
	wantContains(t, "turn after expiry", tr, "mcp[c1] is_error=true", "mcp[c2] is_error=true", "Tried both.")
	notices := ucNotices(d.conversationMessages(fork.ID))
	if len(notices) != 1 {
		t.Fatalf("notices = %q, want one", notices)
	}
	wantContains(t, "notice", notices[0], "The sandbox was removed after 1m without activity", "a new one was created", "Changes made by the earlier run are gone")
	log := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
	if len(log.Sessions) != 2 || log.Sessions[0].ID == log.Sessions[1].ID {
		t.Fatalf("sessions of the conversation: %+v", log.Sessions)
	}
	var open int
	for _, s := range log.Sessions {
		if s.Status == "ready" {
			open++
		}
	}
	if open != 1 || log.find("lifecycle", "Reattached sandbox of run "+orig.ID) == nil || log.find("lifecycle", "The sandbox was removed after 1m") == nil {
		t.Errorf("conversation log: %+v %+v", log.Sessions, log.Entries)
	}
	if got := d.sandboxLogAt("/api/agent-runs/" + orig.ID + "/sandbox-log"); len(got.Sessions) != 1 {
		t.Errorf("the run's log gained a session: %+v", got.Sessions)
	}
	// The new sandbox is a new workspace, keyed by the conversation, and it works.
	if _, err := os.Stat(filepath.Join(e.root, "runs", fork.ID)); err != nil {
		t.Errorf("no workspace of the conversation after recreation: %v", err)
	}
	e.ucTurn(fork.ID, "write notes")
	_, tr = e.ucTurn(fork.ID, "read notes")
	wantContains(t, "turn in the new sandbox", tr, "mcp[c1] is_error=false chat notes", "mcp[c2] is_error=true")
	if got := ucNotices(d.conversationMessages(fork.ID)); len(got) != 1 {
		t.Errorf("a second notice appeared inside the window: %q", got)
	}
}

// With keep_alive 0 the run's sandbox is removed when the run ends: a fork recreates it with a notice at once, and the
// chat's own sandbox is removed after every reply, so each message gets a new one.
func TestUnifiedChatKeepAliveZeroRecreatesTheSandbox(t *testing.T) {
	e := newUC(t, ucOpts{load: false, keepAlive: "0s"})
	d, orig := e.d, e.orig
	if len(e.workspaces("report.txt")) != 0 {
		t.Fatal("keep_alive 0 kept the workspace of the run")
	}
	runLog := d.sandboxLogAt("/api/agent-runs/" + orig.ID + "/sandbox-log")
	if len(runLog.Sessions) != 1 || runLog.Sessions[0].Status != "closed" || runLog.find("lifecycle", "Teardown: run succeeded") == nil {
		t.Fatalf("log of the run: %+v %+v", runLog.Sessions, runLog.Entries)
	}

	fork := d.fork(orig.ID)
	notices := ucNotices(d.conversationMessages(fork.ID))
	if len(notices) != 1 {
		t.Fatalf("notices after the fork = %q, want one", notices)
	}
	wantContains(t, "notice", notices[0], "The sandbox was removed as soon as it was not in use (keep_alive is 0)", "a new one was created", "Changes made by the earlier run are gone")
	if n := firstOfKind(d.conversationMessages(fork.ID), "notice"); n.Role != "system" || n.text() != notices[0] {
		t.Errorf("notice message = %+v", n)
	}
	log := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
	if len(log.Sessions) != 1 || log.Sessions[0].Status != "ready" || log.find("lifecycle", "Reattached") != nil || log.find("lifecycle", "Sandbox ready") == nil {
		t.Errorf("conversation log after the fork: %+v %+v", log.Sessions, log.Entries)
	}
	if got := d.sandboxLogAt("/api/agent-runs/" + orig.ID + "/sandbox-log"); len(got.Sessions) != 1 || len(got.Entries) != len(runLog.Entries) {
		t.Errorf("the run's log changed: %+v", got)
	}

	// The notice is shown once; the file of the run is gone.
	_, tr := e.ucTurn(fork.ID, "read notes")
	wantContains(t, "first turn", tr, "mcp[c1] is_error=true", "mcp[c2] is_error=true")
	if got := ucNotices(d.conversationMessages(fork.ID)); len(got) != 1 {
		t.Errorf("notices after the first turn: %q", got)
	}
	// keep_alive 0 removes the chat's sandbox after the reply, so the next message recreates it (and says so).
	if len(e.workspaces("notes.txt")) != 0 {
		t.Error("the chat's sandbox stayed after the reply")
	}
	e.ucTurn(fork.ID, "write notes")
	_, tr = e.ucTurn(fork.ID, "read notes")
	wantContains(t, "turn after the reply", tr, "mcp[c1] is_error=true")
	if got := ucNotices(d.conversationMessages(fork.ID)); len(got) != 3 {
		t.Errorf("notices after three more turns = %q, want 3 in all", got)
	}
}
