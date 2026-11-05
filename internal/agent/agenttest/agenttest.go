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
	"sort"
	"sync"
	"time"

	"hal9001/internal/agent"
)

// BuildFakeAgent compiles cmd/fake-acp-agent into dir and returns the path.
func BuildFakeAgent(dir string) (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate module root")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	bin := filepath.Join(dir, "fake-acp-agent")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-buildvcs=false", "-o", bin, "hal9001/cmd/fake-acp-agent") //nolint:gosec // fixed arguments
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build fake-acp-agent: %w\n%s", err, out)
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
	pending   map[string]*pendingApproval
	expired   []string
	arrived   chan agent.ToolApproval
	nextID    int
}

type pendingApproval struct {
	req agent.ToolApproval
	ch  chan agent.ToolDecision
}

var _ agent.ApprovalBroker = (*Broker)(nil)

func NewBroker() *Broker {
	return &Broker{pending: map[string]*pendingApproval{}, arrived: make(chan agent.ToolApproval, 64)}
}

func (b *Broker) SubmitProposal(_ context.Context, p agent.ProposalApproval) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.proposals = append(b.proposals, p)
	b.nextID++
	return fmt.Sprintf("approval-%d", b.nextID), nil
}

func (b *Broker) AwaitToolApproval(ctx context.Context, a agent.ToolApproval) (agent.ToolDecision, error) {
	pa := &pendingApproval{req: a, ch: make(chan agent.ToolDecision, 1)}
	b.mu.Lock()
	b.pending[a.PermissionRequestID] = pa
	b.mu.Unlock()
	b.arrived <- a
	select {
	case d := <-pa.ch:
		return d, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, a.PermissionRequestID)
		b.expired = append(b.expired, a.PermissionRequestID)
		b.mu.Unlock()
		return agent.ToolDecision{}, ctx.Err()
	}
}

// Proposals returns the proposals submitted so far.
func (b *Broker) Proposals() []agent.ProposalApproval {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]agent.ProposalApproval(nil), b.proposals...)
}

// Expired returns permission request ids whose wait ended without a decision.
func (b *Broker) Expired() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.expired...)
}

// Pending returns permission request ids currently waiting for a decision.
func (b *Broker) Pending() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	for id := range b.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Next waits for the next tool approval to be requested.
func (b *Broker) Next(timeout time.Duration) (agent.ToolApproval, bool) {
	select {
	case a := <-b.arrived:
		return a, true
	case <-time.After(timeout):
		return agent.ToolApproval{}, false
	}
}

// Decide answers a pending approval; it reports whether one was waiting.
func (b *Broker) Decide(permissionRequestID string, d agent.ToolDecision) bool {
	b.mu.Lock()
	pa, ok := b.pending[permissionRequestID]
	delete(b.pending, permissionRequestID)
	b.mu.Unlock()
	if ok {
		pa.ch <- d
	}
	return ok
}

// ManualClock is a Clock the test advances by hand.
type ManualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
}

type timer struct {
	at time.Time
	ch chan time.Time
}

var _ agent.Clock = (*ManualClock)(nil)

func NewManualClock() *ManualClock {
	return &ManualClock{now: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{at: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// Advance moves time forward and fires every timer that became due.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	rest := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			rest = append(rest, t)
		}
	}
	c.timers = rest
}

// WaitTimers blocks until at least n timers are registered and unfired.
func (c *ManualClock) WaitTimers(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		have := len(c.timers)
		c.mu.Unlock()
		if have >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
