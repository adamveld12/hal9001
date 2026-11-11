package e2e

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"hal9001/internal/cli"
)

const (
	authPassword = "correct horse battery staple"
	tsHost       = "mac.tail1234.ts.net"
	tsOwner      = "me@example.com"
)

// authConfig sets the daemon's config.yaml (an `auth:` section and friends).
// With a password, the argon2id hash is installed the way a user does it, with
// `hal9001 auth set-password --stdin`, on a scratch copy of the config; Start
// rewrites config.yaml from d.Config, so the hashed text is what gets set.
func (d *Daemon) authConfig(cfg, password string) {
	d.t.Helper()
	if password != "" {
		scratch := d.t.TempDir()
		path := filepath.Join(scratch, "config.yaml")
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			d.t.Fatal(err)
		}
		var outb, errb bytes.Buffer
		code := cli.Run(context.Background(), []string{"auth", "set-password", "--stdin", "--home", scratch}, cli.Env{
			Stdin: strings.NewReader(password + "\n"), Stdout: &outb, Stderr: &errb, Version: "e2e",
		})
		if out, errOut := outb.String(), errb.String(); code != 0 {
			d.t.Fatalf("auth set-password: exit %d\n%s\n%s", code, out, errOut)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			d.t.Fatal(err)
		}
		if !strings.Contains(string(b), "$argon2id$") {
			d.t.Fatalf("set-password stored no argon2id hash:\n%s", b)
		}
		cfg = string(b)
	}
	d.Config(cfg)
}

type authTrace struct {
	Module  string `json:"module"`
	Control string `json:"control"`
	Result  string `json:"result"`
	Reason  string `json:"reason"`
}

type authEvent struct {
	Action    string      `json:"action"`
	Service   string      `json:"service"`
	Endpoint  string      `json:"endpoint"`
	Modules   []authTrace `json:"modules"`
	Outcome   string      `json:"outcome"`
	Reason    string      `json:"reason"`
	Principal string      `json:"principal"`
	Host      string      `json:"host"`
	SessionID string      `json:"session_id"`
}

// AuthEvents reads GET /api/auth/events (admin), newest first.
func (d *Daemon) AuthEvents(opts ...ReqOpt) []authEvent {
	d.t.Helper()
	var l struct {
		Events []authEvent `json:"events"`
	}
	d.Get("/api/auth/events?limit=1000", opts...).Want(200).JSON(&l)
	return l.Events
}

// trace returns the module trace entry named module, if the event has one.
func (e authEvent) trace(module string) (authTrace, bool) {
	for _, m := range e.Modules {
		if m.Module == module {
			return m, true
		}
	}
	return authTrace{}, false
}

func (e authEvent) modules() string {
	var parts []string
	for _, m := range e.Modules {
		parts = append(parts, m.Module+":"+m.Result)
	}
	return strings.Join(parts, ",")
}

type whoAmI struct {
	Authenticated bool                  `json:"authenticated"`
	Source        string                `json:"source"`
	Principal     struct{ Name string } `json:"principal"`
	Modules       []string              `json:"modules"`
	Services      []string              `json:"services"`
}

func (d *Daemon) whoami(opts ...ReqOpt) whoAmI {
	d.t.Helper()
	var w whoAmI
	d.Get("/api/auth/whoami", opts...).Want(200).JSON(&w)
	return w
}

// login posts a password to /api/auth/{service}/begin.
func (d *Daemon) login(service, password string, opts ...ReqOpt) Resp {
	d.t.Helper()
	body := map[string]any{}
	if password != "" {
		body["password"] = password
	}
	return d.Post("/api/auth/"+service+"/begin", body, opts...)
}

// sessionCookie returns the session cookie a response sets.
func sessionCookie(r Resp) *http.Cookie {
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == "hal9001_session" {
			return c
		}
	}
	return nil
}

func cookieHeader(c *http.Cookie) string { return c.Name + "=" + c.Value }

