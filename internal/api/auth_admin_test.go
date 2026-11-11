package api

import (
	"context"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/auth"
	"hal9001/internal/authcfg"
	"hal9001/internal/spec"
	"hal9001/internal/tailscale"
)

const (
	tailnetHost = "alices-macbook.tail1234.ts.net"
	testPW      = "correct horse battery staple"
	newPW       = "a brand new passphrase"
)

var (
	pwHashOnce sync.Once
	pwHash     string
)

// testPasswordHash hashes testPW once: argon2id is deliberately slow.
func testPasswordHash(t *testing.T) string {
	t.Helper()
	pwHashOnce.Do(func() {
		h, err := auth.HashPassword(testPW)
		if err != nil {
			t.Fatal(err)
		}
		pwHash = h
	})
	return pwHash
}

// fakeTailnet answers whois and status like a Tailscale where alice owns this Mac.
type fakeTailnet struct {
	who tailscale.Whois
	st  tailscale.Status
}

func newFakeTailnet(t *testing.T) fakeTailnet {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "tailscale", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	who, err := tailscale.ParseWhois(read("whois_user.json"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := tailscale.ParseStatus(read("status.json"))
	if err != nil {
		t.Fatal(err)
	}
	return fakeTailnet{who: who, st: st}
}

func (f fakeTailnet) Whois(context.Context, netip.Addr) (tailscale.Whois, error) { return f.who, nil }
func (f fakeTailnet) Status(context.Context) (tailscale.Status, error)           { return f.st, nil }
func (f fakeTailnet) ServeStatus(context.Context) (tailscale.ServeConfig, error) {
	return tailscale.ServeConfig{}, nil
}

// authEnv is an API whose auth manager can see a fake Tailscale.
type authEnv struct{ *env }

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	ts := newFakeTailnet(t)
	return &authEnv{newEnv(t, func(eo *envOptions) { eo.authTailscale = ts })}
}

// remote makes a request look like it came over the tailnet through `tailscale serve`.
func remote(r *http.Request) {
	r.Host = tailnetHost
	r.Header.Set("X-Forwarded-For", "100.101.102.103")
}

// asTailnetUser adds the identity headers `tailscale serve` sets.
func asTailnetUser(login string) func(*http.Request) {
	return func(r *http.Request) {
		remote(r)
		r.Header.Set("Tailscale-User-Login", login)
	}
}

// remoteWith is a request from another device holding session cookie c.
func remoteWith(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { remote(r); r.AddCookie(c) }
}

func (e *authEnv) send(method, path string, body any, mods ...func(*http.Request)) *resp {
	e.t.Helper()
	req := request(method, path, encodeBody(e.t, body))
	for _, m := range mods {
		m(req)
	}
	return serve(e.t, e.srv, req)
}

func (e *authEnv) configText() string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, "config.yaml"))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// editConfig changes config.yaml like a hand edit and hands the result to the auth manager.
func (e *authEnv) editConfig(edit func(root *yaml.Node) (bool, error)) {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.home, "config.yaml"))
	if err != nil {
		e.t.Fatal(err)
	}
	out, _, err := spec.EditConfigYAML(raw, edit)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.home, "config.yaml"), out, 0o600); err != nil {
		e.t.Fatal(err)
	}
	cfg, ds := spec.CheckConfig(out)
	if cfg == nil || spec.HasErrors(ds) {
		e.t.Fatalf("config.yaml invalid: %v", ds)
	}
	if err := e.mgr.Reload(authcfg.AuthConfig(cfg)); err != nil {
		e.t.Fatal(err)
	}
}

// withPassword stores testPW in config.yaml as `hal9001 auth set-password` would.
func (e *authEnv) withPassword() {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.home, "config.yaml"))
	if err != nil {
		e.t.Fatal(err)
	}
	out, _, err := authcfg.SetPassword(raw, testPasswordHash(e.t))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.home, "config.yaml"), out, 0o600); err != nil {
		e.t.Fatal(err)
	}
	cfg, _ := spec.CheckConfig(out)
	if err := e.mgr.Reload(authcfg.AuthConfig(cfg)); err != nil {
		e.t.Fatal(err)
	}
}

// loginRemote signs in from another device with the password and returns the session cookie.
func (e *authEnv) loginRemote(pw string) *http.Cookie {
	e.t.Helper()
	r := e.send("POST", "/api/auth/admin/begin", map[string]any{"method": "password", "password": pw}, remote).want(200)
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	e.t.Fatal("login set no session cookie")
	return nil
}

func (e *authEnv) overview(mods ...func(*http.Request)) overviewBody {
	e.t.Helper()
	var ov overviewBody
	e.send("GET", "/api/auth/overview", nil, mods...).want(200).decode(&ov)
	return ov
}

