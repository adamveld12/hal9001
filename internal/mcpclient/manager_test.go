package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hal9001/internal/mcpclient"
	"hal9001/internal/mcpclient/mcptest"
	"hal9001/internal/spec"
)

const redirect = "http://127.0.0.1:9001/oauth/callback"

type notes struct {
	mu sync.Mutex
	n  []mcpclient.Note
}

func (r *notes) PostNote(n mcpclient.Note) {
	r.mu.Lock()
	r.n = append(r.n, n)
	r.mu.Unlock()
}

func (r *notes) all() []mcpclient.Note {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mcpclient.Note(nil), r.n...)
}

type env struct {
	t     *testing.T
	home  string
	m     *mcpclient.Manager
	cfg   map[string]spec.RemoteMCP
	notes *notes
	now   time.Time
	mu    sync.Mutex
}

func newEnv(t *testing.T, name string, def spec.RemoteMCP) *env {
	t.Helper()
	e := &env{t: t, home: t.TempDir(), cfg: map[string]spec.RemoteMCP{name: def}, notes: &notes{}, now: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	e.m = mcpclient.New(mcpclient.Options{
		Home: e.home, Notifier: e.notes, RedirectURL: func() string { return redirect }, Version: "test",
		Servers: func() map[string]spec.RemoteMCP { return e.cfg },
		Now:     func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
	})
	return e
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// connect runs the whole authorization with a fake browser and returns the callback query it produced.
func (e *env) connect(name string) {
	e.t.Helper()
	ctx := context.Background()
	res, err := e.m.Connect(ctx, name)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	cb, err := mcptest.Approve(ctx, res.URL)
	if err != nil {
		e.t.Fatalf("approve: %v", err)
	}
	if got, err := e.m.Callback(ctx, callbackQuery(e.t, cb)); err != nil || got != name {
		e.t.Fatalf("callback: %q %v", got, err)
	}
}

func callbackQuery(t *testing.T, cb string) mcpclient.CallbackQuery {
	t.Helper()
	u, err := url.Parse(cb)
	if err != nil || !strings.HasPrefix(cb, redirect) {
		t.Fatalf("callback %q: %v", cb, err)
	}
	q := u.Query()
	return mcpclient.CallbackQuery{State: q.Get("state"), Code: q.Get("code")}
}

func oauthDef(srv *mcptest.Server) spec.RemoteMCP { return spec.RemoteMCP{URL: srv.URL} }

func TestOAuthWithDynamicRegistrationConnectsAndCachesTools(t *testing.T) {
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))

	if v, _ := e.m.Server("notion"); v.Status != mcpclient.StatusDisconnected {
		t.Fatalf("before connecting: %s", v.Status)
	}
	e.connect("notion")

	v, err := e.m.Server("notion")
	if err != nil || v.Status != mcpclient.StatusConnected || v.ToolCount != 4 || v.ServerName != "fake-notion" || v.TokenExpiresAt == nil {
		t.Fatalf("view after connect: %+v %v", v, err)
	}
	d, _ := e.m.Detail("notion")
	if d.Tools[3].Name != "search" || !strings.Contains(string(d.Tools[3].InputSchema), `"filters"`) || d.Tools[3].Description != "Search the workspace." {
		t.Errorf("cached tools: %+v", d.Tools)
	}

	ar := srv.AuthRequests()
	if len(ar) != 1 {
		t.Fatalf("authorization requests: %v", ar)
	}
	q := ar[0]
	if q.Get("code_challenge_method") != "S256" || q.Get("resource") != srv.URL || q.Get("redirect_uri") != redirect || q.Get("scope") != "read write" || len(q.Get("state")) < 20 {
		t.Errorf("authorize request: %v", q)
	}
	tr := srv.TokenRequests()
	if len(tr) != 1 || tr[0].Get("grant_type") != "authorization_code" || tr[0].Get("resource") != srv.URL || len(tr[0].Get("code_verifier")) < 40 {
		t.Errorf("token request: %v", tr)
	}
	if srv.Registrations() != 1 {
		t.Errorf("registrations: %d", srv.Registrations())
	}

	// A second connect reuses the registration.
	e.connect("notion")
	if srv.Registrations() != 1 {
		t.Errorf("registered again: %d", srv.Registrations())
	}

	info, err := os.Stat(filepath.Join(e.home, "data", "mcp", "notion.secrets.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secrets file: %v %v", info, err)
	}
}