// Scenario 21, default config: a local request is accepted by localhost-trust
// with no login.
func TestScenario21DefaultConfigLocalhostTrust(t *testing.T) {
	d := newDaemon(t)
	d.Start()

	d.Get("/api/status").Want(200)
	w := d.whoami()
	if !w.Authenticated || w.Source != "stack" || w.Principal.Name != "localhost" || !slices.Equal(w.Modules, []string{"localhost-trust"}) ||
		!slices.Equal(w.Services, []string{"ui", "api", "approve", "admin"}) {
		t.Errorf("whoami: %+v", w)
	}
	var found bool
	for _, e := range d.AuthEvents() {
		if e.Endpoint == "GET /api/status" && e.Service == "api" {
			tr, ok := e.trace("localhost-trust")
			found = ok && tr.Result == "success" && e.Outcome == "success" && e.Principal == "localhost"
		}
	}
	if !found {
		t.Error("no auth event shows localhost-trust succeeding for GET /api/status")
	}
}

// Scenario 21, password: with localhost-trust in no stack, API calls get 401;
// a password login yields a session cookie that works; a wrong password is
// 401 authentication-failed; logout and revocation end sessions.
func TestScenario21PasswordLoginSessions(t *testing.T) {
	d := newDaemon(t)
	d.authConfig(`auth:
  stacks:
    pw: [{module: password, control: sufficient}]
  services: {ui: pw, api: pw, approve: pw, admin: pw}
`, authPassword)
	d.IdleSensor("src")
	d.Start()

	// No localhost-trust: no API endpoint is reachable without a session (the UI
	// shell is public so the login page loads).
	for _, path := range []string{"/api/status", "/api/events", "/api/auth/events"} {
		r := d.Get(path)
		if r.Status != 401 || r.Problem() != "unauthenticated" || r.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("GET %s without login: %s %v", path, r, r.Header)
		}
	}
	if w := d.whoami(); w.Authenticated {
		t.Errorf("whoami without login: %+v", w)
	}

	// Wrong or missing password: 401, no cookie.
	for _, pw := range []string{"wrong password", ""} {
		r := d.login("ui", pw)
		if r.Status != 401 || r.Problem() != "authentication-failed" || sessionCookie(r) != nil {
			t.Errorf("login with %q: %s %v", pw, r, r.Header)
		}
	}

	// Right password: a session cookie, HttpOnly and SameSite=Strict.
	r := d.login("ui", authPassword).Want(200)
	c1 := sessionCookie(r)
	if c1 == nil || !c1.HttpOnly || c1.SameSite != http.SameSiteStrictMode || c1.Path != "/" || c1.Secure || c1.MaxAge <= 0 {
		t.Fatalf("session cookie: %+v (%v)", c1, r.Header["Set-Cookie"])
	}
	var res struct {
		Status    string `json:"status"`
		Principal struct{ Name string }
		Services  []string `json:"services"`
	}
	r.JSON(&res)
	if res.Status != "success" || res.Principal.Name != "owner" || len(res.Services) != 4 {
		t.Errorf("login result: %s", r)
	}
	if strings.Contains(string(r.Body), authPassword) {
		t.Error("login response echoes the password")
	}

	d.Cookie = cookieHeader(c1)
	d.Get("/api/status").Want(200)
	d.Get("/api/auth/events").Want(200)
	if w := d.whoami(); !w.Authenticated || w.Source != "session" || w.Principal.Name != "owner" || len(w.Services) != 4 {
		t.Errorf("whoami with session: %+v", w)
	}
	if r := d.Get("/api/status", withoutCookie(), withHeader("Cookie", "hal9001_session=not-a-session")); r.Status != 401 {
		t.Errorf("forged cookie: %s", r)
	}

	// A second login (without the first cookie) makes a second session;
	// revoking the first leaves the second working.
	r2 := d.login("api", authPassword, withoutCookie()).Want(200)
	c2 := sessionCookie(r2)
	if c2 == nil || c2.Value == c1.Value {
		t.Fatalf("second login cookie: %+v", c2)
	}
	var sessions struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
		Current string `json:"current"`
	}
	d.Get("/api/auth/sessions").Want(200).JSON(&sessions)
	if len(sessions.Sessions) != 2 {
		t.Fatalf("sessions: %s", d.Get("/api/auth/sessions").Body)
	}
	first := sessions.Current
	d.Cookie = cookieHeader(c2)
	d.Get("/api/auth/sessions").Want(200).JSON(&sessions)
	if sessions.Current == first || sessions.Current == "" {
		t.Errorf("current session id %q equals the other session %q", sessions.Current, first)
	}
	d.Delete("/api/auth/sessions/" + first).Want(200)
	d.Get("/api/status", withoutCookie(), withHeader("Cookie", cookieHeader(c1))).Want(401)
	d.Get("/api/status").Want(200)
	d.Delete("/api/auth/sessions/" + first).Want(404)

	// Logout revokes the session and clears the cookie.
	lo := d.Post("/api/auth/logout", nil).Want(200)
	if cleared := sessionCookie(lo); cleared == nil || cleared.MaxAge >= 0 {
		t.Errorf("logout cookie: %+v", cleared)
	}
	d.Get("/api/status").Want(401)

	// Revoke all, from a fresh session.
	c3 := sessionCookie(d.login("ui", authPassword, withoutCookie()).Want(200))
	d.Cookie = cookieHeader(c3)
	d.Get("/api/status").Want(200)
	var revoked struct {
		Revoked int `json:"revoked"`
	}
	d.Delete("/api/auth/sessions").Want(200).JSON(&revoked)
	if revoked.Revoked < 1 {
		t.Errorf("revoke all: %+v", revoked)
	}
	d.Get("/api/status").Want(401)

	// The audit trail (read with a new admin session) records each step.
	d.Cookie = cookieHeader(sessionCookie(d.login("admin", authPassword, withoutCookie()).Want(200)))
	var wrong, ok, denied, logout, revokedEv bool
	for _, e := range d.AuthEvents() {
		switch {
		case e.Action == "login_begin" && e.Outcome == "fail":
			tr, has := e.trace("password")
			wrong = wrong || (has && tr.Result == "fail" && tr.Reason == "wrong password") || (has && tr.Result == "ignore")
		case e.Action == "login_begin" && e.Outcome == "success":
			ok = ok || e.Principal == "owner" && e.SessionID != ""
		case e.Action == "request" && e.Outcome == "fail" && e.Endpoint == "GET /api/status":
			denied = true
		case e.Action == "logout":
			logout = true
		case e.Action == "session_revoke" || e.Action == "session_revoke_all":
			revokedEv = true
		}
	}
	if !wrong || !ok || !denied || !logout || !revokedEv {
		t.Errorf("auth events incomplete: failed login %v, login %v, denied request %v, logout %v, revoke %v", wrong, ok, denied, logout, revokedEv)
	}
}

