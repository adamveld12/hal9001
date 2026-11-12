package assist

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"hal9001/internal/agent/fakeagent"
)

type turnLog struct {
	mu     sync.Mutex
	events []string
}

func (l *turnLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf(format, args...))
}

func (l *turnLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func (l *turnLog) TurnWaiting(session, approval string) { l.add("waiting %s %s", session, approval) }
func (l *turnLog) TurnResumed(session string)           { l.add("resumed %s", session) }
func (l *turnLog) TurnEnded(session string, failed bool, errText, result string) {
	l.add("ended %s failed=%v err=%q result=%q", session, failed, errText, result)
}

// The turn observer hears when a turn of a conversation waits for an approval, runs on, and ends with the final message.
func TestTurnObserverSeesApprovalsAndTheEndOfATurn(t *testing.T) {
	ctx := context.Background()
	t.Run("a finished turn reports its final message", func(t *testing.T) {
		e := newForkEnv(t, say("Hello from the chat."), nil)
		var log turnLog
		e.m.SetTurnObserver(&log)
		sess, err := e.m.Fork(ctx, e.run.ID)
		must(t, err)
		_, err = e.m.SendMessage(ctx, sess.ID, "hi", nil, nil)
		must(t, err)
		e.wait(sess.ID)
		want := []string{fmt.Sprintf("ended %s failed=false err=%q result=%q", sess.ID, "", "Hello from the chat.")}
		if got := log.get(); !slices.Equal(got, want) {
			t.Errorf("events = %q, want %q", got, want)
		}
	})

	t.Run("an approval pauses and resumes the turn, a cancelled turn ends with its last message", func(t *testing.T) {
		e := newForkEnv(t, fakeagent.Script{LoadSession: true, Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{
			{Op: fakeagent.OpMessage, Text: "Working on it."}, {Op: fakeagent.OpHang},
		}}}}, nil)
		var log turnLog
		e.m.SetTurnObserver(&log)
		sess, err := e.m.Fork(ctx, e.run.ID)
		must(t, err)
		_, err = e.m.SendMessage(ctx, sess.ID, "go", nil, nil)
		must(t, err)

		var runID string
		deadline := time.Now().Add(30 * time.Second)
		for runID == "" {
			e.m.mu.Lock()
			for id := range e.m.byRun {
				runID = id
			}
			e.m.mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("the turn never started")
			}
			time.Sleep(5 * time.Millisecond)
		}
		e.m.ToolApprovalPending(runID, "call-1", "approval-1")
		e.m.ToolApprovalResolved(runID, "approval-1", true)
		_, err = e.m.Cancel(ctx, sess.ID)
		must(t, err)
		e.wait(sess.ID)

		got := log.get()
		if len(got) != 3 || got[0] != fmt.Sprintf("waiting %s approval-1", sess.ID) || got[1] != "resumed "+sess.ID || got[2][:len("ended "+sess.ID+" failed=false")] != "ended "+sess.ID+" failed=false" {
			t.Errorf("events = %q", got)
		}
	})
}
