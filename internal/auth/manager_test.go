package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"hal9001/internal/auth"
)

// Scenario 21: stacks and PAM controls through the API.
func TestDefaultConfigLocalCurlSucceedsViaLocalhostTrust(t *testing.T) {
	e := newEnv(t)
	for _, svc := range auth.Services {
		rec := e.do(get("/api/ping/" + string(svc)))
		wantStatus(t, rec, 200)
		if got := decode[map[string]string](t, rec); got["principal"] != "localhost" || got["source"] != "stack" {
			t.Errorf("%s: %v", svc, got)
		}
	}
	ev := e.lastEvent()
	if ev.Outcome != auth.OutcomeSuccess || ev.Principal != "localhost" || ev.Service != auth.ServiceAdmin ||
		ev.Endpoint != "GET /api/ping/admin" || ev.RemoteAddr != loopbackRA || ev.Host != localHost || joinResults(ev) != "localhost-trust:success" {
		t.Errorf("event = %+v", ev)
	}
}

func TestWithoutLocalhostTrustCurlGets401AndPasswordLoginYieldsWorkingSession(t *testing.T) {
	e := newEnv(t, withConfig(stackConfig(entry("webauthn", auth.Sufficient), entry("password", auth.Sufficient))))

	rec := e.do(get("/api/ping/api"))
	wantStatus(t, rec, 401)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	p := decode[map[string]any](t, rec)
	if p["type"] != "urn:hal9001:problem:unauthenticated" || p["service"] != "api" {
		t.Errorf("problem = %v", p)
	}
	if ns, _ := p["next_step"].(map[string]any); ns["method"] != "POST" || ns["href"] != "/api/auth/api/begin" {
		t.Errorf("next_step = %v", p["next_step"])
	}
	if m, _ := p["methods"].([]any); len(m) != 2 || m[0] != "webauthn" || m[1] != "password" {
		t.Errorf("methods = %v, want [webauthn password]", p["methods"])
	}

	wantStatus(t, e.do(post("/api/auth/api/begin", map[string]string{"password": "wrong"})), 401)
	if ev := e.lastEvent(); ev.Outcome != auth.OutcomeFail || ev.Action != "login_begin" ||
		joinResults(ev) != "webauthn:ignore,password:fail" || ev.Modules[1].Reason != "wrong password" {
		t.Errorf("failed login event = %+v (%s)", ev, joinResults(ev))
	}

	rec = e.do(post("/api/auth/api/begin", map[string]string{"password": testPW}))
	wantStatus(t, rec, 200)
	cookie := sessionCookie(t, rec)
	resp := decode[struct {
		Status    string
		Principal struct{ Name string }
		Services  []string
	}](t, rec)
	if resp.Status != "success" || resp.Principal.Name != "owner" || len(resp.Services) != 4 {
		t.Errorf("login response = %+v", resp)
	}

	rec = e.do(get("/api/ping/admin").cookie(cookie))
	wantStatus(t, rec, 200)
	if got := decode[map[string]string](t, rec); got["principal"] != "owner" || got["source"] != "session" {
		t.Errorf("session request = %v", got)
	}
	wantStatus(t, e.do(get("/api/ping/admin")), 401)
}

func TestRequisiteFailureStopsEvaluationAndIsRecorded(t *testing.T) {
	e := newEnv(t,
		withModules(fixed("deny", auth.Fail("policy says no"))),
		withConfig(stackConfig(entry("deny", auth.Requisite), entry("localhost-trust", auth.Sufficient))))
	wantStatus(t, e.do(get("/api/ping/api")), 401)
	ev := e.lastEvent()
	if ev.Outcome != auth.OutcomeFail || joinResults(ev) != "deny:fail" || !strings.Contains(ev.Reason, "requisite module deny failed") {
		t.Errorf("event = %+v (%s)", ev, joinResults(ev))
	}
}

func TestRequiredFailureFollowedBySufficientSuccessStillFails(t *testing.T) {
	e := newEnv(t,
		withModules(fixed("deny", auth.Fail("policy says no"))),
		withConfig(stackConfig(entry("deny", auth.Required), entry("localhost-trust", auth.Sufficient))))
	wantStatus(t, e.do(get("/api/ping/api")), 401)
	ev := e.lastEvent()
	if ev.Outcome != auth.OutcomeFail || joinResults(ev) != "deny:fail,localhost-trust:success" {
		t.Errorf("event = %+v (%s): localhost-trust must have run and still lost", ev, joinResults(ev))
	}
	if ev.Principal != "" {
		t.Errorf("failed decision carries principal %q", ev.Principal)
	}
}

