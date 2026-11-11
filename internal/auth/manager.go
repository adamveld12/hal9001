package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"hal9001/internal/httpsec"
	"hal9001/internal/tailscale"
)

// SessionCookie is the name of the session cookie.
const SessionCookie = "hal9001_session"

// slideInterval throttles sliding-expiry writes: a session's expiry is
// extended at most once per interval.
const slideInterval = time.Minute

// Options configures a Manager.
type Options struct {
	Config Config
	Store  Store
	// Tailscale is the CLI abstraction; nil means the real CLI.
	Tailscale tailscale.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Modules are extra modules to register next to the built-in ones.
	Modules []Module
	// Logger receives operational errors (e.g. a failed audit write); nil
	// means slog.Default().
	Logger *slog.Logger
}

// Manager owns the auth configuration, module registry, sessions and login
// exchanges. Its HTTP surface is Require (middleware) and Routes.
type Manager struct {
	store  Store
	reg    *Registry
	now    func() time.Time
	log    *slog.Logger
	cfg    atomic.Pointer[Config]
	ts     *TailscaleIdentity
	wa     *WebAuthn
	logins *loginTable
}

// New builds a Manager with the built-in modules registered (localhost-trust,
// tailscale-identity, password, webauthn) and validates opts.Config.
func New(opts Options) (*Manager, error) {
	if opts.Store == nil {
		return nil, errors.New("auth: Store is required")
	}
	clock := opts.Now
	if clock == nil {
		clock = time.Now
	}
	m := &Manager{store: opts.Store, reg: NewRegistry(), now: func() time.Time { return clock().UTC() }, log: opts.Logger, logins: newLoginTable()}
	if m.log == nil {
		m.log = slog.Default()
	}
	ts := opts.Tailscale
	if ts == nil {
		ts = tailscale.CLI{}
	}
	m.ts = NewTailscaleIdentity(ts, m.now)
	m.wa = NewWebAuthn(opts.Store, m.now)
	for _, mod := range append([]Module{LocalhostTrust{}, m.ts, m.wa, Password{}}, opts.Modules...) {
		if err := m.reg.Register(mod); err != nil {
			return nil, err
		}
	}
	if err := m.Reload(opts.Config); err != nil {
		return nil, err
	}
	return m, nil
}

// Reload validates and atomically swaps the configuration. Pending logins
// keep the stack they started with.
func (m *Manager) Reload(c Config) error {
	if err := c.Validate(m.reg); err != nil {
		return fmt.Errorf("auth config: %w", err)
	}
	m.cfg.Store(&c)
	return nil
}

// Config returns the active configuration.
func (m *Manager) Config() Config { return *m.cfg.Load() }

func (m *Manager) stackFor(c Config, svc Service) *Stack {
	name := c.Services[svc]
	return &Stack{Name: name, Entries: c.Stacks[name], Registry: m.reg}
}

// loginMethods lists the interactive modules of svc's stack, for login UIs.
func (m *Manager) loginMethods(c Config, svc Service) []string {
	methods := []string{}
	for _, e := range c.Stacks[c.Services[svc]] {
		if mod, ok := m.reg.Get(e.Module); ok {
			if _, ok := mod.(Interactive); ok {
				methods = append(methods, e.Module)
			}
		}
	}
	return methods
}

func (m *Manager) newRequest(r *http.Request, c Config, svc Service, interactive bool) *AuthRequest {
	peer, _ := netip.ParseAddrPort(r.RemoteAddr)
	return &AuthRequest{
		Service:     svc,
		HTTPMethod:  r.Method,
		Path:        r.URL.Path,
		Host:        r.Host,
		RemoteAddr:  r.RemoteAddr,
		Peer:        peer.Addr().Unmap(),
		Header:      r.Header,
		Secure:      isSecure(r, peer.Addr()),
		Interactive: interactive,
		Settings:    c.settings(),
	}
}

