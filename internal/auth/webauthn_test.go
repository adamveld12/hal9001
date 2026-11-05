package auth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	vwa "github.com/descope/virtualwebauthn"

	"hal9001/internal/auth"
)

// Passkey flows are driven through the API with a virtual authenticator
// (github.com/descope/virtualwebauthn), the only way to exercise real
// attestation and assertion verification without a browser.

type passkeyEnv struct {
	*env
	session *http.Cookie
}

func newPasskeyEnv(t *testing.T) *passkeyEnv {
	t.Helper()
	cfg := stackConfig(entry("webauthn", auth.Sufficient), entry("password", auth.Sufficient))
	cfg.AllowedHosts = []string{tsHost}
	e := newEnv(t, withConfig(cfg))
	return &passkeyEnv{env: e, session: loginCookie(t, e)}
}

// rpFor returns the relying party the browser would use for host.
func rpFor(host string) vwa.RelyingParty {
	if host == localHost {
		return vwa.RelyingParty{Name: "hal9001", ID: "localhost", Origin: "http://" + host}
	}
	return vwa.RelyingParty{Name: "hal9001", ID: host, Origin: "https://" + host}
}

// at puts a request on host the way the real deployment would deliver it.
func at(r *req, host string) *req {
	if host == tsHost {
		return r.on(host).hdr("X-Forwarded-Proto", "https")
	}
	return r.on(host)
}

// register runs the registration API for host and returns the credential.
func (p *passkeyEnv) register(t *testing.T, host string, authn vwa.Authenticator, label string) (vwa.Credential, int) {
	t.Helper()
	rec := p.do(at(post("/api/auth/webauthn/register/begin", map[string]string{"label": label}).cookie(p.session), host))
	wantStatus(t, rec, 200)
	begin := decode[struct {
		RegistrationID string          `json:"registration_id"`
		Options        json.RawMessage `json:"options"`
	}](t, rec)
	opts, err := vwa.ParseAttestationOptions(string(begin.Options))
	if err != nil {
		t.Fatalf("options: %v\n%s", err, begin.Options)
	}
	if opts.RelyingPartyID != rpFor(host).ID {
		t.Fatalf("options rpId = %q, want %q", opts.RelyingPartyID, rpFor(host).ID)
	}
	cred := vwa.NewCredential(vwa.KeyTypeEC2)
	att := vwa.CreateAttestationResponse(rpFor(host), authn, cred, *opts)
	rec = p.do(at(post("/api/auth/webauthn/register/finish", map[string]any{
		"registration_id": begin.RegistrationID, "credential": json.RawMessage(att),
	}).cookie(p.session), host))
	return cred, rec.Code
}

type loginResult struct {
	status int
	cookie *http.Cookie
	body   string
}

// login runs begin + continue with authn for host.
func (p *passkeyEnv) login(t *testing.T, host string, authn vwa.Authenticator, cred *vwa.Credential) loginResult {
	t.Helper()
	rec := p.do(at(post("/api/auth/ui/begin", map[string]string{"method": "webauthn"}), host))
	wantStatus(t, rec, 200)
	ch := decode[struct {
		Status  string
		LoginID string `json:"login_id"`
		Step    struct {
			Kind string
			Data json.RawMessage
		}
	}](t, rec)
	if ch.Status != "challenge" || ch.Step.Kind != "webauthn.assertion" {
		t.Fatalf("begin = %s, want a webauthn challenge", rec.Body)
	}
	opts, err := vwa.ParseAssertionOptions(string(ch.Step.Data))
	if err != nil {
		t.Fatal(err)
	}
	cred.Counter++
	assertion := vwa.CreateAssertionResponse(rpFor(host), authn, *cred, *opts)
	rec = p.do(at(post("/api/auth/ui/continue", map[string]any{"login_id": ch.LoginID, "response": json.RawMessage(assertion)}), host))
	res := loginResult{status: rec.Code, body: rec.Body.String()}
	if rec.Code == 200 {
		res.cookie = sessionCookie(t, rec)
	}
	return res
}

