package store

import (
	"encoding/json"
	"testing"
	"time"
)

// A sandbox log follows its node run: pruning the flow run deletes it, and a restart marks open sandboxes interrupted.
func TestSandboxLogRetentionAndRestart(t *testing.T) {
	s, clk := newTestStore(t)
	old := seedRun(t, s, "old", FlowRunSucceeded)
	live := seedRun(t, s, "live", FlowRunRunning)

	mk := func(r seededRun, status string) *SandboxSession {
		ss := &SandboxSession{NodeRunID: r.node.ID, AgentRunID: r.agent.ID, Backend: "local", Status: status, EnvNames: []string{"A"}}
		must(t, s.CreateSandboxSession(ctx(), ss))
		for range 3 {
			data, _ := json.Marshal(SandboxLogData{Stdout: "out"})
			must(t, s.AppendSandboxLogEntry(ctx(), &SandboxLogEntry{SessionID: ss.ID, Kind: SandboxKindExec, Title: "$ x", Data: data}))
		}
		return ss
	}
	oldSS := mk(old, SandboxClosed)
	liveSS := mk(live, SandboxReady)

	// Entries come back in order, counted on the session, paged forwards and as a tail.
	got, more, err := s.ListSandboxLogEntries(ctx(), SandboxEntryFilter{SandboxFilter: SandboxFilter{NodeRunID: live.node.ID}, Limit: 2})
	must(t, err)
	if len(got) != 2 || !more || got[0].ID >= got[1].ID {
		t.Fatalf("first page = %+v more=%v", got, more)
	}
	tail, more, err := s.ListSandboxLogEntries(ctx(), SandboxEntryFilter{SandboxFilter: SandboxFilter{AgentRunID: live.agent.ID}, Limit: 2, Tail: true})
	must(t, err)
	if len(tail) != 2 || !more || tail[0].ID >= tail[1].ID || tail[1].ID != got[1].ID+1 {
		t.Fatalf("tail = %+v more=%v", tail, more)
	}
	if ss, err := s.GetSandboxSession(ctx(), liveSS.ID); err != nil || ss.EntryCount != 3 {
		t.Fatalf("session = %+v %v", ss, err)
	}

	res, err := s.MarkInterrupted(ctx())
	must(t, err)
	if res.SandboxSessions != 1 {
		t.Fatalf("interrupted sandbox sessions = %d, want 1 (the ready one)", res.SandboxSessions)
	}
	if ss, _ := s.GetSandboxSession(ctx(), liveSS.ID); ss.Status != SandboxInterrupted || ss.EndedAt == nil || ss.Error == "" {
		t.Errorf("live session after restart = %+v", ss)
	}
	if ss, _ := s.GetSandboxSession(ctx(), oldSS.ID); ss.Status != SandboxClosed {
		t.Errorf("closed session changed: %+v", ss)
	}

	clk.Advance(40 * 24 * time.Hour)
	if _, err := s.Prune(ctx(), clk.Now(), 30, 180); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSandboxSession(ctx(), oldSS.ID); err == nil {
		t.Error("the pruned run's sandbox session was kept")
	}
	if es, _, _ := s.ListSandboxLogEntries(ctx(), SandboxEntryFilter{SandboxFilter: SandboxFilter{NodeRunID: old.node.ID}}); len(es) != 0 {
		t.Errorf("the pruned run's log entries were kept: %d", len(es))
	}
}