// isSecure reports whether the client's connection to us is HTTPS: directly
// over TLS, or via a loopback proxy (tailscale serve) that terminates TLS.
func isSecure(r *http.Request, peer netip.Addr) bool {
	if r.TLS != nil {
		return true
	}
	if !peer.IsValid() || !peer.Unmap().IsLoopback() {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") ||
		strings.HasSuffix(hostName(r.Host), ".ts.net")
}

// Identity is the authenticated caller, available from IdentityFrom inside a
// handler wrapped by Require.
type Identity struct {
	Principal Principal
	// Source is "session" or "stack".
	Source  string
	Modules []string
	Service Service
	// SessionID is set when Source is "session".
	SessionID string
}

type ctxKey struct{}

// IdentityFrom returns the identity Require attached to ctx.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Require returns middleware that admits a request when a valid session
// cookie covers svc, or else when svc's stack succeeds for the request alone
// (non-interactive modules such as localhost-trust and tailscale-identity).
// Otherwise it responds 401, or 403 when a valid session exists but does not
// cover svc. The decision is recorded as an AuthEvent, except for session
// admissions, which are not logged per request.
func (m *Manager) Require(svc Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, status := m.authorize(w, r, svc)
			if status != 0 {
				m.deny(w, svc, status)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
		})
	}
}

// authorize returns the identity, or a non-zero HTTP status to deny with.
func (m *Manager) authorize(w http.ResponseWriter, r *http.Request, svc Service) (Identity, int) {
	c := m.Config()
	sess, token, hasSession := m.activeSession(r)
	if hasSession && sess.Covers(svc) {
		m.slide(r.Context(), w, r, &sess, token, c)
		return Identity{Principal: Principal{Name: sess.Principal}, Source: "session", Modules: sess.Modules, Service: svc, SessionID: sess.ID}, 0
	}

	req := m.newRequest(r, c, svc, false)
	ev := m.stackFor(c, svc).Evaluate(r.Context(), req)
	m.record(r.Context(), eventFor(req, "request", ev, sess.ID))
	if ev.Outcome == OutcomeSuccess {
		return Identity{Principal: ev.Principal, Source: "stack", Modules: ev.Modules, Service: svc}, 0
	}
	if hasSession {
		return Identity{}, http.StatusForbidden
	}
	return Identity{}, http.StatusUnauthorized
}

func (m *Manager) deny(w http.ResponseWriter, svc Service, status int) {
	p := httpsec.Problem{
		Type:       httpsec.TypeUnauthenticated,
		Title:      "Authentication required",
		Status:     status,
		Detail:     fmt.Sprintf("service %q requires authentication", svc),
		Extensions: map[string]any{"service": svc, "methods": m.loginMethods(m.Config(), svc)},
	}
	if status == http.StatusForbidden {
		p.Type, p.Title = httpsec.TypeForbidden, "Session does not cover this service"
		p.Detail = fmt.Sprintf("your session does not grant service %q", svc)
	} else {
		w.Header().Set("WWW-Authenticate", `Cookie realm="hal9001"`)
		p.Extensions["next_step"] = map[string]string{"method": "POST", "href": "/api/auth/" + string(svc) + "/begin"}
	}
	httpsec.WriteProblem(w, p)
}

func eventFor(req *AuthRequest, action string, ev Evaluation, sessionID string) AuthEvent {
	e := AuthEvent{
		Action:     action,
		Service:    req.Service,
		Endpoint:   req.HTTPMethod + " " + req.Path,
		Modules:    ev.Trace,
		Outcome:    ev.Outcome,
		Reason:     ev.Reason,
		RemoteAddr: req.RemoteAddr,
		Host:       req.Host,
		SessionID:  sessionID,
	}
	if ev.Outcome == OutcomeSuccess {
		e.Principal = ev.Principal.Name
	}
	return e
}