func TestPasskeyRegistrationAndLogin(t *testing.T) {
	p := newPasskeyEnv(t)
	authn := vwa.NewAuthenticatorWithOptions(vwa.AuthenticatorOptions{UserHandle: nil})

	cred, status := p.register(t, localHost, authn, "MacBook Touch ID")
	if status != 201 {
		t.Fatalf("register finish = %d", status)
	}

	rec := p.do(get("/api/auth/webauthn/credentials").cookie(p.session))
	wantStatus(t, rec, 200)
	list := decode[struct{ Credentials []auth.WebAuthnCredential }](t, rec).Credentials
	if len(list) != 1 || list[0].RPID != "localhost" || list[0].Label != "MacBook Touch ID" ||
		list[0].ID != base64.RawURLEncoding.EncodeToString(cred.ID) || list[0].SignCount != 0 {
		t.Fatalf("credentials = %+v", list)
	}
	if strings.Contains(rec.Body.String(), "public") || strings.Contains(rec.Body.String(), `"data"`) {
		t.Errorf("credential list leaks key material: %s", rec.Body)
	}

	// Log in without a session, using only the passkey.
	res := p.login(t, localHost, authn, &cred)
	if res.status != 200 {
		t.Fatalf("passkey login = %d %s", res.status, res.body)
	}
	if ev := p.lastEvent(); ev.Action != "login_continue" || ev.Outcome != auth.OutcomeSuccess || ev.Principal != "owner" ||
		joinResults(ev) != "webauthn:challenge,webauthn:success" {
		t.Errorf("event = %+v (%s)", ev, joinResults(ev))
	}
	wantStatus(t, p.do(get("/api/ping/admin").cookie(res.cookie)), 200)

	// The sign count and last-used time were recorded.
	stored, _ := p.store.ListWebAuthnCredentials(context.Background(), "localhost")
	if stored[0].SignCount != 1 || stored[0].LastUsedAt == nil {
		t.Errorf("stored credential after login = %+v, want sign count 1 and last_used set", stored[0])
	}
	res = p.login(t, localHost, authn, &cred)
	if res.status != 200 {
		t.Fatalf("second login = %d %s", res.status, res.body)
	}
	if stored, _ = p.store.ListWebAuthnCredentials(context.Background(), "localhost"); stored[0].SignCount != 2 {
		t.Errorf("sign count = %d, want 2", stored[0].SignCount)
	}

	// A signature with a non-increasing counter is rejected as a possible clone.
	stale := cred
	stale.Counter = 0 // login() increments to 1, which is below the stored 2
	if res := p.login(t, localHost, authn, &stale); res.status != 401 {
		t.Errorf("stale-counter login = %d, want 401", res.status)
	}
	if ev := p.lastEvent(); ev.Outcome != auth.OutcomeFail || ev.Modules[1].Result != auth.KindFail || !strings.Contains(ev.Modules[1].Reason, "cloned") {
		t.Errorf("event = %+v, want a webauthn clone-warning failure", ev.Modules)
	}
}

func TestPasskeyLoginRejectsUnknownKeyAndUnverifiedUser(t *testing.T) {
	p := newPasskeyEnv(t)
	authn := vwa.NewAuthenticator()
	cred, status := p.register(t, localHost, authn, "real")
	if status != 201 {
		t.Fatalf("register = %d", status)
	}

	stranger := vwa.NewCredential(vwa.KeyTypeEC2)
	if res := p.login(t, localHost, authn, &stranger); res.status != 401 {
		t.Errorf("login with an unregistered credential = %d, want 401", res.status)
	}
	// Same credential ID, different private key: signature must not verify.
	forged := vwa.NewCredential(vwa.KeyTypeEC2)
	forged.ID = cred.ID
	if res := p.login(t, localHost, authn, &forged); res.status != 401 {
		t.Errorf("login with a forged signature = %d, want 401", res.status)
	}
	noUV := vwa.NewAuthenticatorWithOptions(vwa.AuthenticatorOptions{UserNotVerified: true})
	if res := p.login(t, localHost, noUV, &cred); res.status != 401 {
		t.Errorf("login without user verification = %d, want 401", res.status)
	}
	if res := p.login(t, localHost, authn, &cred); res.status != 200 {
		t.Errorf("legitimate login after the failures = %d, want 200", res.status)
	}
}