// A request that tailscaled proxied for the public internet (tailscale funnel) is never local, even when the peer is
// loopback and the Host header says localhost. The router refuses it earlier (httpsec); this is the second line.
func TestLocalhostTrustIgnoresFunnelRequests(t *testing.T) {
	for _, host := range []string{"localhost:9001", "127.0.0.1:9001", tsHost} {
		req := &auth.AuthRequest{
			Host: host, Peer: netip.MustParseAddr("127.0.0.1"),
			Header: http.Header{"Tailscale-Funnel-Request": {"?1"}},
		}
		if res := (auth.LocalhostTrust{}).Authenticate(context.Background(), req); res.Kind != auth.KindIgnore {
			t.Errorf("Host %s: localhost-trust result = %s, want ignore", host, res.Kind)
		}
	}
}

// Scenario 22.
func TestLocalhostTrustRejectsProxiedAndNonLocalRequests(t *testing.T) {
	tests := []struct {
		name  string
		build func() *req
	}{
		{"X-Forwarded-For", func() *req { return get("/api/ping/api").hdr("X-Forwarded-For", "100.1.2.3") }},
		{"X-Forwarded-Proto", func() *req { return get("/api/ping/api").hdr("X-Forwarded-Proto", "https") }},
		{"X-Forwarded-Host", func() *req { return get("/api/ping/api").hdr("x-forwarded-host", "evil.example") }},
		{"Forwarded", func() *req { return get("/api/ping/api").hdr("Forwarded", "for=1.2.3.4") }},
		{"Tailscale-User-Login", func() *req { return get("/api/ping/api").hdr("Tailscale-User-Login", "alice@example.com") }},
		{"any Tailscale-* header", func() *req { return get("/api/ping/api").hdr("Tailscale-Headers-Info", "x") }},
		{"ts.net Host from loopback (tailscale serve)", func() *req { return get("/api/ping/api").on(tsHost) }},
		{"non-loopback peer", func() *req { return get("/api/ping/api").from("192.0.2.7:4444") }},
		{"IPv6 non-loopback peer", func() *req { return get("/api/ping/api").from("[2001:db8::1]:4444") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := auth.DefaultConfig()
			cfg.AllowedHosts = []string{tsHost}
			cfg.Stacks = map[string][]auth.StackEntry{"default": {entry("localhost-trust", auth.Sufficient)}}
			e := newEnv(t, withConfig(cfg))
			wantStatus(t, e.do(tt.build()), 401)
			ev := e.lastEvent()
			if len(ev.Modules) != 1 || ev.Modules[0].Result != auth.KindIgnore || ev.Modules[0].Reason == "" {
				t.Errorf("trace = %+v, want localhost-trust ignore with a reason", ev.Modules)
			}
		})
	}

	t.Run("plain local requests are accepted, including 127.0.0.1 and IPv6 loopback peers", func(t *testing.T) {
		e := newEnv(t)
		wantStatus(t, e.do(get("/api/ping/api")), 200)
		wantStatus(t, e.do(get("/api/ping/api").on("127.0.0.1:9001")), 200)
		wantStatus(t, e.do(get("/api/ping/api").from("[::1]:5000")), 200)
	})
}

func TestPasswordIgnoredWithoutCredentialsFailedWhenWrong(t *testing.T) {
	// webauthn sufficient first so a stack-level "ignore" is distinguishable from a "fail".
	e := newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))

	rec := e.do(post("/api/auth/api/begin", map[string]string{}))
	wantStatus(t, rec, 401)
	if ev := e.lastEvent(); ev.Modules[0].Result != auth.KindIgnore || ev.Modules[0].Reason != "no password presented" {
		t.Errorf("no credentials: %+v, want ignore", ev.Modules)
	}

	wantStatus(t, e.do(post("/api/auth/api/begin", map[string]string{"password": "wrong"})), 401)
	if ev := e.lastEvent(); ev.Modules[0].Result != auth.KindFail {
		t.Errorf("wrong password: %+v, want fail", ev.Modules)
	}

	// A required password with no credentials still denies (ignore is not success)...
	e = newEnv(t, withConfig(stackConfig(entry("password", auth.Required))))
	wantStatus(t, e.do(post("/api/auth/api/begin", map[string]string{})), 401)
	// ...but an ignored requisite password does not block a later module.
	e = newEnv(t, withConfig(stackConfig(entry("password", auth.Requisite), entry("localhost-trust", auth.Sufficient))))
	wantStatus(t, e.do(get("/api/ping/api")), 200)
}