func TestCallbackRefusesBadStateReusedStateExpiredStateAndDeniedConsent(t *testing.T) {
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))
	ctx := context.Background()

	if _, err := e.m.Callback(ctx, mcpclient.CallbackQuery{State: "forged", Code: "x"}); !errors.Is(err, mcpclient.ErrInvalidState) {
		t.Errorf("forged state: %v", err)
	}
	res, _ := e.m.Connect(ctx, "notion")
	cb, _ := mcptest.Approve(ctx, res.URL)
	q := callbackQuery(t, cb)
	if _, err := e.m.Callback(ctx, q); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	if name, err := e.m.Callback(ctx, q); !errors.Is(err, mcpclient.ErrInvalidState) || name != "" {
		t.Errorf("reused state: %q %v", name, err)
	}
	if n := len(srv.TokenRequests()); n != 1 {
		t.Errorf("the code was redeemed %d times", n)
	}

	res, _ = e.m.Connect(ctx, "notion")
	cb, _ = mcptest.Approve(ctx, res.URL)
	e.advance(11 * time.Minute)
	if _, err := e.m.Callback(ctx, callbackQuery(t, cb)); !errors.Is(err, mcpclient.ErrInvalidState) {
		t.Errorf("expired state: %v", err)
	}
}

func TestDeniedConsentIsRecordedOnTheServer(t *testing.T) {
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))
	ctx := context.Background()
	res, _ := e.m.Connect(ctx, "notion")
	u, _ := url.Parse(res.URL)
	name, err := e.m.Callback(ctx, mcpclient.CallbackQuery{State: u.Query().Get("state"), Error: "access_denied", ErrorDescription: "nope"})
	if name != "notion" || err == nil {
		t.Fatalf("callback: %q %v", name, err)
	}
	v, _ := e.m.Server("notion")
	if v.Status != mcpclient.StatusError || !strings.Contains(v.Error, "access_denied") {
		t.Errorf("view: %+v", v)
	}
}

func TestPastedClientCredentials(t *testing.T) {
	srv := mcptest.New(mcptest.Options{NoDCR: true, Client: "my-client", ClientSecret: "s3cret"})
	defer srv.Close()

	// Without a pasted client the provider cannot be used.
	auto := newEnv(t, "linear", oauthDef(srv))
	if _, err := auto.m.Connect(context.Background(), "linear"); !errors.Is(err, mcpclient.ErrNoRegistration) {
		t.Fatalf("automatic registration without DCR: %v", err)
	}

	e := newEnv(t, "linear", spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthOAuthClient, ClientID: "my-client"})
	secret := "s3cret"
	if err := e.m.SetSecrets("linear", mcpclient.Secrets{ClientSecret: &secret}); err != nil {
		t.Fatal(err)
	}
	e.connect("linear")
	if v, _ := e.m.Server("linear"); v.Status != mcpclient.StatusConnected || !v.HasClientSecret || v.ClientID != "my-client" {
		t.Errorf("view: %+v", v)
	}
	if got := srv.TokenRequests()[0]; got.Get("client_secret") != "s3cret" || got.Get("client_id") != "my-client" {
		t.Errorf("token request: %v", got)
	}

	wrong := newEnv(t, "linear", spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthOAuthClient, ClientID: "my-client"})
	bad := "wrong"
	_ = wrong.m.SetSecrets("linear", mcpclient.Secrets{ClientSecret: &bad})
	ctx := context.Background()
	res, err := wrong.m.Connect(ctx, "linear")
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := mcptest.Approve(ctx, res.URL)
	if _, err := wrong.m.Callback(ctx, callbackQuery(t, cb)); err == nil {
		t.Fatal("a wrong client secret was accepted")
	}
	if v, _ := wrong.m.Server("linear"); v.Status != mcpclient.StatusError {
		t.Errorf("status after a refused exchange: %s", v.Status)
	}
}