type overviewBody struct {
	Methods []struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Enabled bool   `json:"enabled"`
		Custom  bool   `json:"custom"`
		Detail  string `json:"detail"`
	} `json:"methods"`
	Current struct {
		Method    *string  `json:"method"`
		Modules   []string `json:"modules"`
		Remote    bool     `json:"remote"`
		Source    string   `json:"source"`
		Principal string   `json:"principal"`
		SessionID *string  `json:"session_id"`
	} `json:"current"`
	Password struct {
		Set        bool    `json:"set"`
		Enabled    bool    `json:"enabled"`
		LastUsedAt *string `json:"last_used_at"`
	} `json:"password"`
	PasskeyCount  int      `json:"passkey_count"`
	AllowedLogins []string `json:"allowed_logins"`
}

func (o overviewBody) method(id string) (state string, enabled bool) {
	for _, m := range o.Methods {
		if m.ID == id {
			return m.State, m.Enabled
		}
	}
	return "", false
}

type changeBody struct {
	Overview      overviewBody `json:"overview"`
	EndedSessions int          `json:"ended_sessions"`
	Notice        *string      `json:"notice"`
}

func stacksReq(modules ...string) map[string]any {
	entries := make([]map[string]any, len(modules))
	for i, m := range modules {
		entries[i] = map[string]any{"module": m, "control": "sufficient"}
	}
	return map[string]any{
		"stacks":   map[string]any{"default": entries},
		"services": map[string]string{"ui": "default", "api": "default", "approve": "default", "admin": "default"},
	}
}

// keepHash makes the password entries of a stacks request keep the stored hash, like a client that sends back what it read.
func keepHash(req map[string]any) map[string]any {
	for _, entries := range req["stacks"].(map[string]any) {
		for _, en := range entries.([]map[string]any) {
			if en["module"] == "password" {
				en["options"] = map[string]any{"hash": "«redacted»"}
			}
		}
	}
	return req
}

func (r *resp) diagnosticCodes() []string {
	r.t.Helper()
	var p struct {
		Diagnostics []struct {
			Code string `json:"code"`
			Path string `json:"path"`
		} `json:"diagnostics"`
	}
	r.decode(&p)
	var out []string
	for _, d := range p.Diagnostics {
		out = append(out, d.Code+"@"+d.Path)
	}
	return out
}

func (e *authEnv) events(action string, mods ...func(*http.Request)) []map[string]any {
	e.t.Helper()
	var body struct {
		Events []map[string]any `json:"events"`
	}
	e.send("GET", "/api/auth/events?limit=1000", nil, mods...).want(200).decode(&body)
	var out []map[string]any
	for _, ev := range body.Events {
		if ev["action"] == action {
			out = append(out, ev)
		}
	}
	return out
}

// ---- overview ----

func TestOverviewSaysHowEachWayInIsDoingAndHowTheCallerCameIn(t *testing.T) {
	e := newAuthEnv(t)
	ov := e.overview()
	for id, want := range map[string]string{"localhost": "on", "tailscale": "attention", "passkeys": "attention", "password": "attention"} {
		if got, enabled := ov.method(id); got != want || !enabled {
			t.Errorf("%s: state %q enabled %v, want %q enabled", id, got, enabled, want)
		}
	}
	if ov.Current.Method == nil || *ov.Current.Method != "localhost" || ov.Current.Remote || ov.Current.Source != "stack" || ov.Current.SessionID != nil {
		t.Errorf("current = %+v", ov.Current)
	}
	if ov.Password.Set || ov.Password.LastUsedAt != nil || ov.PasskeyCount != 0 || len(ov.AllowedLogins) != 0 {
		t.Errorf("overview = %+v", ov)
	}

	e.withPassword()
	cookie := e.loginRemote(testPW)
	e.editConfig(func(root *yaml.Node) (bool, error) { return spec.AddAllowedHost(root, tailnetHost) })
	ov = e.overview(remoteWith(cookie))
	if state, _ := ov.method("password"); state != "on" || !ov.Password.Set || !ov.Password.Enabled || ov.Password.LastUsedAt == nil {
		t.Errorf("password state %q: %+v", state, ov.Password)
	}
	if ov.Current.Method == nil || *ov.Current.Method != "password" || !ov.Current.Remote || ov.Current.Source != "session" || ov.Current.SessionID == nil {
		t.Errorf("current = %+v", ov.Current)
	}
	if strings.Contains(string(e.send("GET", "/api/auth/overview", nil).Body), "argon2") {
		t.Error("the overview contains the password hash")
	}
}