func TestPasswordLoginWithNoHashConfiguredFails(t *testing.T) {
	cfg := stackConfig(entry("password", auth.Sufficient))
	e := newEnv(t, withConfig(cfg))
	c := e.m.Config()
	c.PasswordHash = ""
	if err := e.m.Reload(c); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.do(post("/api/auth/api/begin", map[string]string{"password": testPW})), 401)
	if r := e.lastEvent().Modules[0]; r.Result != auth.KindFail || !strings.Contains(r.Reason, "no password is configured") {
		t.Errorf("trace = %+v", r)
	}
}

func TestMethodSelectionSkipsOtherInteractiveModules(t *testing.T) {
	// With a passkey registered, a bare begin challenges; selecting the password method must not.
	e := newEnv(t, withConfig(stackConfig(entry("webauthn", auth.Sufficient), entry("password", auth.Sufficient))))
	rec := e.do(post("/api/auth/api/begin", map[string]string{"method": "password", "password": testPW}))
	wantStatus(t, rec, 200)
	if ev := e.lastEvent(); joinResults(ev) != "webauthn:ignore,password:success" {
		t.Errorf("trace = %s", joinResults(ev))
	}
}

// Sessions.

func loginCookie(t *testing.T, e *env) *http.Cookie {
	t.Helper()
	rec := e.do(post("/api/auth/api/begin", map[string]string{"password": testPW}))
	wantStatus(t, rec, 200)
	return sessionCookie(t, rec)
}

func TestSessionSlidingExpiry(t *testing.T) {
	cfg := stackConfig(entry("password", auth.Sufficient))
	cfg.SessionTTL = time.Hour
	e := newEnv(t, withConfig(cfg))
	cookie := loginCookie(t, e)
	if cookie.MaxAge != 3600 {
		t.Errorf("cookie Max-Age = %d, want 3600", cookie.MaxAge)
	}
	ping := func() *httptest.ResponseRecorder { return e.do(get("/api/ping/api").cookie(cookie)) }

	// Each use within the TTL pushes expiry out, so 4 x 40min stays logged in
	// even though the total is far beyond one TTL.
	for i := 0; i < 4; i++ {
		e.clock.Advance(40 * time.Minute)
		rec := ping()
		wantStatus(t, rec, 200)
		if i > 0 && sessionCookie(t, rec).MaxAge != 3600 {
			t.Errorf("use %d: refreshed cookie Max-Age = %d, want 3600", i, sessionCookie(t, rec).MaxAge)
		}
	}
	// Idle past the TTL: the session is gone.
	e.clock.Advance(61 * time.Minute)
	wantStatus(t, ping(), 401)
}

func TestSessionRevocation(t *testing.T) {
	cfg := stackConfig(entry("password", auth.Sufficient))
	e := newEnv(t, withConfig(cfg))
	a, b := loginCookie(t, e), loginCookie(t, e)
	if a.Value == b.Value {
		t.Fatal("two logins share a session")
	}
	wantStatus(t, e.do(get("/api/ping/api").cookie(a)), 200)

	// List, then revoke only A using B.
	rec := e.do(get("/api/auth/sessions").cookie(b))
	wantStatus(t, rec, 200)
	list := decode[struct {
		Sessions []auth.Session
		Current  string
	}](t, rec)
	if len(list.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(list.Sessions))
	}
	var aID string
	for _, s := range list.Sessions {
		if s.ID != list.Current {
			aID = s.ID
		}
		if s.ID == a.Value || s.ID == b.Value {
			t.Error("session list exposes the cookie secret instead of its hash")
		}
	}
	wantStatus(t, e.do(del("/api/auth/sessions/"+aID).cookie(b)), 200)
	wantStatus(t, e.do(get("/api/ping/api").cookie(a)), 401)
	wantStatus(t, e.do(get("/api/ping/api").cookie(b)), 200)
	wantStatus(t, e.do(del("/api/auth/sessions/"+aID).cookie(b)), 404)

	// Revoke all.
	c := loginCookie(t, e)
	rec = e.do(del("/api/auth/sessions").cookie(c))
	wantStatus(t, rec, 200)
	if n := decode[map[string]int](t, rec)["revoked"]; n != 2 {
		t.Errorf("revoked = %d, want 2 (B and C; A was already revoked)", n)
	}
	for _, ck := range []*http.Cookie{b, c} {
		wantStatus(t, e.do(get("/api/ping/api").cookie(ck)), 401)
	}
}

