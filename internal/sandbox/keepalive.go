package sandbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"hal9001/internal/agent"
)

// closeTimeout bounds the teardown of a held sandbox after its time is up.
const closeTimeout = 2 * time.Minute

// KeeperOptions configures NewKeeper.
type KeeperOptions struct {
	// Clock times the holds; nil means the wall clock. Tests pass a manual clock.
	Clock agent.Clock
	Log   *slog.Logger
}

// Keeper holds sandbox sessions after the run that used them ended, so a conversation can continue in the same
// workspace. A held session is closed when its time is up, when a new Hold replaces it, or on CloseAll. Take hands it
// to a new owner instead. Safe for concurrent use.
type Keeper struct {
	clock agent.Clock
	log   *slog.Logger

	mu     sync.Mutex
	held   map[string]*held
	closed bool
	wg     sync.WaitGroup
}

type held struct {
	sess    Session
	success bool
	timer   agent.Timer
	stop    chan struct{}
}

// NewKeeper returns an empty Keeper.
func NewKeeper(opts KeeperOptions) *Keeper {
	if opts.Clock == nil {
		opts.Clock = agent.WallClock{}
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Keeper{clock: opts.Clock, log: opts.Log, held: map[string]*held{}}
}

// Hold keeps sess under key for ttl and then closes it with Close(ctx, success). A ttl of 0 or less closes it now,
// before Hold returns. A session already held under key is closed first (with the success it was held with).
func (k *Keeper) Hold(key string, sess Session, ttl time.Duration, success bool) {
	k.mu.Lock()
	old := k.held[key]
	delete(k.held, key)
	if ttl <= 0 || k.closed {
		k.mu.Unlock()
		k.discard(old)
		k.closeSession(key, sess, success)
		return
	}
	h := &held{sess: sess, success: success, timer: k.clock.NewTimer(ttl), stop: make(chan struct{})}
	k.held[key] = h
	k.wg.Add(1)
	k.mu.Unlock()
	k.discard(old)
	go k.expire(key, h)
}

// expire closes h when its timer fires, unless it was taken or replaced first.
func (k *Keeper) expire(key string, h *held) {
	defer k.wg.Done()
	select {
	case <-h.timer.C():
	case <-h.stop:
		return
	}
	k.mu.Lock()
	if k.held[key] != h {
		k.mu.Unlock()
		return
	}
	delete(k.held, key)
	k.mu.Unlock()
	k.closeSession(key, h.sess, h.success)
}

// Take removes the session held under key without closing it and stops its timer.
func (k *Keeper) Take(key string) (Session, bool) {
	k.mu.Lock()
	h := k.held[key]
	delete(k.held, key)
	k.mu.Unlock()
	if h == nil {
		return nil, false
	}
	k.stopHeld(h)
	return h.sess, true
}

// Release closes the session held under key now. It reports whether there was one.
func (k *Keeper) Release(key string) bool {
	k.mu.Lock()
	h := k.held[key]
	delete(k.held, key)
	k.mu.Unlock()
	if h == nil {
		return false
	}
	k.discard(h)
	return true
}

// Holding reports whether a session is held under key.
func (k *Keeper) Holding(key string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, ok := k.held[key]
	return ok
}

// CloseAll closes every held session and makes later Holds close at once. The daemon calls it on shutdown, before the
// Manager closes the backends.
func (k *Keeper) CloseAll() {
	k.mu.Lock()
	k.closed = true
	all := k.held
	k.held = map[string]*held{}
	k.mu.Unlock()
	for key, h := range all {
		k.stopHeld(h)
		k.closeSession(key, h.sess, h.success)
	}
	k.wg.Wait()
}

// discard stops h (when not nil) and closes its session. h must already be removed from the map.
func (k *Keeper) discard(h *held) {
	if h == nil {
		return
	}
	k.stopHeld(h)
	k.closeSession("", h.sess, h.success)
}

func (k *Keeper) stopHeld(h *held) {
	h.timer.Stop()
	close(h.stop)
}

func (k *Keeper) closeSession(key string, sess Session, success bool) {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	if err := sess.Close(ctx, success); err != nil {
		k.log.Warn("sandbox: close held session", "key", key, "err", err)
	}
}
