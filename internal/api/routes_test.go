package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"hal9001/internal/apispec"
	"hal9001/internal/auth"
	"hal9001/internal/httpsec"
	"hal9001/internal/store"
)

var paramRE = regexp.MustCompile(`\{[^}.]*(\.\.\.)?\}`)

// samplePath fills the parameters of a contract path or mux pattern with
// values that route like real ones.
func samplePath(p string) string {
	p = strings.ReplaceAll(p, "/{$}", "/")
	return paramRE.ReplaceAllStringFunc(p, func(m string) string {
		if strings.HasSuffix(m, "...}") {
			return "app.js"
		}
		return "x1"
	})
}

// operations lists every "METHOD path" of the contract.
func operations(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for path, item := range apispec.MustLoad().Paths.Map() {
		for method := range item.Operations() {
			out[method+" "+path] = true
		}
	}
	if len(out) < 70 {
		t.Fatalf("contract lists only %d operations", len(out))
	}
	return out
}

func TestEveryContractOperationHasARouteAndEveryRouteIsInTheContract(t *testing.T) {
	e := newEnv(t)
	registered := map[string]bool{}
	for _, r := range e.srv.routes {
		registered[r] = true
	}
	for op := range operations(t) {
		method, path, _ := strings.Cut(op, " ")
		if strings.HasPrefix(path, "/api/auth/") {
			// Mounted by auth.Manager.Routes: the mux must dispatch it to a
			// pattern of its own, not to the catch-all.
			_, pattern := e.srv.mux.Handler(httptest.NewRequest(method, samplePath(path), nil))
			if pattern == "" || pattern == "/" || !strings.HasPrefix(pattern, method+" ") {
				t.Errorf("%s has no auth route (mux pattern %q)", op, pattern)
			}
			continue
		}
		if !registered[method+" "+muxPath(path)] {
			t.Errorf("%s is in the contract but has no route", op)
		}
	}
	for _, route := range e.srv.routes {
		method, pattern, _ := strings.Cut(route, " ")
		if op, _ := apispec.FindOperation(method, samplePath(pattern)); op == nil {
			t.Errorf("route %q is registered but is not in the contract", route)
		}
	}
}

// raw sends a request without checking the response against the contract,
// for paths the contract does not describe.
func (e *env) raw(method, path string) *resp {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, request(method, path, nil))
	return &resp{t: e.t, Status: rec.Code, Header: rec.Result().Header, Body: rec.Body.Bytes()}
}

func TestUnknownAPIPathsAreProblemsNotTheUI(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ui")) })
	e := newEnv(t, func(o *envOptions) { o.ui = ui })
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/nope"}, {"GET", "/api"}, {"DELETE", "/api/health"}, {"POST", "/elsewhere"},
	} {
		r := e.raw(tc.method, tc.path).want(404)
		if r.problem() != "not-found" {
			t.Errorf("%s %s: problem %q", tc.method, tc.path, r.problem())
		}
	}
}

func TestUIRoutesDelegateToTheUIHandler(t *testing.T) {
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("ui:" + r.URL.Path))
	})
	e := newEnv(t, func(o *envOptions) { o.ui = ui })
	if r := e.get("/").want(200); string(r.Body) != "ui:/" {
		t.Errorf("/ body = %q", r.Body)
	}
	if r := e.get("/assets/app.js").want(200); string(r.Body) != "ui:/assets/app.js" {
		t.Errorf("asset body = %q", r.Body)
	}
	if r := e.raw("GET", "/assets/chunks/a.js").want(200); string(r.Body) != "ui:/assets/chunks/a.js" {
		t.Errorf("nested asset body = %q", r.Body)
	}
	// Client-side routes reload into the same handler.
	if r := e.raw("GET", "/inbox/01JC7Z3K9Q8V5T2M4N6P8R0S1T").want(200); string(r.Body) != "ui:/inbox/01JC7Z3K9Q8V5T2M4N6P8R0S1T" {
		t.Errorf("spa body = %q", r.Body)
	}
}

func TestMissingUIIsExplainedByAProblem(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/", "/assets/app.js"} {
		r := e.get(p).want(404)
		if r.problem() != "not-found" || !strings.Contains(string(r.Body), "UI is not built") {
			t.Errorf("GET %s: %s", p, r.Body)
		}
	}
	e.raw("GET", "/inbox").want(404)
}

// lockedConfig requires a password for every service: with no credentials
// presented nothing authenticates.
func lockedConfig() auth.Config {
	cfg := auth.DefaultConfig()
	cfg.Stacks = map[string][]auth.StackEntry{"locked": {{Module: "password", Control: auth.Sufficient}}}
	for _, svc := range auth.Services {
		cfg.Services[svc] = "locked"
	}
	return cfg
}

