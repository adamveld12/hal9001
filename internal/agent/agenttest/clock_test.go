package agenttest

import (
	"testing"
	"time"
)

// Tick must not advance past a scheduler that has not armed its next timer yet:
// the tick would fire nothing and be lost.
func TestTickNeedsATimerDueWithinTheTick(t *testing.T) {
	c := NewManualClock()
	c.NewTimer(time.Hour) // a long timer is pending, but the tick would not fire it
	if c.Tick(time.Second, 50*time.Millisecond) {
		t.Fatal("Tick reported success with no timer due within the tick")
	}
	if got := c.Now().Sub(NewManualClock().Now()); got != 0 {
		t.Errorf("the clock advanced by %s although no timer was due", got)
	}
}

func TestTickWaitsForTheFiredTimerToRearm(t *testing.T) {
	c := NewManualClock()
	timer := c.NewTimer(time.Second)
	handled := make(chan struct{})
	go func() { // a scheduler: handle the tick, then arm the next timer
		<-timer.C()
		close(handled)
		c.NewTimer(time.Second)
	}()
	if !c.Tick(time.Second, 10*time.Second) {
		t.Fatal("Tick did not report the re-armed timer")
	}
	select {
	case <-handled:
	default:
		t.Error("Tick returned before the scheduler handled the tick")
	}
	if n := c.Created(); n != 2 {
		t.Errorf("timers created = %d, want 2", n)
	}
}
