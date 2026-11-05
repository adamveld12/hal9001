package api

import (
	"context"
	"sync"
	"time"

	"hal9001/internal/store"
)

// changeBus fans store change notifications out to long-poll waiters. Each
// waiter has a one-slot wake channel, so publishing never blocks the
// committing goroutine and bursts coalesce into one wake-up.
type changeBus struct {
	mu   sync.Mutex
	subs map[*waiter]struct{}
}

type waiter struct {
	match func(store.Change) bool
	wake  chan struct{}
}

func newChangeBus() *changeBus { return &changeBus{subs: map[*waiter]struct{}{}} }

func (b *changeBus) subscribe(match func(store.Change) bool) (*waiter, func()) {
	w := &waiter{match: match, wake: make(chan struct{}, 1)}
	b.mu.Lock()
	b.subs[w] = struct{}{}
	b.mu.Unlock()
	return w, func() {
		b.mu.Lock()
		delete(b.subs, w)
		b.mu.Unlock()
	}
}

func (b *changeBus) publish(c store.Change) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for w := range b.subs {
		if !w.match(c) {
			continue
		}
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// waitFor is waitMatch for changes to one record.
func (s *Server) waitFor(ctx context.Context, d time.Duration, resource, id string, done func() (bool, error)) error {
	return s.waitMatch(ctx, d, func(c store.Change) bool { return c.Resource == resource && c.ID == id }, done)
}

// waitMatch blocks until done reports true, d elapses, or ctx ends. It
// re-checks done whenever a committed store change satisfies match. The
// subscription is registered before the first check, so a change that lands
// between the check and the wait is not missed.
//
// done returns an error to abort (for example a database failure).
func (s *Server) waitMatch(ctx context.Context, d time.Duration, match func(store.Change) bool, done func() (bool, error)) error {
	if d <= 0 {
		return nil
	}
	w, cancel := s.bus.subscribe(match)
	defer cancel()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		select {
		case <-w.wake:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return nil
		case <-s.hub.done:
			return nil
		}
	}
}