func TestLogoutRevokesSessionAndClearsCookie(t *testing.T) {
	e := newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))
	cookie := loginCookie(t, e)
	rec := e.do(post("/api/auth/logout", nil).cookie(cookie))
	wantStatus(t, rec, 200)
	if c := sessionCookie(t, rec); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout cookie = %+v, want an expired empty cookie", c)
	}
	if ev := e.lastEvent(); ev.Action != "logout" || ev.Principal != "owner" {
		t.Errorf("event = %+v", ev)
	}
	wantStatus(t, e.do(get("/api/ping/api").cookie(cookie)), 401)
	// Logging out without a session is harmless.
	wantStatus(t, e.do(post("/api/auth/logout", nil)), 200)
}

func TestUnknownOrForgedCookieIsNotASession(t *testing.T) {
	e := newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))
	loginCookie(t, e)
	wantStatus(t, e.do(get("/api/ping/api").cookie(&http.Cookie{Name: auth.SessionCookie, Value: "guess"})), 401)
}

func TestSessionStoresOnlyTheHashOfTheCookie(t *testing.T) {
	e := newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))
	cookie := loginCookie(t, e)
	if _, err := e.store.GetSession(context.Background(), cookie.Value); err == nil {
		t.Error("store holds a session keyed by the raw cookie value")
	}
}

func TestWhoami(t *testing.T) {
	e := newEnv(t)
	rec := e.do(get("/api/auth/whoami"))
	wantStatus(t, rec, 200)
	got := decode[struct {
		Authenticated bool
		Source        string
		Principal     struct{ Name string }
		Services      []string
	}](t, rec)
	if !got.Authenticated || got.Source != "stack" || got.Principal.Name != "localhost" || len(got.Services) != 4 {
		t.Errorf("local whoami = %+v", got)
	}

	e = newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))
	if got := decode[map[string]any](t, e.do(get("/api/auth/whoami"))); got["authenticated"] != false {
		t.Errorf("anonymous whoami = %v", got)
	}
	cookie := loginCookie(t, e)
	got2 := decode[map[string]any](t, e.do(get("/api/auth/whoami").cookie(cookie)))
	if got2["authenticated"] != true || got2["source"] != "session" {
		t.Errorf("session whoami = %v", got2)
	}
}

func TestLoginAgainWithValidSessionDoesNotCreateAnother(t *testing.T) {
	e := newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))
	cookie := loginCookie(t, e)
	rec := e.do(post("/api/auth/ui/begin", map[string]string{}).cookie(cookie))
	wantStatus(t, rec, 200)
	if decode[map[string]any](t, rec)["already_authenticated"] != true {
		t.Error("expected already_authenticated")
	}
	list, _ := e.store.ListSessions(context.Background(), e.clock.Now())
	if len(list) != 1 {
		t.Errorf("%d sessions, want 1", len(list))
	}
}

func TestLocalhostTrustLoginEstablishesBrowserSession(t *testing.T) {
	e := newEnv(t)
	rec := e.do(post("/api/auth/ui/begin", map[string]string{}))
	wantStatus(t, rec, 200)
	cookie := sessionCookie(t, rec)
	// Once localhost-trust is gone from the stack the session still works.
	cfg := stackConfig(entry("password", auth.Sufficient))
	cfg.PasswordHash = e.m.Config().PasswordHash
	if err := e.m.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.do(get("/api/ping/api").cookie(cookie)), 200)
	wantStatus(t, e.do(get("/api/ping/api")), 401)
}

// Services and multi-step logins.

// otpModule is a second factor: it challenges, then checks the answer.
func otpModule() auth.Module {
	return scriptedModule{name: "otp", fn: func(r *auth.AuthRequest) auth.Result {
		if !r.Interactive {
			return auth.Ignore("interactive only")
		}
		if r.Step == nil {
			return auth.Challenge(auth.Step{Kind: "otp", Data: []byte(`{"digits":6}`), State: []byte(`"nonce-1"`)})
		}
		if string(r.Step.State) == `"nonce-1"` && string(r.Step.Response) == `"123456"` {
			return auth.Success(auth.Principal{Name: "owner"})
		}
		return auth.Fail("bad code")
	}}
}