func TestTailnetNeedsSharingAndHandEditedStacksAreCustom(t *testing.T) {
	e := newAuthEnv(t)
	detail := func(id string) (state, text string, custom bool) {
		for _, m := range e.overview().Methods {
			if m.ID == id {
				return m.State, m.Detail, m.Custom
			}
		}
		return
	}
	if st, text, _ := detail("tailscale"); st != "attention" || !strings.Contains(text, "allowed_hosts") {
		t.Errorf("tailscale = %q %q", st, text)
	}
	e.editConfig(func(root *yaml.Node) (bool, error) { return spec.AddAllowedHost(root, tailnetHost) })
	if st, text, _ := detail("tailscale"); st != "attention" || !strings.Contains(text, "Sharing") {
		t.Errorf("tailscale = %q %q", st, text)
	}
	e.editConfig(func(root *yaml.Node) (bool, error) { return spec.SetTailscaleServe(root, true, 443) })
	if st, _, _ := detail("tailscale"); st != "on" {
		t.Errorf("tailscale = %q, want on", st)
	}

	// The admin service gets a stack without passkeys: passkeys are on for some services only.
	req := stacksReq("localhost-trust", "tailscale-identity", "webauthn", "password")
	req["stacks"].(map[string]any)["admin"] = []map[string]any{{"module": "localhost-trust", "control": "sufficient"}, {"module": "password", "control": "sufficient"}}
	req["services"].(map[string]string)["admin"] = "admin"
	e.send("PUT", "/api/auth/stacks", req).want(200)
	if st, _, custom := detail("passkeys"); !custom || st == "off" {
		t.Errorf("passkeys = %q custom %v, want custom and enabled", st, custom)
	}
	// Switching it on completes the shape: webauthn joins every stack that lacks it.
	e.send("PATCH", "/api/auth/methods", map[string]any{"passkeys": true}).want(200)
	if _, _, custom := detail("passkeys"); custom {
		t.Error("passkeys are still custom after switching them on")
	}
	if !strings.Contains(e.configText(), "admin:\n") {
		t.Errorf("config.yaml:\n%s", e.configText())
	}
}

func TestMethodSwitchesEditTheStacksAndKeepComments(t *testing.T) {
	e := newAuthEnv(t)
	r := e.send("PATCH", "/api/auth/methods", map[string]any{"tailscale": false, "passkeys": false}).want(200)
	var res changeBody
	r.decode(&res)
	if st, enabled := res.Overview.method("tailscale"); st != "off" || enabled {
		t.Errorf("tailscale = %q %v", st, enabled)
	}
	cfg := e.configText()
	if strings.Contains(cfg, "module: tailscale-identity") || strings.Contains(cfg, "module: webauthn") || !strings.Contains(cfg, "module: localhost-trust") {
		t.Errorf("config.yaml:\n%s", cfg)
	}
	if !strings.Contains(cfg, "# Auth: PAM-style stacks per service") || !strings.Contains(cfg, "# Logins accepted by the tailscale-identity auth module.") {
		t.Errorf("comments lost:\n%s", cfg)
	}
	if len(e.events("auth_methods_change")) != 1 {
		t.Error("the change was not logged as an auth event")
	}

	e.send("PATCH", "/api/auth/methods", map[string]any{"tailscale": true}).want(200)
	if ov := e.overview(); func() bool { _, on := ov.method("tailscale"); return !on }() {
		t.Error("tailscale did not come back on")
	}
	if !strings.Contains(e.configText(), "module: tailscale-identity") {
		t.Errorf("tailscale-identity not added back:\n%s", e.configText())
	}

	// Password can only be switched on once a password is set; localhost is not switchable.
	e.send("PATCH", "/api/auth/methods", map[string]any{"password": false}).want(200)
	before := e.configText()
	if p := e.send("PATCH", "/api/auth/methods", map[string]any{"password": true}).want(409); p.problem() != "invalid-state" {
		t.Errorf("problem = %s", p.problem())
	}
	e.send("PATCH", "/api/auth/methods", map[string]any{"localhost": false}).want(400)
	e.send("PATCH", "/api/auth/methods", map[string]any{}).want(422)
	if e.configText() != before {
		t.Error("a refused change wrote config.yaml")
	}
}

// ---- guardrails ----

func TestALocalRequestCannotTakeLocalhostTrustAway(t *testing.T) {
	e := newAuthEnv(t)
	before := e.configText()
	r := e.send("PUT", "/api/auth/stacks", stacksReq("tailscale-identity", "password")).want(422)
	if r.problem() != "validation-failed" || !strings.Contains(strings.Join(r.diagnosticCodes(), " "), "localhost_trust_required@auth.services.") {
		t.Errorf("diagnostics = %v", r.diagnosticCodes())
	}
	// Moving one service to a stack that does not admit this Mac is refused too.
	req := stacksReq("localhost-trust")
	req["stacks"].(map[string]any)["remote-only"] = []map[string]any{{"module": "password", "control": "sufficient"}}
	req["services"].(map[string]string)["admin"] = "remote-only"
	if r := e.send("PUT", "/api/auth/stacks", req).want(422); !strings.Contains(strings.Join(r.diagnosticCodes(), " "), "localhost_trust_required@auth.services.admin") {
		t.Errorf("diagnostics = %v", r.diagnosticCodes())
	}
	if e.configText() != before {
		t.Error("a refused change wrote config.yaml")
	}
	if p := e.send("POST", "/api/auth/stacks/preview", stacksReq("password")).want(200); p.json()["valid"] != false {
		t.Errorf("preview = %s", p.Body)
	}
}

