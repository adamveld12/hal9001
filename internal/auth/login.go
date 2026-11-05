package auth

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// loginTTL is how long a client has to answer a challenge.
	loginTTL = 5 * time.Minute
	// maxPendingLogins bounds memory held for unanswered challenges; the
	// oldest are dropped first.
	maxPendingLogins = 256
)

// pendingLogin is a stack evaluation paused on a Challenge, held server-side
// and bound to the client that started it.
type pendingLogin struct {
	service Service
	host    string
	peer    netip.Addr
	stack   Stack
	paused  *Pending
	expires time.Time
	created time.Time
}

type loginTable struct {
	mu sync.Mutex
	m  map[string]*pendingLogin
}

func newLoginTable() *loginTable { return &loginTable{m: map[string]*pendingLogin{}} }

func (t *loginTable) put(id string, p *pendingLogin, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if !now.Before(v.expires) {
			delete(t.m, k)
		}
	}
	for len(t.m) >= maxPendingLogins {
		oldest, oldestKey := now.Add(time.Hour), ""
		for k, v := range t.m {
			if v.created.Before(oldest) {
				oldest, oldestKey = v.created, k
			}
		}
		delete(t.m, oldestKey)
	}
	t.m[id] = p
}

// take removes and returns the pending login; each challenge answers once.
func (t *loginTable) take(id string, now time.Time) (*pendingLogin, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.m[id]
	if !ok {
		return nil, false
	}
	delete(t.m, id)
	if !now.Before(p.expires) {
		return nil, false
	}
	return p, true
}