func TestAPIKeyHeader(t *testing.T) {
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Header, Header: "X-Api-Key", HeaderValue: "k-123"})
	defer srv.Close()
	e := newEnv(t, "internal", spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthHeader, Header: "X-Api-Key"})
	ctx := context.Background()

	if _, err := e.m.Call(ctx, "internal", "echo", nil); !errors.Is(err, mcpclient.ErrNotConnected) {
		t.Fatalf("without a key: %v", err)
	}
	wrong := "nope"
	_ = e.m.SetSecrets("internal", mcpclient.Secrets{APIKey: &wrong})
	if res := e.m.Test(ctx, "internal"); res.OK || !strings.Contains(res.Error, "rejected") || res.Status != mcpclient.StatusError {
		t.Errorf("wrong key: %+v", res)
	}
	key := "k-123"
	_ = e.m.SetSecrets("internal", mcpclient.Secrets{APIKey: &key})
	res := e.m.Test(ctx, "internal")
	if !res.OK || res.ToolCount != 4 || res.Status != mcpclient.StatusConnected || res.Transport != "streamable_http" {
		t.Fatalf("test: %+v", res)
	}
	out, err := e.m.Call(ctx, "internal", "echo", map[string]any{"message": "hi"})
	if err != nil || out.IsError || string(out.Structured) != `{"echo":{"message":"hi"}}` {
		t.Fatalf("call: %+v %v", out, err)
	}
	if c := srv.Calls(); len(c) != 1 || c[0].Header != "k-123" || c[0].Auth != "" {
		t.Errorf("the server saw %+v", c)
	}
}

func TestCallResults(t *testing.T) {
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Open})
	defer srv.Close()
	e := newEnv(t, "open", spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthNone})
	ctx := context.Background()
	if r, err := e.m.Call(ctx, "open", "plain", nil); err != nil || r.Text != "line one\nline two" || len(r.Structured) != 0 || r.IsError {
		t.Errorf("plain: %+v %v", r, err)
	}
	if r, err := e.m.Call(ctx, "open", "fail", nil); err != nil || !r.IsError || r.Text != "it broke" {
		t.Errorf("tool error: %+v %v", r, err)
	}
	if _, err := e.m.Call(ctx, "open", "no-such-tool", nil); err == nil {
		t.Error("an unknown tool was not an error")
	}
	if _, err := e.m.Call(ctx, "ghost", "plain", nil); !errors.Is(err, mcpclient.ErrUnknownServer) {
		t.Errorf("unknown server: %v", err)
	}
}

func TestTokenIsRefreshedBeforeExpiryAndOnRejection(t *testing.T) {
	srv := mcptest.New(mcptest.Options{AccessTTL: 3600})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))
	e.connect("notion")
	ctx := context.Background()
	refreshes := func() int {
		n := 0
		for _, f := range srv.TokenRequests() {
			if f.Get("grant_type") == "refresh_token" {
				n++
			}
		}
		return n
	}

	if _, err := e.m.Call(ctx, "notion", "plain", nil); err != nil || refreshes() != 0 {
		t.Fatalf("fresh token: %v, %d refreshes", err, refreshes())
	}
	e.advance(59*time.Minute + 30*time.Second) // inside the 60 s skew
	if _, err := e.m.Call(ctx, "notion", "plain", nil); err != nil || refreshes() != 1 {
		t.Fatalf("expiring token: %v, %d refreshes", err, refreshes())
	}
	if last := srv.TokenRequests()[len(srv.TokenRequests())-1]; last.Get("resource") != srv.URL || last.Get("refresh_token") == "" {
		t.Errorf("refresh request: %v", last)
	}

	// The server stops accepting the token before it expires: one refresh, one retry, the caller notices nothing.
	srv.ExpireAccessTokens()
	if _, err := e.m.Call(ctx, "notion", "plain", nil); err != nil || refreshes() != 2 {
		t.Fatalf("rejected token: %v, %d refreshes", err, refreshes())
	}
	if len(e.notes.all()) != 0 {
		t.Errorf("notes after healthy refreshes: %+v", e.notes.all())
	}
}