// Scenario 21, requisite: a failed requisite module ends evaluation at once,
// so modules after it are not evaluated (and cannot grant access).
func TestScenario21RequisiteFailureStopsEvaluation(t *testing.T) {
	d := newDaemon(t)
	d.authConfig(`auth:
  stacks:
    strict:
      - {module: password, control: requisite}
      - {module: localhost-trust, control: sufficient}
    open: [{module: localhost-trust, control: sufficient}]
  services: {ui: strict, api: open, approve: open, admin: open}
`, authPassword)
	d.Start()

	r := d.login("ui", "wrong password")
	if r.Status != 401 || r.Problem() != "authentication-failed" || sessionCookie(r) != nil {
		t.Fatalf("login with the wrong password: %s", r)
	}
	var body struct {
		Trace []struct{ Module, Result string } `json:"trace"`
	}
	r.JSON(&body)
	if len(body.Trace) != 1 || body.Trace[0].Module != "password" || body.Trace[0].Result != "fail" {
		t.Errorf("response trace: %+v", body.Trace)
	}

	var ev *authEvent
	for _, e := range d.AuthEvents() {
		if e.Action == "login_begin" && e.Service == "ui" {
			ev = &e
			break
		}
	}
	if ev == nil {
		t.Fatal("no login_begin auth event for ui")
	}
	if ev.Outcome != "fail" || ev.modules() != "password:fail" || !strings.Contains(ev.Reason, "requisite module password failed") {
		t.Errorf("event: outcome %s modules %s reason %q", ev.Outcome, ev.modules(), ev.Reason)
	}
	if _, evaluated := ev.trace("localhost-trust"); evaluated {
		t.Error("localhost-trust was evaluated after the requisite failure")
	}

	// With the right password the requisite passes and evaluation continues to
	// the sufficient module, which also succeeds: both are in the trace.
	d.login("ui", authPassword).Want(200)
	for _, e := range d.AuthEvents() {
		if e.Action == "login_begin" && e.Service == "ui" && e.Outcome == "success" {
			if e.modules() != "password:success,localhost-trust:success" {
				t.Errorf("success trace: %s", e.modules())
			}
			return
		}
	}
	t.Error("no successful login_begin event")
}