func twoFactorEnv(t *testing.T) *env {
	cfg := auth.DefaultConfig()
	cfg.Stacks = map[string][]auth.StackEntry{"default": {entry("password", auth.Required), entry("otp", auth.Required)}}
	return newEnv(t, withConfig(cfg), withModules(otpModule()))
}

func TestMultiStepLoginChallengeAndContinue(t *testing.T) {
	e := twoFactorEnv(t)
	rec := e.do(post("/api/auth/api/begin", map[string]string{"password": testPW}))
	wantStatus(t, rec, 200)
	if len(rec.Result().Cookies()) != 0 {
		t.Error("session cookie issued before the challenge was answered")
	}
	ch := decode[struct {
		Status  string
		LoginID string `json:"login_id"`
		Module  string
		Step    struct {
			Kind  string
			Data  map[string]int
			State any
		}
	}](t, rec)
	if ch.Status != "challenge" || ch.Module != "otp" || ch.Step.Kind != "otp" || ch.Step.Data["digits"] != 6 || ch.LoginID == "" {
		t.Fatalf("challenge = %+v", ch)
	}
	if ch.Step.State != nil {
		t.Error("server-side step state leaked to the client")
	}
	if ev := e.lastEvent(); ev.Outcome != auth.OutcomeChallenge || joinResults(ev) != "password:success,otp:challenge" {
		t.Errorf("begin event = %+v (%s)", ev, joinResults(ev))
	}

	// Wrong answer fails and consumes the exchange.
	wantStatus(t, e.do(post("/api/auth/api/continue", map[string]any{"login_id": ch.LoginID, "response": "000000"})), 401)
	wantStatus(t, e.do(post("/api/auth/api/continue", map[string]any{"login_id": ch.LoginID, "response": "123456"})), 410)

	// Fresh exchange, right answer.
	rec = e.do(post("/api/auth/api/begin", map[string]string{"password": testPW}))
	id := decode[struct {
		LoginID string `json:"login_id"`
	}](t, rec).LoginID
	rec = e.do(post("/api/auth/api/continue", map[string]any{"login_id": id, "response": "123456"}))
	wantStatus(t, rec, 200)
	cookie := sessionCookie(t, rec)
	ev := e.lastEvent()
	if ev.Action != "login_continue" || ev.Outcome != auth.OutcomeSuccess || joinResults(ev) != "password:success,otp:challenge,otp:success" || ev.SessionID == "" {
		t.Errorf("continue event = %+v (%s)", ev, joinResults(ev))
	}
	wantStatus(t, e.do(get("/api/ping/api").cookie(cookie)), 200)
}

func TestPendingLoginIsBoundToServiceHostAndPeerAndExpires(t *testing.T) {
	begin := func(e *env) string {
		rec := e.do(post("/api/auth/api/begin", map[string]string{"password": testPW}))
		return decode[struct {
			LoginID string `json:"login_id"`
		}](t, rec).LoginID
	}
	answer := func(id string) map[string]any { return map[string]any{"login_id": id, "response": "123456"} }

	e := twoFactorEnv(t)
	wantStatus(t, e.do(post("/api/auth/admin/continue", answer(begin(e)))), 410)
	wantStatus(t, e.do(post("/api/auth/api/continue", answer(begin(e))).from("127.0.0.2:5")), 410)
	wantStatus(t, e.do(post("/api/auth/api/continue", answer(begin(e))).on("127.0.0.1:9001")), 410)
	id := begin(e)
	e.clock.Advance(6 * time.Minute)
	wantStatus(t, e.do(post("/api/auth/api/continue", answer(id))), 410)
	wantStatus(t, e.do(post("/api/auth/api/continue", map[string]any{"response": "x"})), 400)
	wantStatus(t, e.do(post("/api/auth/nonsense/begin", nil)), 404)
}