func TestRefreshFailureNeedsReconnectAndPostsOneInboxNote(t *testing.T) {
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))
	e.connect("notion")
	ctx := context.Background()

	srv.RevokeRefreshTokens()
	if _, err := e.m.Call(ctx, "notion", "plain", nil); !errors.Is(err, mcpclient.ErrNeedsReconnect) {
		t.Fatalf("call: %v", err)
	}
	v, _ := e.m.Server("notion")
	if v.Status != mcpclient.StatusNeedsReconnect || v.Error == "" {
		t.Errorf("view: %+v", v)
	}
	ns := e.notes.all()
	if len(ns) != 1 || ns[0].Kind != "reconnect" || ns[0].Server != "notion" || ns[0].LinkLabel != "Reconnect" || ns[0].LinkPath != "/settings/context?mcp_server=notion" || ns[0].Priority != "high" {
		t.Fatalf("notes: %+v", ns)
	}
	// Failing again does not post again; reconnecting clears the state.
	_, _ = e.m.Call(ctx, "notion", "plain", nil)
	if n := len(e.notes.all()); n != 1 {
		t.Errorf("%d notes after a second failure", n)
	}
	e.connect("notion")
	if v, _ := e.m.Server("notion"); v.Status != mcpclient.StatusConnected || v.Error != "" {
		t.Errorf("after reconnect: %+v", v)
	}
	if _, err := e.m.Call(ctx, "notion", "plain", nil); err != nil {
		t.Errorf("call after reconnect: %v", err)
	}
}

func TestDisconnectDropsTheTokens(t *testing.T) {
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))
	e.connect("notion")
	if err := e.m.Disconnect("notion"); err != nil {
		t.Fatal(err)
	}
	v, _ := e.m.Server("notion")
	if v.Status != mcpclient.StatusDisconnected || v.TokenExpiresAt != nil {
		t.Errorf("view: %+v", v)
	}
	if _, err := e.m.Call(context.Background(), "notion", "plain", nil); !errors.Is(err, mcpclient.ErrNotConnected) {
		t.Errorf("call: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(e.home, "data", "mcp", "notion.secrets.json"))
	if strings.Contains(string(b), "access-") || strings.Contains(string(b), "refresh-") {
		t.Errorf("tokens left in the secrets file: %s", b)
	}
	apiKey := spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthNone}
	e2 := newEnv(t, "x", apiKey)
	if err := e2.m.Disconnect("x"); !errors.Is(err, mcpclient.ErrNotOAuth) {
		t.Errorf("disconnect of a server without OAuth: %v", err)
	}
}

func TestToolRefreshReportsChangesAndPostsANoteOnlyWhenTheListChanged(t *testing.T) {
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Open})
	defer srv.Close()
	e := newEnv(t, "open", spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthNone})
	ctx := context.Background()

	if _, _, err := e.m.RefreshTools(ctx, "open"); err != nil { // nothing to compare with: no note
		t.Fatalf("first refresh: %v", err)
	}
	if _, ch, _ := e.m.RefreshTools(ctx, "open"); ch.Any() {
		t.Errorf("an unchanged list reported %+v", ch)
	}
	if len(e.notes.all()) != 0 {
		t.Fatalf("notes without a change: %+v", e.notes.all())
	}

	tools := mcptest.DefaultTools()
	tools[0].Description = "Return the arguments, politely." // changed
	tools = append(tools[:1], tools[2:]...)                  // plain removed
	tools = append(tools, mcptest.Tool{Name: "create-page", Description: "Create a page."})
	srv.SetTools(tools)
	d, ch, err := e.m.RefreshTools(ctx, "open")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ch.Added, ",") != "create-page" || strings.Join(ch.Removed, ",") != "plain" || strings.Join(ch.Changed, ",") != "echo" || d.ToolCount != 4 {
		t.Fatalf("changes: %+v tools %d", ch, d.ToolCount)
	}
	ns := e.notes.all()
	if len(ns) != 1 || ns[0].Kind != "tools" || ns[0].Priority != "low" || !strings.Contains(ns[0].BodyMD, "`create-page`") || !strings.Contains(ns[0].BodyMD, "`plain`") || !strings.Contains(ns[0].BodyMD, "`echo`") {
		t.Fatalf("notes: %+v", ns)
	}
	if got, _, _ := e.m.Tools("open"); len(got) != 4 || got[0].Name != "create-page" {
		t.Errorf("cache: %+v", got)
	}
}