// Scenario 21, required: a failed required module is remembered; a later
// sufficient success does not grant access. The trace shows both modules.
func TestScenario21RequiredFailureOverridesSufficient(t *testing.T) {
	d := newDaemon(t)
	d.authConfig(`auth:
  stacks:
    both:
      - {module: password, control: required}
      - {module: localhost-trust, control: sufficient}
    open: [{module: localhost-trust, control: sufficient}]
  services: {ui: both, api: open, approve: open, admin: open}
`, authPassword)
	d.Start()

	r := d.login("ui", "wrong password")
	if r.Status != 401 || r.Problem() != "authentication-failed" || sessionCookie(r) != nil {
		t.Fatalf("login with a failing required module: %s", r)
	}
	var ev *authEvent
	for _, e := range d.AuthEvents() {
		if e.Action == "login_begin" && e.Service == "ui" {
			ev = &e
			break
		}
	}
	if ev == nil {
		t.Fatal("no login_begin auth event for ui")
	}
	pw, _ := ev.trace("password")
	lt, _ := ev.trace("localhost-trust")
	if ev.Outcome != "fail" || pw.Result != "fail" || lt.Result != "success" || ev.modules() != "password:fail,localhost-trust:success" || ev.Principal != "" {
		t.Errorf("event: outcome %s modules %s principal %q reason %q", ev.Outcome, ev.modules(), ev.Principal, ev.Reason)
	}
	if !strings.Contains(ev.Reason, "required module password failed") {
		t.Errorf("reason: %q", ev.Reason)
	}
}

// Scenario 22: localhost-trust accepts a plain local request and nothing
// that looks proxied or non-local, each condition on its own.
func TestScenario22LocalhostTrustRejectsProxiedRequests(t *testing.T) {
	d := newDaemon(t)
	d.authConfig(`allowed_hosts: [`+tsHost+`]
auth:
  stacks:
    local: [{module: localhost-trust, control: sufficient}]
  services: {ui: local, api: local, approve: local, admin: local}
`, "")
	d.Start()

	d.Get("/api/status").Want(200)
	if w := d.whoami(); !w.Authenticated || w.Principal.Name != "localhost" {
		t.Fatalf("plain local whoami: %+v", w)
	}
	d.Get("/api/status", withHost("localhost:"+strconv.Itoa(d.Port()))).Want(200)

	cases := []struct {
		name string
		opts []ReqOpt
		want string // substring of the localhost-trust ignore reason
	}{
		{"X-Forwarded-For", []ReqOpt{withHeader("X-Forwarded-For", "100.64.0.7")}, "proxy header present: X-Forwarded-For"},
		{"X-Forwarded-Proto", []ReqOpt{withHeader("X-Forwarded-Proto", "https")}, "proxy header present: X-Forwarded-Proto"},
		{"Forwarded", []ReqOpt{withHeader("Forwarded", "for=100.64.0.7;proto=https")}, "proxy header present: Forwarded"},
		{"Tailscale-User-Login", []ReqOpt{withHeader("Tailscale-User-Login", tsOwner)}, "proxy header present: Tailscale-User-Login"},
		{"allowed non-local Host", []ReqOpt{withHost(tsHost)}, "Host is not localhost"},
		{"serve-style request", []ReqOpt{withHost(tsHost), withHeader("X-Forwarded-For", "100.64.0.7"), withHeader("Tailscale-User-Login", tsOwner)}, "Host is not localhost"},
	}
	for _, c := range cases {
		r := d.Get("/api/status", c.opts...)
		if r.Status != 401 || r.Problem() != "unauthenticated" {
			t.Errorf("%s: %s", c.name, r)
		}
		if w := d.whoami(c.opts...); w.Authenticated {
			t.Errorf("%s: whoami authenticated: %+v", c.name, w)
		}
	}

	// A plain local request reads the audit trail: each rejection shows
	// localhost-trust ignoring the request.
	events := d.AuthEvents()
	for _, c := range cases {
		var seen bool
		for _, e := range events {
			if e.Endpoint != "GET /api/status" || e.Outcome != "fail" {
				continue
			}
			if tr, ok := e.trace("localhost-trust"); ok && tr.Result == "ignore" && strings.Contains(tr.Reason, c.want) {
				seen = true
			}
		}
		if !seen {
			t.Errorf("%s: no auth event shows localhost-trust ignoring with %q", c.name, c.want)
		}
	}
	for _, e := range events {
		if e.Outcome == "success" && e.Host != "" && e.Host != d.Addr() && !strings.HasPrefix(e.Host, "localhost:") {
			t.Errorf("non-local Host accepted: %+v", e)
		}
	}
}