func TestPasskeyRegistrationNeedsAuthenticatedSessionAndValidAttestation(t *testing.T) {
	// localhost-trust admits per-request calls, but registration needs a real session.
	e := newEnv(t)
	wantStatus(t, e.do(post("/api/auth/webauthn/register/begin", map[string]string{})), 401)
	wantStatus(t, e.do(post("/api/auth/webauthn/register/finish", map[string]any{"registration_id": "x", "credential": map[string]string{}})), 401)

	// A session that does not cover admin may not register either.
	cfg := auth.DefaultConfig()
	cfg.Stacks = map[string][]auth.StackEntry{
		"basic":  {entry("password", auth.Sufficient)},
		"strict": {entry("password", auth.Required), entry("otp", auth.Required)},
	}
	cfg.Services = map[auth.Service]string{auth.ServiceUI: "basic", auth.ServiceAPI: "basic", auth.ServiceApprove: "basic", auth.ServiceAdmin: "strict"}
	split := newEnv(t, withConfig(cfg), withModules(otpModule()))
	rec := split.do(post("/api/auth/ui/begin", map[string]string{"password": testPW}))
	wantStatus(t, rec, 200)
	wantStatus(t, split.do(post("/api/auth/webauthn/register/begin", map[string]string{}).cookie(sessionCookie(t, rec))), 403)

	p := newPasskeyEnv(t)
	authn := vwa.NewAuthenticator()

	// Attestation verification: wrong origin, missing user verification, replayed registration id.
	begin := func() (string, *vwa.AttestationOptions) {
		rec := p.do(post("/api/auth/webauthn/register/begin", nil).cookie(p.session))
		wantStatus(t, rec, 200)
		b := decode[struct {
			RegistrationID string          `json:"registration_id"`
			Options        json.RawMessage `json:"options"`
		}](t, rec)
		o, err := vwa.ParseAttestationOptions(string(b.Options))
		if err != nil {
			t.Fatal(err)
		}
		return b.RegistrationID, o
	}
	finish := func(id string, att string) int {
		return p.do(post("/api/auth/webauthn/register/finish", map[string]any{"registration_id": id, "credential": json.RawMessage(att)}).cookie(p.session)).Code
	}
	cred := vwa.NewCredential(vwa.KeyTypeEC2)

	id, opts := begin()
	evil := vwa.RelyingParty{Name: "hal9001", ID: "localhost", Origin: "http://evil.example"}
	if got := finish(id, vwa.CreateAttestationResponse(evil, authn, cred, *opts)); got != 400 {
		t.Errorf("attestation for another origin = %d, want 400", got)
	}
	id, opts = begin()
	noUV := vwa.NewAuthenticatorWithOptions(vwa.AuthenticatorOptions{UserNotVerified: true})
	if got := finish(id, vwa.CreateAttestationResponse(rpFor(localHost), noUV, cred, *opts)); got != 400 {
		t.Errorf("attestation without user verification = %d, want 400", got)
	}
	id, opts = begin()
	good := vwa.CreateAttestationResponse(rpFor(localHost), authn, cred, *opts)
	if got := finish(id, good); got != 201 {
		t.Errorf("valid attestation = %d, want 201", got)
	}
	if got := finish(id, good); got != 410 {
		t.Errorf("replayed registration id = %d, want 410", got)
	}
	if ev := p.events()[0]; ev.Action != "webauthn_register" || ev.Outcome != auth.OutcomeFail {
		t.Errorf("newest event = %+v (the replay), want a failed registration", ev)
	}
	if list, _ := p.store.ListAllWebAuthnCredentials(context.Background()); len(list) != 1 {
		t.Errorf("%d credentials stored, want exactly the one valid registration", len(list))
	}
}

func TestPasskeysAreScopedPerRelyingPartyID(t *testing.T) {
	p := newPasskeyEnv(t)
	authn := vwa.NewAuthenticator()

	local, status := p.register(t, localHost, authn, "local")
	if status != 201 {
		t.Fatalf("register localhost = %d", status)
	}
	// Before a passkey exists for the ts.net hostname, webauthn ignores there and password still works.
	wantStatus(t, p.do(at(post("/api/auth/ui/begin", map[string]string{"password": testPW}), tsHost)), 200)
	rec := p.do(at(post("/api/auth/ui/begin", map[string]string{}), tsHost))
	wantStatus(t, rec, 401)
	if ev := p.lastEvent(); ev.Modules[0].Result != auth.KindIgnore || !strings.Contains(ev.Modules[0].Reason, "no passkey registered for "+tsHost) {
		t.Errorf("webauthn at the ts.net host = %+v, want ignore: the localhost passkey must not apply", ev.Modules)
	}

	remote, status := p.register(t, tsHost, authn, "tailnet")
	if status != 201 {
		t.Fatalf("register %s = %d", tsHost, status)
	}
	if res := p.login(t, tsHost, authn, &remote); res.status != 200 {
		t.Fatalf("ts.net login = %d %s", res.status, res.body)
	}
	// A passkey from one RP ID cannot sign in at the other: its challenge only lists its own credentials.
	if res := p.login(t, localHost, authn, &remote); res.status != 401 {
		t.Errorf("ts.net credential at localhost = %d, want 401", res.status)
	}
	if res := p.login(t, tsHost, authn, &local); res.status != 401 {
		t.Errorf("localhost credential at the ts.net host = %d, want 401", res.status)
	}
	if res := p.login(t, localHost, authn, &local); res.status != 200 {
		t.Errorf("localhost credential at localhost = %d, want 200", res.status)
	}

	// An IP host has no WebAuthn RP ID.
	wantStatus(t, p.do(post("/api/auth/ui/begin", map[string]string{}).on("127.0.0.1:9001")), 401)
	if ev := p.lastEvent(); ev.Modules[0].Result != auth.KindIgnore || !strings.Contains(ev.Modules[0].Reason, "IP address") {
		t.Errorf("modules = %+v, want ignore for an IP host", ev.Modules)
	}

	// Removing a passkey takes effect immediately.
	listRec := p.do(get("/api/auth/webauthn/credentials").cookie(p.session))
	for _, c := range decode[struct{ Credentials []auth.WebAuthnCredential }](t, listRec).Credentials {
		wantStatus(t, p.do(del("/api/auth/webauthn/credentials/"+c.ID).cookie(p.session)), 204)
	}
	wantStatus(t, p.do(del("/api/auth/webauthn/credentials/nope").cookie(p.session)), 404)
	wantStatus(t, p.do(post("/api/auth/ui/begin", map[string]string{"method": "webauthn"})), 401)
}
