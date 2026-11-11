// Package agenttest holds in-memory implementations of the agent package's
// ports plus a helper that builds the fake ACP agent binary. It is for tests
// of this package and of the engine/API wiring on top of it.
package agenttest

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"hal9001/internal/agent"
)

// BuildFakeAgent compiles cmd/fake-acp-agent into dir and returns the path.
func BuildFakeAgent(dir string) (string, error) { return buildCmd(dir, "fake-acp-agent") }

// BuildHal compiles cmd/hal9001 into dir and returns the path: agents spawn it as their `hal9001 mcp` server.
func BuildHal(dir string) (string, error) { return buildCmd(dir, "hal9001") }

func buildCmd(dir, name string) (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate module root")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	bin := filepath.Join(dir, name)
	cmd := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", bin, "hal9001/cmd/"+name) //nolint:gosec // fixed arguments
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build %s: %w\n%s", name, err, out)
	}
	return bin, nil
}

// Recorder is an in-memory agent.Recorder.
type Recorder struct {
	mu     sync.Mutex
	runs   map[string][]agent.RunRecord
	events map[string][]agent.Event
	perms  map[string]map[string]agent.PermissionRecord
	order  map[string][]string // run -> permission ids in first-seen order
	raw    map[string][]agent.RawFrame
}

var _ agent.Recorder = (*Recorder)(nil)

func NewRecorder() *Recorder {
	return &Recorder{
		runs:   map[string][]agent.RunRecord{},
		events: map[string][]agent.Event{},
		perms:  map[string]map[string]agent.PermissionRecord{},
		order:  map[string][]string{},
		raw:    map[string][]agent.RawFrame{},
	}
}

func (r *Recorder) RunUpdated(rec agent.RunRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[rec.ID] = append(r.runs[rec.ID], rec)
}

func (r *Recorder) Event(e agent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[e.RunID] = append(r.events[e.RunID], e)
}

func (r *Recorder) Permission(p agent.PermissionRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.perms[p.RunID] == nil {
		r.perms[p.RunID] = map[string]agent.PermissionRecord{}
	}
	if _, seen := r.perms[p.RunID][p.ID]; !seen {
		r.order[p.RunID] = append(r.order[p.RunID], p.ID)
	}
	r.perms[p.RunID][p.ID] = p
}

func (r *Recorder) Raw(f agent.RawFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raw[f.RunID] = append(r.raw[f.RunID], f)
}

// RunHistory returns every RunUpdated call for the run, oldest first.
func (r *Recorder) RunHistory(id string) []agent.RunRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.RunRecord(nil), r.runs[id]...)
}

// Events returns the run's events in recorded order.
func (r *Recorder) Events(runID string) []agent.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.Event(nil), r.events[runID]...)
}

// Permissions returns the latest state of each permission request, in the
// order they were first recorded.
func (r *Recorder) Permissions(runID string) []agent.PermissionRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []agent.PermissionRecord
	for _, id := range r.order[runID] {
		out = append(out, r.perms[runID][id])
	}
	return out
}

// RawFrames returns the run's JSON-RPC lines in recorded order.
func (r *Recorder) RawFrames(runID string) []agent.RawFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.RawFrame(nil), r.raw[runID]...)
}

// Broker is an in-memory agent.ApprovalBroker driven by the test.
type Broker struct {
	mu        sync.Mutex
	proposals []agent.ProposalApproval
	nextID    int
}

var _ agent.ApprovalBroker = (*Broker)(nil)

func NewBroker() *Broker { return &Broker{} }

func (b *Broker) SubmitProposal(_ context.Context, p agent.ProposalApproval) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.proposals = append(b.proposals, p)
	b.nextID++
	return fmt.Sprintf("approval-%d", b.nextID), nil
}

// Proposals returns the proposals submitted so far.
func (b *Broker) Proposals() []agent.ProposalApproval {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]agent.ProposalApproval(nil), b.proposals...)
}

// ManualClock is a Clock the test advances by hand.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*manualTimer
	created int
}

type manualTimer struct {
	c  *ManualClock
	at time.Time
	ch chan time.Time
}

var _ agent.Clock = (*ManualClock)(nil)

// NewManualClock starts at a fixed instant.
func NewManualClock() *ManualClock {
	return NewManualClockAt(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
}

// NewManualClockAt starts at start. Use time.Now() when the clock must agree
// with timestamps the store records from the wall clock.
func NewManualClockAt(start time.Time) *ManualClock { return &ManualClock{now: start} }

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After fires when the clock reaches now+d; at once if d <= 0.
func (c *ManualClock) After(d time.Duration) <-chan time.Time { return c.NewTimer(d).C() }

// NewTimer returns a timer that fires when the clock reaches now+d; at once if d <= 0.
func (c *ManualClock) NewTimer(d time.Duration) agent.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &manualTimer{c: c, at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.created++
	if d <= 0 {
		t.ch <- c.now
	} else {
		c.timers = append(c.timers, t)
	}
	return t
}

func (t *manualTimer) C() <-chan time.Time { return t.ch }

func (t *manualTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	for i, o := range t.c.timers {
		if o == t {
			t.c.timers = append(t.c.timers[:i], t.c.timers[i+1:]...)
			return true
		}
	}
	return false
}

// Advance moves time forward and fires every timer that became due. It
// returns how many fired.
func (c *ManualClock) Advance(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	fired := 0
	rest := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
			fired++
		} else {
			rest = append(rest, t)
		}
	}
	c.timers = rest
	return fired
}

// Tick advances the clock by d for a scheduler that re-arms a timer after every
// firing (the engine's sensor loops): it waits until a timer is pending, advances,
// and returns once as many new timers exist as fired, that is, once every fired
// timer's owner handled its tick and armed the next. It reports false if that
// does not happen within timeout. It is wrong for clocks that also time agent
// runs, whose timers do not re-arm.
func (c *ManualClock) Tick(d, timeout time.Duration) bool {
	if !c.WaitTimers(1, timeout) {
		return false
	}
	created := c.Created()
	fired := c.Advance(d)
	return c.WaitCreated(created+fired, timeout)
}

// Created is how many timers were ever created (fired, stopped or pending).
func (c *ManualClock) Created() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created
}

// WaitTimers blocks until at least n timers are registered and unfired.
func (c *ManualClock) WaitTimers(n int, timeout time.Duration) bool {
	return c.waitFor(timeout, func() bool { return len(c.timers) >= n })
}

// WaitCreated blocks until at least n timers were ever created.
func (c *ManualClock) WaitCreated(n int, timeout time.Duration) bool {
	return c.waitFor(timeout, func() bool { return c.created >= n })
}

func (c *ManualClock) waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ok := cond()
		c.mu.Unlock()
		if ok {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