// tsDaemon prepares a daemon with a fake `tailscale` CLI. The fake answers from
// files under <home>/ts: whois-<ip>.json, status.json, and `down` (when the
// file exists, every command fails as if tailscaled were not running). The
// admin service is guarded by localhost-trust so the tests can read the audit
// trail; ui, api and approve use the default stack without localhost-trust.
func tsDaemon(t *testing.T, allowedLogins string) *Daemon {
	t.Helper()
	d := newDaemon(t)
	dir := d.Path("ts")
	d.Write("ts/status.json", `{"BackendState":"Running","Self":{"HostName":"mac","DNSName":"`+tsHost+`.","UserID":42},"User":{"42":{"LoginName":"`+tsOwner+`"},"43":{"LoginName":"other@example.com"}}}`)
	d.Write("ts/whois-100.64.0.7.json", `{"Node":{"Name":"phone.tail1234.ts.net.","Tags":null},"UserProfile":{"LoginName":"`+tsOwner+`"}}`)
	d.Write("ts/whois-100.64.0.8.json", `{"Node":{"Name":"laptop.tail1234.ts.net.","Tags":null},"UserProfile":{"LoginName":"other@example.com"}}`)
	d.Write("ts/whois-100.64.0.9.json", `{"Node":{"Name":"server.tail1234.ts.net.","Tags":["tag:server"]}}`)
	d.Write("ts/whois-100.64.0.10.json", `{"Node":{"Name":"phone2.tail1234.ts.net.","Tags":null},"UserProfile":{"LoginName":"`+tsOwner+`"}}`)
	d.Script("tailscale", `dir=`+shq(dir)+`
if [ -f "$dir/down" ]; then echo "failed to connect to local tailscaled; it doesn't appear to be running" >&2; exit 1; fi
case "$1" in
  whois) f="$dir/whois-$3.json"; if [ -f "$f" ]; then cat "$f"; else echo "no match for IP $3" >&2; exit 1; fi ;;
  status) cat "$dir/status.json" ;;
  serve) echo '{}' ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
esac
`)
	d.authConfig("allowed_hosts: ["+tsHost+"]\n"+allowedLogins+`auth:
  stacks:
    local: [{module: localhost-trust, control: sufficient}]
    remote:
      - {module: tailscale-identity, control: sufficient}
      - {module: webauthn, control: sufficient}
      - {module: password, control: sufficient}
  services: {ui: remote, api: remote, approve: remote, admin: local}
`, "")
	return d
}

// tsGet sends GET /api/status as tailscale serve would: Host is the ts.net
// name, X-Forwarded-For the tailnet client IP, Tailscale-User-Login the login.
func (d *Daemon) tsGet(ip, login string) Resp {
	d.t.Helper()
	opts := []ReqOpt{withHost(tsHost)}
	if ip != "" {
		opts = append(opts, withHeader("X-Forwarded-For", ip))
	}
	if login != "" {
		opts = append(opts, withHeader("Tailscale-User-Login", login))
	}
	return d.Get("/api/status", opts...)
}