func TestARemoteRequestCannotTakeLocalhostTrustAwayEvenConfirmed(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	cookie := e.loginRemote(testPW)
	before := e.configText()
	wantBlocked := func(r *resp, svc string) {
		t.Helper()
		if r.problem() != "validation-failed" || !strings.Contains(strings.Join(r.diagnosticCodes(), " "), "localhost_trust_required@auth.services."+svc) {
			t.Errorf("problem = %s", r.Body)
		}
		if e.configText() != before {
			t.Error("a refused change wrote config.yaml")
		}
	}

	// Removing localhost-trust from every stack: refused with and without confirm.
	req := keepHash(stacksReq("tailscale-identity", "webauthn", "password"))
	wantBlocked(e.send("PUT", "/api/auth/stacks", req, remoteWith(cookie)).want(422), "admin")
	req["confirm"] = true
	wantBlocked(e.send("PUT", "/api/auth/stacks", req, remoteWith(cookie)).want(422), "admin")

	// Moving one service to a stack without localhost-trust is refused too.
	req = keepHash(stacksReq("localhost-trust", "password"))
	req["stacks"].(map[string]any)["remote-only"] = []map[string]any{{"module": "password", "control": "sufficient"}}
	req["services"].(map[string]string)["admin"] = "remote-only"
	req["confirm"] = true
	wantBlocked(e.send("PUT", "/api/auth/stacks", req, remoteWith(cookie)).want(422), "admin")

	// The preview reports it as a blocking finding.
	var pv struct {
		Valid       bool `json:"valid"`
		Diagnostics []struct {
			Severity string `json:"severity"`
			Code     string `json:"code"`
		} `json:"diagnostics"`
	}
	e.send("POST", "/api/auth/stacks/preview", keepHash(stacksReq("tailscale-identity", "webauthn", "password")), remoteWith(cookie)).want(200).decode(&pv)
	found := false
	for _, d := range pv.Diagnostics {
		found = found || (d.Code == "localhost_trust_required" && d.Severity == "error")
	}
	if pv.Valid || !found {
		t.Errorf("preview = %+v", pv)
	}
	if len(e.events("auth_stacks_change", remoteWith(cookie))) != 0 {
		t.Error("a refused change was logged as applied")
	}
	// Methods cannot switch localhost at all.
	e.send("PATCH", "/api/auth/methods", map[string]any{"localhost": false, "confirm": true}, remoteWith(cookie)).want(400)
}

func TestSwitchingOffTheMethodTheCallerCameInNeedsConfirmationAndEndsTheSession(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	e.send("PATCH", "/api/auth/methods", map[string]any{"tailscale": false, "passkeys": false}).want(200) // password is the only remote way in
	cookie := e.loginRemote(testPW)
	other := e.loginRemote(testPW)
	before := e.configText()

	r := e.send("PATCH", "/api/auth/methods", map[string]any{"password": false}, remoteWith(cookie)).want(409)
	body := r.json()
	if r.problem() != "confirmation-required" || body["ends_session"] != true {
		t.Fatalf("problem = %s", r.Body)
	}
	if reasons, _ := body["reasons"].([]any); len(reasons) != 2 {
		t.Errorf("reasons = %v (want: ends this session, and no remote method left)", reasons)
	}
	if e.configText() != before {
		t.Error("an unconfirmed change wrote config.yaml")
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(cookie)).want(200)

	r = e.send("PATCH", "/api/auth/methods", map[string]any{"password": false, "confirm": true}, remoteWith(cookie)).want(200)
	var res changeBody
	r.decode(&res)
	if res.EndedSessions != 2 || res.Notice == nil || !strings.Contains(*res.Notice, "2 sessions") {
		t.Errorf("result = %s", r.Body)
	}
	cleared := false
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		cleared = cleared || (c.Name == auth.SessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Errorf("the session cookie was not cleared: %v", r.Header["Set-Cookie"])
	}
	cfg := e.configText()
	if strings.Contains(cfg, "argon2id") || strings.Contains(cfg, "module: password") || strings.Contains(cfg, "{module: password") {
		t.Errorf("password entries or hash remain:\n%s", cfg)
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(cookie)).want(401)
	e.send("GET", "/api/auth/overview", nil, remoteWith(other)).want(401)
}

func TestAFurtherRemoteMethodLetsTheCallerSwitchAnotherOneOffWithoutConfirming(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	cookie := e.loginRemote(testPW)
	res := e.send("PATCH", "/api/auth/methods", map[string]any{"tailscale": false, "passkeys": false}, remoteWith(cookie)).want(200)
	var body changeBody
	res.decode(&body)
	if body.EndedSessions != 0 || body.Notice != nil {
		t.Errorf("result = %s", res.Body)
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(cookie)).want(200)
}

