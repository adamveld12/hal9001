package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock safe for concurrent use.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestStore(t *testing.T, opts ...Option) (*Store, *fakeClock) {
	t.Helper()
	clk := newClock()
	all := append([]Option{WithClock(clk.Now)}, opts...)
	s, err := Open(filepath.Join(t.TempDir(), "data", "hal.db"), all...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, clk
}

func ctx() context.Context { return context.Background() }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixtures

func mkEvent(t *testing.T, s *Store, sensor, key, payload string) *Event {
	t.Helper()
	ev := &Event{SensorID: sensor, DedupKey: key, Payload: Inline(payload)}
	ok, err := s.IngestEvent(ctx(), ev)
	must(t, err)
	if !ok {
		t.Fatalf("event %s/%s was not new", sensor, key)
	}
	return ev
}

func mkFlowRun(t *testing.T, s *Store, flowID, eventID string, status FlowRunStatus) *FlowRun {
	t.Helper()
	fr := &FlowRun{FlowID: flowID, FlowHash: "h1", FlowYAML: "id: " + flowID, EventID: eventID, Status: status}
	must(t, s.CreateFlowRun(ctx(), fr))
	return fr
}

func mkNodeRun(t *testing.T, s *Store, flowRunID, nodeID string, typ NodeType, status NodeRunStatus) *NodeRun {
	t.Helper()
	n := &NodeRun{FlowRunID: flowRunID, NodeID: nodeID, NodeType: typ, Status: status}
	must(t, s.CreateNodeRun(ctx(), n))
	return n
}

func mkAgentRun(t *testing.T, s *Store, nodeRunID string, style ApproveStyle, status AgentStatus) *AgentRun {
	t.Helper()
	a := &AgentRun{NodeRunID: nodeRunID, Runtime: "claude", Mode: ModeApprove, ApproveStyle: style, Policy: PolicyGated, Phase: PhasePropose, Status: status}
	must(t, s.CreateAgentRun(ctx(), a))
	return a
}

// valid fills what every approval needs (title, description, origin) when a test leaves it out.
func valid(a *Approval) *Approval {
	if a.Title == "" {
		a.Title = "Approve"
	}
	if a.Description == "" {
		a.Description = "Do the thing"
	}
	if a.Origin == "" {
		a.Origin = OriginFlow
	}
	return a
}
