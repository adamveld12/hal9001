package sandbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"hal9001/internal/agent/agenttest"
)

// closeCounter is a Session that only records how it was closed.
type closeCounter struct {
	Session
	mu      sync.Mutex
	closes  int
	success bool
}

func (c *closeCounter) Close(_ context.Context, success bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	c.success = success
	return nil
}

func (c *closeCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

// eventually waits for cond: the Keeper closes sessions from its own goroutine after the clock fires.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestKeeperClosesAfterTTL(t *testing.T) {
	clock := agenttest.NewManualClock()
	k := NewKeeper(KeeperOptions{Clock: clock})
	s := &closeCounter{}
	k.Hold("run1", s, time.Minute, false)

	clock.Advance(59 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if s.count() != 0 || !k.Holding("run1") {
		t.Fatalf("closed before the ttl: closes=%d", s.count())
	}
	clock.Advance(2 * time.Second)
	eventually(t, "the session to close", func() bool { return s.count() == 1 })
	if s.success {
		t.Error("the run's outcome was not passed to Close")
	}
	if k.Holding("run1") {
		t.Error("an expired session is still held")
	}
}

func TestKeeperZeroTTLClosesNow(t *testing.T) {
	k := NewKeeper(KeeperOptions{Clock: agenttest.NewManualClock()})
	s := &closeCounter{}
	k.Hold("run1", s, 0, true)
	if s.count() != 1 || !s.success || k.Holding("run1") {
		t.Fatalf("closes=%d held=%v", s.count(), k.Holding("run1"))
	}
}

func TestKeeperTakeStopsTheTimer(t *testing.T) {
	clock := agenttest.NewManualClock()
	k := NewKeeper(KeeperOptions{Clock: clock})
	s := &closeCounter{}
	k.Hold("run1", s, time.Minute, true)
	got, ok := k.Take("run1")
	if !ok || got != Session(s) {
		t.Fatalf("Take = %v, %v", got, ok)
	}
	if _, ok := k.Take("run1"); ok {
		t.Error("a session was taken twice")
	}
	clock.Advance(time.Hour)
	time.Sleep(20 * time.Millisecond)
	if s.count() != 0 {
		t.Error("a taken session was closed by its old timer")
	}
}

func TestKeeperHoldReplacesAndClosesTheOlderSession(t *testing.T) {
	clock := agenttest.NewManualClock()
	k := NewKeeper(KeeperOptions{Clock: clock})
	old, fresh := &closeCounter{}, &closeCounter{}
	k.Hold("run1", old, time.Minute, true)
	k.Hold("run1", fresh, time.Minute, true)
	if old.count() != 1 || fresh.count() != 0 {
		t.Fatalf("old closes=%d fresh closes=%d", old.count(), fresh.count())
	}
	clock.Advance(2 * time.Minute)
	eventually(t, "the new session to close", func() bool { return fresh.count() == 1 })
	if old.count() != 1 {
		t.Errorf("the replaced session was closed %d times", old.count())
	}
}

func TestKeeperCloseAll(t *testing.T) {
	clock := agenttest.NewManualClock()
	k := NewKeeper(KeeperOptions{Clock: clock})
	a, b := &closeCounter{}, &closeCounter{}
	k.Hold("a", a, time.Hour, true)
	k.Hold("b", b, time.Hour, false)
	k.CloseAll()
	if a.count() != 1 || b.count() != 1 {
		t.Fatalf("closes a=%d b=%d", a.count(), b.count())
	}
	late := &closeCounter{}
	k.Hold("c", late, time.Hour, true)
	if late.count() != 1 {
		t.Error("a Hold after CloseAll must close at once")
	}
	clock.Advance(2 * time.Hour)
	time.Sleep(20 * time.Millisecond)
	if a.count() != 1 || b.count() != 1 {
		t.Error("a session was closed twice")
	}
}