// record writes an auth event. A failed audit write is logged, not fatal:
// refusing every request because the audit table is unwritable would lock the
// owner out of the tool that shows them why.
func (m *Manager) record(ctx context.Context, e AuthEvent) {
	now := m.now()
	e.ID = ulid.MustNew(ulid.Timestamp(now), rand.Reader).String()
	e.Time = now.UTC()
	// Detach from request cancellation so a hung-up client doesn't drop the event.
	if err := m.store.AppendAuthEvent(context.WithoutCancel(ctx), e); err != nil {
		m.log.Error("auth: write auth event failed", "err", err, "action", e.Action, "outcome", e.Outcome)
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// activeSession returns the request's session when its cookie names an
// unrevoked, unexpired session.
func (m *Manager) activeSession(r *http.Request) (Session, string, bool) {
	ck, err := r.Cookie(SessionCookie)
	if err != nil || ck.Value == "" {
		return Session{}, "", false
	}
	s, err := m.store.GetSession(r.Context(), hashToken(ck.Value))
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			m.log.Error("auth: load session failed", "err", err)
		}
		return Session{}, "", false
	}
	if !s.Active(m.now()) {
		return Session{}, "", false
	}
	return s, ck.Value, true
}

// slide extends the session's expiry (at most once per slideInterval) and
// re-sends the cookie so the browser's copy lives as long as the server's.
func (m *Manager) slide(ctx context.Context, w http.ResponseWriter, r *http.Request, s *Session, token string, c Config) {
	now := m.now()
	if now.Sub(s.LastSeenAt) < slideInterval {
		return
	}
	s.LastSeenAt, s.ExpiresAt = now, now.Add(c.sessionTTL())
	if err := m.store.UpdateSession(ctx, *s); err != nil {
		m.log.Error("auth: slide session failed", "err", err)
		return
	}
	m.setCookie(w, r, token, c.sessionTTL())
}

func (m *Manager) setCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, sessionCookieFor(r, token, int(ttl/time.Second)))
}

func (m *Manager) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, sessionCookieFor(r, "", -1))
}

// sessionCookieFor builds the session cookie: HttpOnly, SameSite=Strict, and
// Secure whenever the request came over HTTPS (REQUIREMENTS §10.3). Plain
// http://localhost has to work, so Secure cannot be unconditional.
func sessionCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	peer, _ := netip.ParseAddrPort(r.RemoteAddr)
	c := &http.Cookie{ //nolint:gosec // G124: Secure is set below from the request scheme; http://localhost must work
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	c.Secure = isSecure(r, peer.Addr())
	return c
}

// grantedServices returns the services whose stack is the one that just
// authenticated the caller: every module that satisfied it satisfies them too
// (no step-up, REQUIREMENTS §10.4).
func grantedServices(c Config, svc Service) []Service {
	name := c.Services[svc]
	var out []Service
	for _, s := range Services {
		if c.Services[s] == name {
			out = append(out, s)
		}
	}
	return out
}

// establishSession records a successful login: it extends the caller's
// current session when it belongs to the same principal, otherwise creates a
// new one, and sets the cookie.
func (m *Manager) establishSession(ctx context.Context, w http.ResponseWriter, r *http.Request, c Config, svc Service, ev Evaluation) (Session, error) {
	now := m.now()
	services := grantedServices(c, svc)
	if cur, token, ok := m.activeSession(r); ok && cur.Principal == ev.Principal.Name {
		for _, s := range services {
			if !cur.Covers(s) {
				cur.Services = append(cur.Services, s)
			}
		}
		for _, mod := range ev.Modules {
			if !containsString(cur.Modules, mod) {
				cur.Modules = append(cur.Modules, mod)
			}
		}
		cur.LastSeenAt, cur.ExpiresAt = now, now.Add(c.sessionTTL())
		if err := m.store.UpdateSession(ctx, cur); err != nil {
			return Session{}, err
		}
		m.setCookie(w, r, token, c.sessionTTL())
		return cur, nil
	}
	token, err := newToken()
	if err != nil {
		return Session{}, err
	}
	s := Session{
		ID:         hashToken(token),
		Principal:  ev.Principal.Name,
		Modules:    append([]string(nil), ev.Modules...),
		Services:   services,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  now.Add(c.sessionTTL()),
		Host:       r.Host,
		RemoteAddr: r.RemoteAddr,
	}
	if err := m.store.CreateSession(ctx, s); err != nil {
		return Session{}, err
	}
	m.setCookie(w, r, token, c.sessionTTL())
	return s, nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