// ---- password ----

func TestSettingAPasswordStoresOnlyAnArgon2idHash(t *testing.T) {
	e := newAuthEnv(t)
	e.send("PATCH", "/api/auth/methods", map[string]any{"password": false}).want(200)

	if p := e.send("PUT", "/api/auth/password", map[string]any{"password": "short"}).want(422); strings.Join(p.diagnosticCodes(), " ") != "invalid_value@password" {
		t.Errorf("diagnostics = %v", p.diagnosticCodes())
	}
	r := e.send("PUT", "/api/auth/password", map[string]any{"password": testPW}).want(200)
	var res changeBody
	r.decode(&res)
	if st, enabled := res.Overview.method("password"); st != "on" || !enabled || !res.Overview.Password.Set {
		t.Errorf("overview = %s", r.Body)
	}
	cfg := e.configText()
	if strings.Contains(cfg, testPW) || strings.Count(cfg, "$argon2id$") != 1 || !strings.Contains(cfg, "# Auth: PAM-style stacks per service") {
		t.Errorf("config.yaml:\n%s", cfg)
	}
	if strings.Contains(string(r.Body), testPW) || strings.Contains(string(r.Body), "argon2id") {
		t.Errorf("the response leaks the password or its hash: %s", r.Body)
	}
	for _, path := range []string{"/api/settings", "/api/auth/stacks"} {
		body := string(e.send("GET", path, nil).want(200).Body)
		if strings.Contains(body, "argon2id") || !strings.Contains(body, "«redacted»") {
			t.Errorf("%s does not redact the hash: %s", path, body)
		}
	}
	if len(e.events("auth_password_set")) != 1 {
		t.Error("the change was not logged")
	}
	// The new password signs in; a wrong one does not.
	e.loginRemote(testPW)
	e.send("POST", "/api/auth/admin/begin", map[string]any{"method": "password", "password": newPW}, remote).want(401)
}

func TestChangingThePasswordNeedsTheCurrentOneAndEndsOtherPasswordSessions(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	mine, theirs := e.loginRemote(testPW), e.loginRemote(testPW)
	before := e.configText()

	for name, body := range map[string]map[string]any{
		"missing": {"password": newPW},
		"wrong":   {"password": newPW, "current_password": "not the password"},
	} {
		if p := e.send("PUT", "/api/auth/password", body, remoteWith(mine)).want(422); strings.Join(p.diagnosticCodes(), " ") != "invalid_value@current_password" {
			t.Errorf("%s: diagnostics = %v", name, p.diagnosticCodes())
		}
	}
	if e.configText() != before {
		t.Error("a refused change wrote config.yaml")
	}

	r := e.send("PUT", "/api/auth/password", map[string]any{"password": newPW, "current_password": testPW}, remoteWith(mine)).want(200)
	var res changeBody
	r.decode(&res)
	if res.EndedSessions != 1 || res.Notice == nil {
		t.Errorf("result = %s", r.Body)
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(mine)).want(200)
	e.send("GET", "/api/auth/overview", nil, remoteWith(theirs)).want(401)
	if strings.Contains(e.configText(), newPW) || strings.Count(e.configText(), "$argon2id$") != 1 {
		t.Errorf("config.yaml:\n%s", e.configText())
	}
	e.loginRemote(newPW)
	e.send("POST", "/api/auth/admin/begin", map[string]any{"method": "password", "password": testPW}, remote).want(401)

	changes := e.events("auth_password_change")
	outcomes := map[string]int{}
	for _, ev := range changes {
		outcomes[ev["outcome"].(string)]++
	}
	if outcomes["fail"] != 1 || outcomes["success"] != 1 { // a missing current password is a bad request, not a failed attempt
		t.Errorf("auth_password_change outcomes = %v", outcomes)
	}
}

func TestFailedPasswordAttemptsShareOneLimiter(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	for range 3 {
		e.send("POST", "/api/auth/admin/begin", map[string]any{"method": "password", "password": "wrong wrong wrong"}, remote).want(401)
	}
	wrong := map[string]any{"password": newPW, "current_password": "wrong wrong wrong"}
	for range 2 {
		e.send("PUT", "/api/auth/password", wrong).want(422)
	}
	// Five failures from this address: everything password-related waits, even the right password.
	r := e.send("PUT", "/api/auth/password", map[string]any{"password": newPW, "current_password": testPW}).want(429)
	secs, err := strconv.Atoi(r.Header.Get("Retry-After"))
	if r.problem() != "rate-limited" || err != nil || secs < 1 || secs > 900 || r.json()["retry_after"] != float64(secs) {
		t.Errorf("429 = %s (Retry-After %q)", r.Body, r.Header.Get("Retry-After"))
	}
	e.send("POST", "/api/auth/admin/begin", map[string]any{"method": "password", "password": testPW}, remote).want(429)
	e.send("DELETE", "/api/auth/password", map[string]any{"current_password": testPW}).want(429)
	if strings.Count(e.configText(), "$argon2id$") != 1 || !strings.Contains(e.configText(), testPasswordHash(t)) {
		t.Error("the password changed while rate limited")
	}
}

