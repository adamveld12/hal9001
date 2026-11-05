package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/auth"
	"hal9001/internal/httpsec"
)

const (
	tsHost     = "alices-macbook.tail1234.ts.net"
	localHost  = "localhost:9001"
	testPW     = "correct horse battery staple"
	clientIP   = "100.101.102.103"
	loopbackRA = "127.0.0.1:50000"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	pwHashOnce sync.Once
	pwHash     string
	pwHashErr  error
)

// testPasswordHash hashes testPW once: argon2id is deliberately slow.
func testPasswordHash(t *testing.T) string {
	t.Helper()
	pwHashOnce.Do(func() { pwHash, pwHashErr = auth.HashPassword(testPW) })
	if pwHashErr != nil {
		t.Fatal(pwHashErr)
	}
	return pwHash
}

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

// fakeTailscale serves canned whois/status data and counts calls.
type fakeTailscale struct {
	mu          sync.Mutex
	whois       map[netip.Addr]auth.Whois
	status      auth.TailscaleStatus
	serve       auth.ServeConfig
	err         error
	whoisCalls  int
	statusCalls int
}

func newFakeTailscale(t *testing.T) *fakeTailscale {
	t.Helper()
	st, err := auth.ParseStatus(fixture(t, "status.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &fakeTailscale{whois: map[netip.Addr]auth.Whois{}, status: st}
}

func (f *fakeTailscale) setWhois(t *testing.T, ip, fixtureName string) {
	t.Helper()
	w, err := auth.ParseWhois(fixture(t, fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.whois[netip.MustParseAddr(ip)] = w
}

func (f *fakeTailscale) Whois(_ context.Context, ip netip.Addr) (auth.Whois, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.whoisCalls++
	if f.err != nil {
		return auth.Whois{}, f.err
	}
	w, ok := f.whois[ip]
	if !ok {
		return auth.Whois{}, &noMatchError{ip}
	}
	return w, nil
}

func (f *fakeTailscale) Status(context.Context) (auth.TailscaleStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusCalls++
	if f.err != nil {
		return auth.TailscaleStatus{}, f.err
	}
	return f.status, nil
}

func (f *fakeTailscale) ServeStatus(context.Context) (auth.ServeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serve, f.err
}

type noMatchError struct{ ip netip.Addr }

func (e *noMatchError) Error() string { return "no match for " + e.ip.String() }

// scriptedModule returns fixed results; used to drive the stack evaluator and
// to stand in for second factors.
type scriptedModule struct {
	name string
	fn   func(*auth.AuthRequest) auth.Result
}

func (m scriptedModule) Name() string { return m.name }
func (m scriptedModule) Authenticate(_ context.Context, r *auth.AuthRequest) auth.Result {
	return m.fn(r)
}

func fixed(name string, r auth.Result) scriptedModule {
	return scriptedModule{name: name, fn: func(*auth.AuthRequest) auth.Result { return r }}
}

type env struct {
	t     *testing.T
	m     *auth.Manager
	store *auth.MemoryStore
	clock *fakeClock
	ts    *fakeTailscale
	h     http.Handler
}

type envOpt func(*envConfig)

type envConfig struct {
	cfg     auth.Config
	modules []auth.Module
	ts      auth.Tailscale
}

func withConfig(c auth.Config) envOpt     { return func(e *envConfig) { e.cfg = c } }
func withModules(m ...auth.Module) envOpt { return func(e *envConfig) { e.modules = m } }
func withTailscale(ts auth.Tailscale) envOpt {
	return func(e *envConfig) { e.ts = ts }
}

// stackConfig maps all four services to one stack.
func stackConfig(entries ...auth.StackEntry) auth.Config {
	c := auth.DefaultConfig()
	c.Stacks = map[string][]auth.StackEntry{"default": entries}
	return c
}

func entry(module string, ctl auth.Control) auth.StackEntry {
	return auth.StackEntry{Module: module, Control: ctl}
}

// newEnv wires the auth routes plus a protected GET /api/ping per service
// behind the httpsec baseline, like the real daemon will.
func newEnv(t *testing.T, opts ...envOpt) *env {
	t.Helper()
	ec := envConfig{cfg: auth.DefaultConfig()}
	for _, o := range opts {
		o(&ec)
	}
	e := &env{t: t, store: auth.NewMemoryStore(), clock: newClock(), ts: newFakeTailscale(t)}
	if ec.ts == nil {
		ec.ts = e.ts
	}
	if ec.cfg.PasswordHash == "" {
		ec.cfg.PasswordHash = testPasswordHash(t)
	}
	m, err := auth.New(auth.Options{Config: ec.cfg, Store: e.store, Tailscale: ec.ts, Now: e.clock.Now, Modules: ec.modules})
	if err != nil {
		t.Fatal(err)
	}
	e.m = m

	mux := http.NewServeMux()
	m.Routes(mux, 9001)
	for _, svc := range auth.Services {
		mux.Handle("GET /api/ping/"+string(svc), m.Require(svc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := auth.IdentityFrom(r.Context())
			_ = json.NewEncoder(w).Encode(map[string]any{"principal": id.Principal.Name, "source": id.Source})
		})))
	}
	e.h = httpsec.Middleware(httpsec.Options{AllowedHosts: func() []string { return m.Config().AllowedHosts }})(mux)
	return e
}

// req is a builder for test requests.
type req struct {
	method, host, path string
	remote             string
	body               any
	headers            map[string]string
	cookies            []*http.Cookie
	https              bool
}

func get(path string) *req {
	return &req{method: "GET", host: localHost, path: path, remote: loopbackRA}
}

func post(path string, body any) *req {
	return &req{method: "POST", host: localHost, path: path, remote: loopbackRA, body: body}
}

func del(path string) *req {
	return &req{method: "DELETE", host: localHost, path: path, remote: loopbackRA}
}

func (r *req) on(host string) *req        { r.host = host; return r }
func (r *req) from(remote string) *req    { r.remote = remote; return r }
func (r *req) hdr(k, v string) *req       { r.set(k, v); return r }
func (r *req) cookie(c *http.Cookie) *req { r.cookies = append(r.cookies, c); return r }
func (r *req) viaHTTPS() *req             { r.https = true; return r }
func (r *req) set(k, v string) {
	if r.headers == nil {
		r.headers = map[string]string{}
	}
	r.headers[k] = v
}

// viaServe makes the request look like tailscale serve proxied it: loopback
// peer, ts.net Host, identity and forwarding headers.
func (r *req) viaServe(login, ip string) *req {
	r.host = tsHost
	r.remote = loopbackRA
	r.set("Tailscale-User-Login", login)
	r.set("X-Forwarded-For", ip)
	r.set("X-Forwarded-Proto", "https")
	return r
}

func (e *env) do(r *req) *httptest.ResponseRecorder {
	e.t.Helper()
	var body *bytes.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			e.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	scheme := "http"
	if r.https {
		scheme = "https"
	}
	hr := httptest.NewRequest(r.method, scheme+"://"+r.host+r.path, body)
	hr.Host = r.host
	hr.RemoteAddr = r.remote
	if r.method != "GET" {
		hr.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	for _, c := range r.cookies {
		hr.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, hr)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie in response (status %d, body %s)", rec.Code, rec.Body)
	return nil
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body)
	}
}

// lastEvent returns the newest auth event.
func (e *env) lastEvent() auth.AuthEvent {
	e.t.Helper()
	evs, err := e.store.ListAuthEvents(context.Background(), 1)
	if err != nil || len(evs) == 0 {
		e.t.Fatalf("no auth events (err %v)", err)
	}
	return evs[0]
}

func (e *env) events() []auth.AuthEvent {
	e.t.Helper()
	evs, err := e.store.ListAuthEvents(context.Background(), 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return evs
}

func moduleResults(ev auth.AuthEvent) []string {
	out := make([]string, len(ev.Modules))
	for i, m := range ev.Modules {
		out[i] = m.Module + ":" + string(m.Result)
	}
	return out
}

func joinResults(ev auth.AuthEvent) string { return strings.Join(moduleResults(ev), ",") }