func TestNoSecretIsEverInAViewOrTheConfig(t *testing.T) {
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e := newEnv(t, "notion", oauthDef(srv))
	e.connect("notion")
	e.cfg["keyed"] = spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthHeader, Header: "X-Api-Key"}
	key := "top-secret-key"
	_ = e.m.SetSecrets("keyed", mcpclient.Secrets{APIKey: &key})

	b, _ := json.Marshal(e.m.List())
	d, _ := e.m.Detail("notion")
	db, _ := json.Marshal(d)
	all := string(b) + string(db)
	for _, secret := range []string{"top-secret-key", "access-", "refresh-", "dyn-client", "code_verifier"} {
		if strings.Contains(all, secret) {
			t.Errorf("a view contains %q:\n%s", secret, all)
		}
	}
	if v, _ := e.m.Server("keyed"); !v.HasAPIKey {
		t.Error("has_api_key is false for a stored key")
	}
	cfg, _ := json.Marshal(e.cfg)
	if strings.Contains(string(cfg), "top-secret-key") {
		t.Errorf("config holds a secret: %s", cfg)
	}
}

func TestRunTokenGrantsOnlyDeclaredServersAndLogsCalls(t *testing.T) {
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Open})
	defer srv.Close()
	e := newEnv(t, "open", spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthNone})
	e.cfg["other"] = spec.RemoteMCP{URL: srv.URL, Auth: spec.MCPAuthNone}
	ctx := context.Background()

	acc, err := e.m.GrantScript("run1", "s1", []string{"open"}, time.Minute, "http://127.0.0.1:9001", "/tmp/not-hal", "/usr/bin")
	if err != nil || acc == nil {
		t.Fatal(err)
	}
	tok := acc.Env[mcpclient.EnvRunToken]
	g, ok := e.m.LookupRunToken(tok)
	if !ok || acc.Env[mcpclient.EnvURL] != "http://127.0.0.1:9001" {
		t.Fatalf("token lookup: %v %v", ok, acc.Env)
	}
	if _, err := e.m.CallWith(ctx, g, "open", "echo", map[string]any{"message": "x"}); err != nil {
		t.Fatalf("declared server: %v", err)
	}
	before := len(srv.Calls())
	if _, err := e.m.CallWith(ctx, g, "other", "echo", nil); !errors.Is(err, mcpclient.ErrNotAllowed) {
		t.Fatalf("undeclared server: %v", err)
	}
	if len(srv.Calls()) != before {
		t.Error("the undeclared server was contacted")
	}
	if r, err := e.m.CallWith(ctx, g, "open", "fail", nil); err != nil || !r.IsError {
		t.Fatalf("tool error: %+v %v", r, err)
	}
	calls := acc.Calls()
	if len(calls) != 3 || !calls[0].OK || calls[1].OK || calls[1].Server != "other" || calls[2].OK || calls[2].Tool != "fail" || calls[0].Tool != "echo" {
		t.Errorf("call log: %+v", calls)
	}
	acc.Revoke()
	if _, ok := e.m.LookupRunToken(tok); ok {
		t.Error("a revoked token still works")
	}
	if _, ok := e.m.LookupRunToken("hal_run_forged"); ok {
		t.Error("a forged token works")
	}
	if none, err := e.m.GrantScript("run2", "s2", nil, time.Minute, "", "", ""); err != nil || none != nil {
		t.Errorf("a sensor without mcp_servers got access: %v %v", none, err)
	}
}