func TestRemovingThePasswordEndsItsSessionsAndForgetsTheHash(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	mine, theirs := e.loginRemote(testPW), e.loginRemote(testPW)

	e.send("DELETE", "/api/auth/password", map[string]any{"current_password": "nope"}, remoteWith(mine)).want(422)
	// The caller's own session is one of them: confirmation first.
	if p := e.send("DELETE", "/api/auth/password", map[string]any{"current_password": testPW}, remoteWith(mine)).want(409); p.problem() != "confirmation-required" || p.json()["ends_session"] != true {
		t.Fatalf("problem = %s", p.Body)
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(theirs)).want(200)

	// From this Mac no confirmation is needed: no session of the caller is involved.
	r := e.send("DELETE", "/api/auth/password", map[string]any{"current_password": testPW}).want(200)
	var res changeBody
	r.decode(&res)
	if res.EndedSessions != 2 || res.Overview.Password.Set || res.Overview.Password.Enabled {
		t.Errorf("result = %s", r.Body)
	}
	if cfg := e.configText(); strings.Contains(cfg, "argon2id") || strings.Contains(cfg, "module: password") || strings.Contains(cfg, "{module: password") {
		t.Errorf("password entries or hash remain:\n%s", cfg)
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(mine)).want(401)
	e.send("GET", "/api/auth/overview", nil, remoteWith(theirs)).want(401)
	e.send("POST", "/api/auth/admin/begin", map[string]any{"method": "password", "password": testPW}, remote).want(401)

	// Removing nothing is fine and needs no current password.
	e.send("DELETE", "/api/auth/password", nil).want(200)
	if len(e.events("auth_password_remove")) != 2 { // one wrong attempt, one removal
		t.Errorf("events = %v", e.events("auth_password_remove"))
	}
}

// ---- allowed logins ----

func TestAllowedLoginsAreValidatedDeduplicatedAndWrittenWithTheCommentsKept(t *testing.T) {
	e := newAuthEnv(t)
	before := e.configText()
	p := e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{"alice@example.com", "bob", "a b@example.com", "x,y@example.com", "@example.com", "carol@", ""}}).want(422)
	if got := strings.Join(p.diagnosticCodes(), " "); got != "invalid_value@logins[1] invalid_value@logins[2] invalid_value@logins[3] invalid_value@logins[4] invalid_value@logins[5] invalid_value@logins[6]" {
		t.Errorf("diagnostics = %s", got)
	}
	e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{strings.Repeat("a", 250) + "@example.com"}}).want(422)
	if e.configText() != before {
		t.Error("a refused change wrote config.yaml")
	}

	r := e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{" Alice@Example.com ", "alice@example.com", "bob@github"}}).want(200)
	var res changeBody
	r.decode(&res)
	if strings.Join(res.Overview.AllowedLogins, ",") != "Alice@Example.com,bob@github" {
		t.Errorf("allowed_logins = %v", res.Overview.AllowedLogins)
	}
	cfg := e.configText()
	if !strings.Contains(cfg, "- Alice@Example.com") || !strings.Contains(cfg, "# Logins accepted by the tailscale-identity auth module.") || !strings.Contains(cfg, "# Hostnames (besides localhost:9001") {
		t.Errorf("config.yaml:\n%s", cfg)
	}
	if len(e.events("auth_logins_change")) != 1 {
		t.Error("the change was not logged")
	}

	// An empty list means this Mac's owner again.
	e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{}}).want(200)
	if !strings.Contains(e.configText(), "allowed_logins: []") {
		t.Errorf("config.yaml:\n%s", e.configText())
	}
	if got := e.overview().AllowedLogins; got == nil || len(got) != 0 {
		t.Errorf("allowed_logins = %v, want an empty list", got)
	}
}

func TestRemovingTheCallersOwnTailnetLoginNeedsConfirmation(t *testing.T) {
	e := newAuthEnv(t)
	e.editConfig(func(root *yaml.Node) (bool, error) { return spec.AddAllowedHost(root, tailnetHost) })
	alice := asTailnetUser("alice@example.com")

	ov := e.overview(alice)
	if ov.Current.Method == nil || *ov.Current.Method != "tailscale" || !ov.Current.Remote || ov.Current.Principal != "alice@example.com" {
		t.Fatalf("current = %+v", ov.Current)
	}
	before := e.configText()
	r := e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{"bob@example.com"}}, alice).want(409)
	if r.problem() != "confirmation-required" || r.json()["ends_session"] != false {
		t.Fatalf("problem = %s", r.Body)
	}
	if e.configText() != before {
		t.Error("an unconfirmed change wrote config.yaml")
	}
	// Keeping the caller's login (any letter case) needs none.
	e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{"Alice@example.com", "bob@example.com"}}, alice).want(200)
	e.send("PUT", "/api/auth/allowed-logins", map[string]any{"logins": []string{"bob@example.com"}, "confirm": true}, alice).want(200)
	if !strings.Contains(e.configText(), "bob@example.com") || strings.Contains(e.configText(), "alice@example.com") {
		t.Errorf("config.yaml:\n%s", e.configText())
	}
	e.send("GET", "/api/auth/overview", nil, alice).want(401)
}

