package agent

import (
	"testing"
	"time"
)

// The gate counts only session/update notifications (not requests, responses or other notifications) and drain
// waits for the client to catch up with what was read from the pipe.
func TestUpdateGateWaitsForReadNotificationsToBeHandled(t *testing.T) {
	g := newUpdateGate()
	for _, line := range []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"text":"mentions session/update"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/update","params":{}}`, // a request, not a notification
		`{"jsonrpc":"2.0","method":"$/cancel_request","params":{}}`,
		`not json with "session/update" in it`,
	} {
		g.observe([]byte(line))
	}
	if got := g.received.Load(); got != 2 {
		t.Fatalf("received = %d, want 2", got)
	}

	released := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		g.drain(5 * time.Second)
		released <- time.Since(start)
	}()
	g.done()
	select {
	case <-released:
		t.Fatal("drain returned with one update still unhandled")
	case <-time.After(50 * time.Millisecond):
	}
	g.done()
	select {
	case d := <-released:
		if d > 2*time.Second {
			t.Errorf("drain took %s after the last update was handled", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return once every update was handled")
	}
}

// A notification the SDK never delivers (it could not parse it) must not hold the run forever.
func TestUpdateGateDrainGivesUp(t *testing.T) {
	g := newUpdateGate()
	g.observe([]byte(`{"jsonrpc":"2.0","method":"session/update"}`))
	start := time.Now()
	g.drain(100 * time.Millisecond)
	if d := time.Since(start); d < 90*time.Millisecond || d > 2*time.Second {
		t.Errorf("drain gave up after %s, want about the limit", d)
	}
}
