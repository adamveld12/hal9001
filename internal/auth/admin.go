package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"hal9001/internal/httpsec"
)

// This file is the surface the sign-in settings endpoints (internal/api,
// /api/auth/overview and friends) build on: the password rules and failure
// limiter, locality of a request, session ending and the audit trail of
// configuration changes.

// Password length bounds. The minimum applies to every way of setting a
// password (the API and `hal9001 auth set-password`).
const (
	MinPasswordLength = 12
	MaxPasswordLength = 1024
)

// ValidatePassword reports why password may not be stored, or nil.
func ValidatePassword(password string) error {
	if n := utf8.RuneCountInString(password); n < MinPasswordLength {
		return fmt.Errorf("the password must be at least %d characters (it has %d)", MinPasswordLength, n)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("the password must be at most %d bytes", MaxPasswordLength)
	}
	return nil
}

// TypeRateLimited is the problem type of a 429 from too many failed password attempts.
const TypeRateLimited = httpsec.ProblemBase + "rate-limited"

// Failed password attempts (password logins and wrong current_password
// values) from one peer address are limited: after loginFailureLimit failures
// within loginFailureWindow every further attempt is refused until the oldest
// failure leaves the window. A success clears the peer's failures.
const (
	loginFailureLimit  = 5
	loginFailureWindow = 15 * time.Minute
	// limiterMaxPeers bounds the table; when it is full, expired entries are dropped.
	limiterMaxPeers = 4096
)

type failureLimiter struct {
	mu    sync.Mutex
	fails map[netip.Addr][]time.Time
}

func newFailureLimiter() *failureLimiter {
	return &failureLimiter{fails: map[netip.Addr][]time.Time{}}
}

// live returns the failures of key inside the window, dropping older ones.
// The caller holds mu.
func (l *failureLimiter) live(key netip.Addr, now time.Time) []time.Time {
	cutoff := now.Add(-loginFailureWindow)
	list := l.fails[key]
	i := 0
	for i < len(list) && !list[i].After(cutoff) {
		i++
	}
	list = list[i:]
	if len(list) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = list
	return list
}

// retryAfter is how long key must wait before its next attempt; zero when it may try now.
func (l *failureLimiter) retryAfter(key netip.Addr, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.live(key, now)
	if len(list) < loginFailureLimit {
		return 0
	}
	return list[len(list)-loginFailureLimit].Add(loginFailureWindow).Sub(now)
}

func (l *failureLimiter) fail(key netip.Addr, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) >= limiterMaxPeers {
		for k := range l.fails {
			l.live(k, now)
		}
	}
	l.fails[key] = append(l.live(key, now), now)
}