// ---- stack editor ----

func TestTheStackEditorRejectsWhatTheConfigLoaderRejects(t *testing.T) {
	e := newAuthEnv(t)
	before := e.configText()
	for name, tc := range map[string]struct {
		mutate func(req map[string]any)
		want   string
	}{
		"unknown module": {func(req map[string]any) {
			req["stacks"].(map[string]any)["default"] = []map[string]any{{"module": "localhost-trust", "control": "sufficient"}, {"module": "retina-scan", "control": "sufficient"}}
		}, "unknown_module@auth.stacks.default[1].module"},
		"unknown control": {func(req map[string]any) {
			req["stacks"].(map[string]any)["default"] = []map[string]any{{"module": "localhost-trust", "control": "maybe"}}
		}, "invalid_enum@auth.stacks.default[0].control"},
		"unknown service": {func(req map[string]any) { req["services"].(map[string]string)["backdoor"] = "default" }, "invalid_enum@auth.services.backdoor"},
		"unknown stack":   {func(req map[string]any) { req["services"].(map[string]string)["api"] = "nope" }, "unknown_stack@auth.services.api"},
		"missing service": {func(req map[string]any) { delete(req["services"].(map[string]string), "approve") }, "required@auth.services.approve"},
		"no stacks":       {func(req map[string]any) { req["stacks"] = map[string]any{} }, "required@auth.stacks"},
		"plaintext hash": {func(req map[string]any) {
			req["stacks"].(map[string]any)["default"] = []map[string]any{{"module": "localhost-trust", "control": "sufficient"}, {"module": "password", "control": "sufficient", "options": map[string]any{"hash": "hunter2"}}}
		}, "invalid_value@auth.stacks.default[1].options.hash"},
	} {
		req := stacksReq("localhost-trust", "password")
		tc.mutate(req)
		r := e.send("PUT", "/api/auth/stacks", req).want(422)
		if !strings.Contains(strings.Join(r.diagnosticCodes(), " "), tc.want) {
			t.Errorf("%s: diagnostics %v, want %s", name, r.diagnosticCodes(), tc.want)
		}
		p := e.send("POST", "/api/auth/stacks/preview", req).want(200)
		if p.json()["valid"] != false || !strings.Contains(strings.Join(p.diagnosticCodes(), " "), tc.want) {
			t.Errorf("%s: preview = %s", name, p.Body)
		}
	}
	if e.configText() != before {
		t.Error("a refused change wrote config.yaml")
	}
}

func TestPreviewShowsTheDiffAndWritesNothing(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	cookie := e.loginRemote(testPW)
	before := e.configText()

	var pv struct {
		Valid           bool     `json:"valid"`
		Changed         bool     `json:"changed"`
		ConfirmRequired bool     `json:"confirm_required"`
		Reasons         []string `json:"reasons"`
		EndsSession     bool     `json:"ends_session"`
		Before          string   `json:"before"`
		After           string   `json:"after"`
	}
	req := stacksReq("localhost-trust", "tailscale-identity", "webauthn")
	e.send("POST", "/api/auth/stacks/preview", req, remoteWith(cookie)).want(200).decode(&pv)
	if !pv.Valid || !pv.Changed || !pv.ConfirmRequired || !pv.EndsSession || len(pv.Reasons) != 1 {
		t.Errorf("preview = %+v", pv)
	}
	if !strings.Contains(pv.Before, "module: password") || strings.Contains(pv.After, "module: password") || !strings.HasPrefix(pv.After, "auth:\n") {
		t.Errorf("before:\n%s\nafter:\n%s", pv.Before, pv.After)
	}
	if strings.Contains(pv.Before, "argon2id") || !strings.Contains(pv.Before, "«redacted»") {
		t.Errorf("the preview does not redact the hash:\n%s", pv.Before)
	}
	if e.configText() != before {
		t.Error("a preview wrote config.yaml")
	}
	e.send("GET", "/api/auth/overview", nil, remoteWith(cookie)).want(200)

	// Nothing to change.
	e.send("POST", "/api/auth/stacks/preview", keepHash(stacksReq("localhost-trust", "tailscale-identity", "webauthn", "password"))).want(200).decode(&pv)
	if !pv.Valid || pv.Changed || pv.ConfirmRequired || len(pv.Reasons) != 0 {
		t.Errorf("preview of the current stacks = %+v", pv)
	}
}