func TestSessionCoversOnlyServicesItsStackGranted(t *testing.T) {
	cfg := auth.DefaultConfig()
	cfg.Stacks = map[string][]auth.StackEntry{
		"basic":  {entry("password", auth.Sufficient)},
		"strict": {entry("password", auth.Required), entry("otp", auth.Required)},
	}
	cfg.Services = map[auth.Service]string{auth.ServiceUI: "basic", auth.ServiceAPI: "basic", auth.ServiceApprove: "basic", auth.ServiceAdmin: "strict"}
	e := newEnv(t, withConfig(cfg), withModules(otpModule()))

	rec := e.do(post("/api/auth/ui/begin", map[string]string{"password": testPW}))
	wantStatus(t, rec, 200)
	cookie := sessionCookie(t, rec)
	if got := decode[struct{ Services []string }](t, rec).Services; len(got) != 3 {
		t.Errorf("services = %v, want ui, api, approve", got)
	}
	wantStatus(t, e.do(get("/api/ping/approve").cookie(cookie)), 200)
	rec = e.do(get("/api/ping/admin").cookie(cookie))
	wantStatus(t, rec, 403)
	if decode[map[string]any](t, rec)["type"] != "urn:hal9001:problem:forbidden" {
		t.Errorf("body = %s", rec.Body)
	}
	wantStatus(t, e.do(get("/api/ping/admin")), 401)

	// Stepping up through admin's own stack extends the same session.
	rec = e.do(post("/api/auth/admin/begin", map[string]string{"password": testPW}).cookie(cookie))
	id := decode[struct {
		LoginID string `json:"login_id"`
	}](t, rec).LoginID
	rec = e.do(post("/api/auth/admin/continue", map[string]any{"login_id": id, "response": "123456"}).cookie(cookie))
	wantStatus(t, rec, 200)
	wantStatus(t, e.do(get("/api/ping/admin").cookie(cookie)), 200)
	wantStatus(t, e.do(get("/api/ping/ui").cookie(cookie)), 200)
	if list, _ := e.store.ListSessions(context.Background(), e.clock.Now()); len(list) != 1 {
		t.Errorf("%d sessions, want 1 (admin stack extended the existing session)", len(list))
	}
}

func TestAdminEndpointsRequireAdmin(t *testing.T) {
	e := newEnv(t, withConfig(stackConfig(entry("password", auth.Sufficient))))
	for _, r := range []*req{get("/api/auth/sessions"), del("/api/auth/sessions"), get("/api/auth/events"), get("/api/auth/webauthn/credentials")} {
		wantStatus(t, e.do(r), 401)
	}
	cookie := loginCookie(t, e)
	rec := e.do(get("/api/auth/events?limit=2").cookie(cookie))
	wantStatus(t, rec, 200)
	if n := len(decode[struct{ Events []auth.AuthEvent }](t, rec).Events); n != 2 {
		t.Errorf("events = %d, want limit 2", n)
	}
	wantStatus(t, e.do(get("/api/auth/events?limit=0").cookie(cookie)), 400)
}

func TestConfigValidation(t *testing.T) {
	cases := map[string]func(*auth.Config){
		"unknown module":  func(c *auth.Config) { c.Stacks["default"][0].Module = "kerberos" },
		"bad control":     func(c *auth.Config) { c.Stacks["default"][0].Control = "maybe" },
		"missing service": func(c *auth.Config) { delete(c.Services, auth.ServiceAdmin) },
		"undefined stack": func(c *auth.Config) { c.Services[auth.ServiceAPI] = "ghost" },
		"unknown service": func(c *auth.Config) { c.Services["root"] = "default" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := auth.DefaultConfig()
			mutate(&c)
			if _, err := auth.New(auth.Options{Config: c, Store: auth.NewMemoryStore()}); err == nil {
				t.Error("invalid config accepted")
			}
		})
	}
	e := newEnv(t)
	bad := auth.DefaultConfig()
	bad.Services[auth.ServiceAPI] = "ghost"
	if err := e.m.Reload(bad); err == nil {
		t.Error("Reload accepted an invalid config")
	}
	wantStatus(t, e.do(get("/api/ping/api")), 200) // old config stays active
}

func TestDefaultStackIncludesTailscaleIdentityAndFourModules(t *testing.T) {
	var names []string
	for _, e := range auth.DefaultConfig().Stacks["default"] {
		names = append(names, e.Module+":"+string(e.Control))
	}
	if got := strings.Join(names, " "); got != "localhost-trust:sufficient tailscale-identity:sufficient webauthn:sufficient password:sufficient" {
		t.Errorf("default stack = %s", got)
	}
}