func TestEveryGuardedOperationRequiresItsOwnService(t *testing.T) {
	cfg := lockedConfig()
	e := newEnv(t, func(o *envOptions) { o.auth = &cfg })
	checked := 0
	for op := range operations(t) {
		method, path, _ := strings.Cut(op, " ")
		sample := samplePath(path)
		if strings.HasPrefix(path, "/api/auth/") || apispec.IsPublic(method, sample) {
			continue
		}
		want := apispec.ServiceFor(method, sample)
		r := serve(t, e.srv, request(method, sample, []byte("{}")))
		if r.Status != 401 || r.problem() != "unauthenticated" {
			t.Errorf("%s: status %d, want 401 unauthenticated: %s", op, r.Status, r.Body)
			continue
		}
		if got := r.json()["service"]; got != want {
			t.Errorf("%s: guarded by service %v, contract says %s", op, got, want)
		}
		checked++
	}
	if checked < 55 {
		t.Fatalf("only %d guarded operations were checked", checked)
	}
	e.get("/api/health").want(200)
}

func TestLocalhostTrustIsPerServiceStack(t *testing.T) {
	cfg := lockedConfig()
	cfg.Stacks["local"] = []auth.StackEntry{{Module: "localhost-trust", Control: auth.Sufficient}}
	cfg.Services[auth.ServiceAPI] = "local"
	var paused atomic.Bool
	e := newEnv(t, func(o *envOptions) { o.auth = &cfg })
	e.eng.pause = func(_ context.Context, _ bool, _ string) (store.KillSwitch, error) {
		paused.Store(true)
		return store.KillSwitch{}, nil
	}
	e.get("/api/flows").want(200)
	r := e.do("POST", "/api/killswitch/pause", "{}").want(401)
	if r.json()["service"] != "admin" {
		t.Errorf("admin stack was not consulted: %s", r.Body)
	}
	if paused.Load() {
		t.Error("the engine ran an admin action for an unauthenticated caller")
	}
}

func TestPasswordLoginSessionCoversTheServices(t *testing.T) {
	hash, err := auth.HashPassword("correct horse")
	must(t, err)
	cfg := lockedConfig()
	cfg.PasswordHash = hash
	e := newEnv(t, func(o *envOptions) { o.auth = &cfg })
	e.eng.resume = func(_ context.Context, principal string) (store.KillSwitch, error) {
		if principal != "owner" {
			t.Errorf("principal = %q, want the logged-in owner", principal)
		}
		return store.KillSwitch{}, nil
	}
	e.get("/api/flows").want(401)

	login := e.do("POST", "/api/auth/api/begin", `{"method":"password","password":"wrong"}`)
	login.want(401)
	login = e.do("POST", "/api/auth/api/begin", `{"method":"password","password":"correct horse"}`).want(200)
	cookie := login.Header.Get("Set-Cookie")
	if !strings.HasPrefix(cookie, auth.SessionCookie+"=") {
		t.Fatalf("no session cookie: %q", cookie)
	}
	req := request("GET", "/api/flows", nil)
	req.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
	serve(t, e.srv, req).want(200)

	req = request("POST", "/api/killswitch/resume", []byte("{}"))
	req.Header.Set("Cookie", strings.SplitN(cookie, ";", 2)[0])
	serve(t, e.srv, req).want(200)
}

// Scenario 18: the baseline sits in front of the API.
func TestBaselineRejectsCrossOriginTextPlainAndRebinding(t *testing.T) {
	e := newEnv(t)
	var paused atomic.Bool
	e.eng.pause = func(_ context.Context, _ bool, _ string) (store.KillSwitch, error) {
		paused.Store(true)
		return store.KillSwitch{}, nil
	}
	h := httpsec.Middleware(httpsec.Options{})(e.srv)
	send := func(mod func(*http.Request)) *resp {
		req := request("POST", "/api/killswitch/pause", []byte("{}"))
		mod(req)
		return serve(t, h, req)
	}
	cases := []struct {
		name string
		mod  func(*http.Request)
		want int
		typ  string
	}{
		{"cross-origin POST", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403, "origin-not-allowed"},
		{"text/plain POST", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415, "unsupported-media-type"},
		{"DNS rebinding", func(r *http.Request) { r.Host = "evil.example" }, 403, "host-not-allowed"},
	}
	for _, tc := range cases {
		r := send(tc.mod)
		if r.Status != tc.want || r.problem() != tc.typ {
			t.Errorf("%s: %d %s, want %d %s", tc.name, r.Status, r.Body, tc.want, tc.typ)
		}
	}
	if paused.Load() {
		t.Fatal("a rejected request reached the engine")
	}
	send(func(*http.Request) {}).want(200)
	if !paused.Load() {
		t.Fatal("a plain local request did not reach the engine")
	}
}