func TestStackEditsKeepTheStoredHashForRedactedValuesAndTheCommentsAndRefuseAStaleHash(t *testing.T) {
	e := newAuthEnv(t)
	e.withPassword()
	var view struct {
		Stacks map[string][]struct {
			Module  string         `json:"module"`
			Control string         `json:"control"`
			Options map[string]any `json:"options"`
		} `json:"stacks"`
		Hash string `json:"hash"`
	}
	e.send("GET", "/api/auth/stacks", nil).want(200).decode(&view)
	var hashOption any
	for _, en := range view.Stacks["default"] {
		if en.Module == "password" {
			hashOption = en.Options["hash"]
		}
	}
	if hashOption != "«redacted»" {
		t.Fatalf("stacks = %+v", view.Stacks)
	}

	// Send the stacks back as read (with «redacted»), but with webauthn required instead of sufficient.
	req := map[string]any{"services": map[string]string{"ui": "default", "api": "default", "approve": "default", "admin": "default"}, "expected_hash": view.Hash}
	entries := make([]map[string]any, 0, len(view.Stacks["default"]))
	for _, en := range view.Stacks["default"] {
		m := map[string]any{"module": en.Module, "control": en.Control}
		if len(en.Options) > 0 {
			m["options"] = en.Options
		}
		if en.Module == "webauthn" {
			m["control"] = "required"
		}
		entries = append(entries, m)
	}
	req["stacks"] = map[string]any{"default": entries}
	e.send("PUT", "/api/auth/stacks", req).want(200)

	cfg := e.configText()
	if strings.Count(cfg, testPasswordHash(t)) != 1 || strings.Contains(cfg, "«redacted»") {
		t.Errorf("the stored hash was not kept:\n%s", cfg)
	}
	if !strings.Contains(cfg, "# Auth: PAM-style stacks per service") || !strings.Contains(cfg, "module: webauthn, control: required") {
		t.Errorf("config.yaml:\n%s", cfg)
	}
	if !strings.Contains(cfg, "{module: localhost-trust, control: sufficient}") {
		t.Errorf("an unchanged entry was rewritten:\n%s", cfg)
	}
	e.loginRemote(testPW) // the kept hash still verifies

	// The hash the client read is stale now.
	if p := e.send("PUT", "/api/auth/stacks", req).want(409); p.problem() != "conflict" {
		t.Errorf("problem = %s", p.problem())
	}
	// «redacted» with nothing stored to keep is an error, not a literal value.
	bad := stacksReq("localhost-trust")
	bad["stacks"].(map[string]any)["default"] = []map[string]any{{"module": "localhost-trust", "control": "sufficient", "options": map[string]any{"token": "«redacted»"}}}
	e.send("PUT", "/api/auth/stacks", bad).want(422)
}

// ---- who may use it ----

func TestTheSettingsNeedAnAdminSession(t *testing.T) {
	// ui, api and approve sign in with the password; admin wants a passkey nobody has.
	cfg := auth.DefaultConfig()
	cfg.Stacks = map[string][]auth.StackEntry{
		"ui":    {{Module: "password", Control: auth.Sufficient}},
		"admin": {{Module: "webauthn", Control: auth.Sufficient}},
	}
	cfg.Services = map[auth.Service]string{auth.ServiceUI: "ui", auth.ServiceAPI: "ui", auth.ServiceApprove: "ui", auth.ServiceAdmin: "admin"}
	cfg.PasswordHash = testPasswordHash(t)
	e := newEnv(t, func(eo *envOptions) { eo.auth = &cfg })

	ops := []struct{ method, path string }{
		{"GET", "/api/auth/overview"}, {"PATCH", "/api/auth/methods"}, {"PUT", "/api/auth/password"}, {"DELETE", "/api/auth/password"},
		{"PUT", "/api/auth/allowed-logins"}, {"GET", "/api/auth/stacks"}, {"PUT", "/api/auth/stacks"}, {"POST", "/api/auth/stacks/preview"},
	}
	for _, op := range ops {
		if got := e.do(op.method, op.path, "{}").Status; got != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials = %d, want 401", op.method, op.path, got)
		}
	}

	login := request("POST", "/api/auth/ui/begin", encodeBody(t, map[string]any{"method": "password", "password": testPW}))
	res := serve(t, e.srv, login).want(200)
	cookie := (&http.Response{Header: res.Header}).Cookies()[0]
	for _, op := range ops {
		req := request(op.method, op.path, []byte("{}"))
		req.AddCookie(cookie)
		if got := serve(t, e.srv, req).Status; got != http.StatusForbidden {
			t.Errorf("%s %s with a session that does not cover admin = %d, want 403", op.method, op.path, got)
		}
	}
}