// tsEvent returns the newest api-service auth event whose tailscale-identity
// result has the given result and a reason containing reason.
func tsEvent(events []authEvent, result, reason string) (authEvent, bool) {
	for _, e := range events {
		if e.Service != "api" || e.Endpoint != "GET /api/status" {
			continue
		}
		if tr, ok := e.trace("tailscale-identity"); ok && tr.Result == result && strings.Contains(tr.Reason, reason) {
			return e, true
		}
	}
	return authEvent{}, false
}

// Scenario 24: tailscale-identity against a fake whois, with allowed_logins.
func TestScenario24TailscaleIdentity(t *testing.T) {
	d := tsDaemon(t, "allowed_logins: ["+tsOwner+"]\n")
	d.Start()

	// Header login equals whois and is allowed: signed in as that login.
	d.tsGet("100.64.0.7", tsOwner).Want(200)
	w := d.whoami(withHost(tsHost), withHeader("X-Forwarded-For", "100.64.0.7"), withHeader("Tailscale-User-Login", tsOwner))
	if !w.Authenticated || w.Source != "stack" || w.Principal.Name != tsOwner || !slices.Equal(w.Modules, []string{"tailscale-identity"}) {
		t.Errorf("whoami through serve: %+v", w)
	}
	// Login comparison is case-insensitive on the email.
	d.tsGet("100.64.0.7", strings.ToUpper(tsOwner)).Want(200)

	type deny struct {
		name      string
		r         Resp
		result    string
		reasonHas string
	}
	denials := []deny{
		{"header login differs from whois", d.tsGet("100.64.0.7", "other@example.com"), "fail", "does not match whois login"},
		{"login not in allowed_logins", d.tsGet("100.64.0.8", "other@example.com"), "fail", "is not in allowed_logins"},
		{"tagged device", d.tsGet("100.64.0.9", tsOwner), "fail", "tagged device"},
		{"unknown tailnet IP", d.tsGet("100.64.0.99", tsOwner), "fail", "whois 100.64.0.99"},
		{"no X-Forwarded-For", d.tsGet("", tsOwner), "fail", "missing X-Forwarded-For"},
		{"no Tailscale-User-Login header", d.tsGet("100.64.0.7", ""), "ignore", "no Tailscale-User-Login header"},
		{"localhost Host", d.Get("/api/status", withHeader("X-Forwarded-For", "100.64.0.7"), withHeader("Tailscale-User-Login", tsOwner)), "ignore", "Host is not an allowed ts.net hostname"},
	}
	for _, c := range denials {
		if c.r.Status != 401 || c.r.Problem() != "unauthenticated" {
			t.Errorf("%s: %s", c.name, c.r)
		}
	}
	// Admin is localhost-trust: read the trail with a plain local request.
	events := d.AuthEvents()
	for _, c := range denials {
		e, ok := tsEvent(events, c.result, c.reasonHas)
		if !ok {
			t.Errorf("%s: no auth event with tailscale-identity %s / %q", c.name, c.result, c.reasonHas)
			continue
		}
		if e.Outcome != "fail" || e.Principal != "" {
			t.Errorf("%s: event %+v", c.name, e)
		}
	}
	if e, ok := tsEvent(events, "success", ""); !ok || e.Principal != tsOwner || e.Host != tsHost {
		t.Errorf("success event: %+v %v", e, ok)
	}

	// GET /api/tailscale and the CLI report the fake's state.
	var rep tsReport
	d.Get("/api/tailscale").Want(200).JSON(&rep)
	if !rep.CLI.Found || rep.BackendState != "Running" || rep.DNSName != tsHost || rep.OwnerLogin != tsOwner || !rep.AllowedHost.Present ||
		rep.Serve.PointsAtHal || rep.Serve.Funnel || !slices.Equal(rep.AllowedLogins, []string{tsOwner}) {
		t.Errorf("tailscale report: %+v", rep)
	}
	d.sameAsAPI("tailscale status", d.cliJSON("", "tailscale", "status"), "/api/tailscale")
}