func (l *failureLimiter) reset(key netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func peerOf(r *http.Request) netip.Addr {
	ap, _ := netip.ParseAddrPort(r.RemoteAddr)
	return ap.Addr().Unmap()
}

// PasswordRetryAfter is how long the client of r must wait before another
// password attempt; zero when it may try now.
func (m *Manager) PasswordRetryAfter(r *http.Request) time.Duration {
	return m.limiter.retryAfter(peerOf(r), m.now())
}

// NotePasswordFailure counts a failed password attempt of r's client.
func (m *Manager) NotePasswordFailure(r *http.Request) { m.limiter.fail(peerOf(r), m.now()) }

// NotePasswordSuccess forgets the failed password attempts of r's client.
func (m *Manager) NotePasswordSuccess(r *http.Request) { m.limiter.reset(peerOf(r)) }

// WriteRateLimited answers 429 rate-limited with Retry-After.
func WriteRateLimited(w http.ResponseWriter, wait time.Duration) {
	secs := max(int((wait+time.Second-1)/time.Second), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpsec.WriteProblem(w, httpsec.Problem{
		Type: TypeRateLimited, Title: "Too many attempts", Status: http.StatusTooManyRequests,
		Detail:     fmt.Sprintf("too many failed password attempts from this address; try again in %d seconds", secs),
		Extensions: map[string]any{"retry_after": secs},
	})
}

// IsLocal reports whether r passes the localhost-trust conditions: loopback
// peer, Host localhost or 127.0.0.1, no proxy headers. A request that does
// not came over the tailnet or through a proxy.
func (m *Manager) IsLocal(r *http.Request) bool {
	req := m.newRequest(r, m.Config(), ServiceAdmin, false)
	return LocalhostTrust{}.Authenticate(r.Context(), req).Kind == KindSuccess
}

// LocalAdmitted returns the services whose stack in c admits a plain request
// from this Mac: loopback peer, Host localhost, no proxy headers, not an
// interactive login.
func (m *Manager) LocalAdmitted(ctx context.Context, c Config) []Service {
	var out []Service
	for _, svc := range Services {
		req := &AuthRequest{
			Service: svc, HTTPMethod: http.MethodGet, Path: "/", Host: "localhost", RemoteAddr: "127.0.0.1:1",
			Peer: netip.MustParseAddr("127.0.0.1"), Header: http.Header{}, Settings: c.settings(),
		}
		if m.stackFor(c, svc).Evaluate(ctx, req).Outcome == OutcomeSuccess {
			out = append(out, svc)
		}
	}
	return out
}

// Validate checks c the way Reload does, without applying it.
func (m *Manager) Validate(c Config) error {
	if err := c.Validate(m.reg); err != nil {
		return fmt.Errorf("auth config: %w", err)
	}
	return nil
}

// OwnerLogin is the Tailscale login that owns this Mac.
func (m *Manager) OwnerLogin(ctx context.Context) (string, error) { return m.ts.ownerLogin(ctx) }

// SessionsMatching returns the active sessions for which match is true.
func (m *Manager) SessionsMatching(ctx context.Context, match func(Session) bool) ([]Session, error) {
	list, err := m.store.ListSessions(ctx, m.now())
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(list, func(s Session) bool { return !match(s) }), nil
}

// EndSessions revokes the active sessions for which match is true and returns their ids.
func (m *Manager) EndSessions(ctx context.Context, match func(Session) bool) ([]string, error) {
	list, err := m.SessionsMatching(ctx, match)
	if err != nil {
		return nil, err
	}
	var ended []string
	for _, s := range list {
		ok, err := m.store.RevokeSession(ctx, s.ID, m.now())
		if err != nil {
			return ended, err
		}
		if ok {
			ended = append(ended, s.ID)
		}
	}
	return ended, nil
}

// ClearSessionCookie tells the browser to drop its session cookie.
func (m *Manager) ClearSessionCookie(w http.ResponseWriter, r *http.Request) { m.clearCookie(w, r) }

// PasskeyCount is the number of registered passkeys.
func (m *Manager) PasskeyCount(ctx context.Context) (int, error) {
	list, err := m.store.ListAllWebAuthnCredentials(ctx)
	return len(list), err
}

// lastLoginScan is how many recent auth events LastPasswordLogin looks at.
const lastLoginScan = 1000

// LastPasswordLogin is the time of the latest successful sign-in that used the
// password module, or nil when none is among the recent auth events.
func (m *Manager) LastPasswordLogin(ctx context.Context) (*time.Time, error) {
	events, err := m.store.ListAuthEvents(ctx, lastLoginScan)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		if e.Outcome != OutcomeSuccess || (e.Action != "login_begin" && e.Action != "login_continue") {
			continue
		}
		for _, t := range e.Modules {
			if t.Module == "password" && t.Result == KindSuccess {
				at := e.Time.UTC()
				return &at, nil
			}
		}
	}
	return nil, nil
}

// RecordChange logs a change of the sign-in configuration taken by the
// authenticated caller of r (action is one of the auth_* actions of the
// contract; reason says what changed).
func (m *Manager) RecordChange(r *http.Request, action string, outcome Outcome, reason string) {
	id, _ := IdentityFrom(r.Context())
	m.record(r.Context(), AuthEvent{
		Action: action, Service: ServiceAdmin, Endpoint: r.Method + " " + r.URL.Path, Outcome: outcome, Reason: reason,
		Principal: id.Principal.Name, RemoteAddr: r.RemoteAddr, Host: r.Host, SessionID: id.SessionID,
	})
}

// isPasswordAttempt reports whether a begin request tries the password module:
// it names the method, or presents a password without naming one.
func isPasswordAttempt(req *AuthRequest) bool {
	return req.Method == "password" || (req.Method == "" && req.StringParam("password") != "")
}

// passwordResult reports how the password module fared in ev: failed, or succeeded.
func passwordResult(ev Evaluation) (failed, succeeded bool) {
	for _, t := range ev.Trace {
		if t.Module != "password" {
			continue
		}
		switch t.Result {
		case KindFail:
			failed = true
		case KindSuccess:
			succeeded = true
		}
	}
	return failed, succeeded && ev.Outcome == OutcomeSuccess
}