// tsReport is the part of GET /api/tailscale the auth scenarios check.
type tsReport struct {
	CLI struct {
		Found bool `json:"found"`
	} `json:"cli"`
	BackendState string `json:"backend_state"`
	DNSName      string `json:"dns_name"`
	OwnerLogin   string `json:"owner_login"`
	AllowedHost  struct {
		Present bool `json:"present"`
	} `json:"allowed_host"`
	Serve struct {
		PointsAtHal bool `json:"points_at_hal"`
		Funnel      bool `json:"funnel"`
	} `json:"serve"`
	AllowedLogins []string `json:"allowed_logins"`
	Error         string   `json:"error"`
}

// Scenario 24: with allowed_logins empty, the owner of this Mac (from
// `tailscale status`) is the only login accepted.
func TestScenario24TailscaleDefaultAllowedLoginIsOwner(t *testing.T) {
	d := tsDaemon(t, "")
	d.Start()

	d.tsGet("100.64.0.7", tsOwner).Want(200)
	if r := d.tsGet("100.64.0.8", "other@example.com"); r.Status != 401 {
		t.Errorf("a non-owner login was accepted: %s", r)
	}
	if _, ok := tsEvent(d.AuthEvents(), "fail", "is not in allowed_logins"); !ok {
		t.Error("no auth event for the non-owner login")
	}
	var rep tsReport
	d.Get("/api/tailscale").Want(200).JSON(&rep)
	if rep.OwnerLogin != tsOwner || !slices.Equal(rep.AllowedLogins, []string{tsOwner}) {
		t.Errorf("report: %+v", rep)
	}
}

// Scenario 24: when tailscaled is unreachable the module ignores the request
// (it neither grants nor denies) and the auth event says why.
func TestScenario24TailscaleUnavailableIsIgnored(t *testing.T) {
	d := tsDaemon(t, "allowed_logins: ["+tsOwner+"]\n")
	d.Start()
	d.Write("ts/down", "")

	if r := d.tsGet("100.64.0.10", tsOwner); r.Status != 401 || r.Problem() != "unauthenticated" {
		t.Fatalf("request with tailscaled down: %s", r)
	}
	e, ok := tsEvent(d.AuthEvents(), "ignore", "unavailable")
	if !ok || e.Outcome != "fail" || !strings.Contains(e.Reason, "no module succeeded") {
		t.Errorf("auth event: %+v %v", e, ok)
	}
	var rep tsReport
	d.Get("/api/tailscale").Want(200).JSON(&rep)
	if rep.BackendState != "" || !strings.Contains(rep.Error, "unavailable") {
		t.Errorf("report with tailscaled down: %+v", rep)
	}
	out, _, code := d.CLI("", "tailscale", "status")
	if code != 0 || !strings.Contains(out, `"logged_in": false`) && !strings.Contains(out, `"logged_in":false`) {
		t.Errorf("CLI tailscale status: exit %d %s", code, out)
	}

	// Back up: the same client IP, never cached while down, now signs in.
	if err := os.Remove(d.Path("ts/down")); err != nil {
		t.Fatal(err)
	}
	d.tsGet("100.64.0.10", tsOwner).Want(200)
}

// Scenario 24: with no tailscale CLI at all the module ignores the request.
// The harness looks for tailscale only in the daemon's Bin directory, so removing
// the fake leaves none whatever is installed on the machine.
func TestScenario24TailscaleCLIMissingIsIgnored(t *testing.T) {
	d := tsDaemon(t, "allowed_logins: ["+tsOwner+"]\n")
	if err := os.Remove(filepath.Join(d.Bin, "tailscale")); err != nil {
		t.Fatal(err)
	}
	d.Start()

	if r := d.tsGet("100.64.0.7", tsOwner); r.Status != 401 {
		t.Fatalf("request without a tailscale CLI: %s", r)
	}
	if e, ok := tsEvent(d.AuthEvents(), "ignore", "unavailable"); !ok || e.Outcome != "fail" {
		t.Errorf("auth event: %+v %v", e, ok)
	}
	var rep tsReport
	d.Get("/api/tailscale").Want(200).JSON(&rep)
	if rep.CLI.Found {
		t.Error("report claims the tailscale CLI was found")
	}
}
