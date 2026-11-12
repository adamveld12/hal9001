package api

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/mcpclient"
	"hal9001/internal/mcpclient/mcptest"
	"hal9001/internal/spec"
)

func withMCPClient(o *envOptions) {
	o.mcpClient = func(e *env) *mcpclient.Manager {
		return mcpclient.New(mcpclient.Options{
			Home: e.home, Version: "test",
			Servers: func() map[string]spec.RemoteMCP { return e.ws.Load().Config.Value.MCPServers },
		})
	}
}

func readHome(t *testing.T, e *env, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMCPServerDefinitionsLiveInConfigAndSecretsNeverLeave(t *testing.T) {
	e := newEnv(t, withMCPClient)
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Header, Header: "X-Api-Key", HeaderValue: "k-123"})
	defer srv.Close()
	before := readHome(t, e, "config.yaml")

	e.do("POST", "/api/mcp-servers", map[string]any{"name": "Bad Name", "url": srv.URL}).want(422)
	e.do("POST", "/api/mcp-servers", map[string]any{"name": "notion", "url": "http://mcp.example.com/mcp"}).want(422)
	e.do("POST", "/api/mcp-servers", map[string]any{"name": "notion", "url": srv.URL, "auth": "none", "api_key": "x"}).want(422)

	r := e.do("POST", "/api/mcp-servers", map[string]any{
		"name": "internal", "url": srv.URL, "auth": "header", "header": "X-Api-Key", "api_key": "k-123",
	}).want(201)
	d := r.json()
	if d["has_api_key"] != true || d["status"] != "disconnected" && d["status"] != "unknown" && d["status"] != "connected" {
		t.Errorf("created: %v", d)
	}
	if strings.Contains(string(r.Body), "k-123") {
		t.Errorf("the response holds the key: %s", r.Body)
	}
	e.do("POST", "/api/mcp-servers", map[string]any{"name": "internal", "url": srv.URL}).want(409)

	cfg := readHome(t, e, "config.yaml")
	if !strings.Contains(cfg, "mcp_servers:") || !strings.Contains(cfg, "internal:") || !strings.Contains(cfg, "header: X-Api-Key") || strings.Contains(cfg, "k-123") {
		t.Errorf("config.yaml:\n%s", cfg)
	}
	if !strings.HasPrefix(cfg, strings.TrimRight(before, "\n")[:40]) {
		t.Errorf("the existing config was rewritten:\n%s", cfg[:200])
	}
	secrets := readHome(t, e, "data/mcp/internal.secrets.json")
	if !strings.Contains(secrets, "k-123") {
		t.Errorf("the key is not in the secrets store: %s", secrets)
	}
	if info, _ := os.Stat(filepath.Join(e.home, "data", "mcp", "internal.secrets.json")); info == nil || info.Mode().Perm() != 0o600 {
		t.Errorf("secrets file mode: %v", info)
	}

	for _, p := range []string{"/api/mcp-servers", "/api/mcp-servers/internal", "/api/settings"} {
		if body := e.get(p).want(200).Body; strings.Contains(string(body), "k-123") {
			t.Errorf("GET %s returned the key", p)
		}
	}
	if items := e.get("/api/mcp-servers").json()["items"].([]any); len(items) != 1 {
		t.Errorf("list: %v", items)
	}

	// Update keeps the stored key when it is omitted and removes it when it is sent empty.
	e.do("PUT", "/api/mcp-servers/internal", map[string]any{"url": srv.URL, "auth": "header", "header": "X-Other"}).want(200)
	if v := e.get("/api/mcp-servers/internal").json(); v["has_api_key"] != true || v["header"] != "X-Other" {
		t.Errorf("after update: %v", v)
	}
	e.do("PUT", "/api/mcp-servers/internal", map[string]any{"url": srv.URL, "auth": "header", "header": "X-Other", "api_key": ""}).want(200)
	if v := e.get("/api/mcp-servers/internal").json(); v["has_api_key"] != false {
		t.Errorf("after removing the key: %v", v)
	}
	e.do("PUT", "/api/mcp-servers/ghost", map[string]any{"url": srv.URL}).want(404)

	e.do("DELETE", "/api/mcp-servers/internal", nil).want(204)
	e.get("/api/mcp-servers/internal").want(404)
	if _, err := os.Stat(filepath.Join(e.home, "data", "mcp", "internal.secrets.json")); !os.IsNotExist(err) {
		t.Errorf("secrets survived the removal: %v", err)
	}
	if strings.Contains(readHome(t, e, "config.yaml"), "mcp_servers") {
		t.Error("the empty mcp_servers section was left in config.yaml")
	}
}

func TestRemovingAServerASensorUsesIsRefusedAndSensorsNeedExistingServers(t *testing.T) {
	e := newEnv(t, withMCPClient)
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Open})
	defer srv.Close()
	e.do("POST", "/api/mcp-servers", map[string]any{"name": "notion", "url": srv.URL, "auth": "none"}).want(201)

	body := "lang: bash\nscript: echo '[]'\nkey: '{{.id}}'\nmcp_servers: [ghost]\n"
	r := e.do("POST", "/api/sensors", map[string]any{"id": "reader", "yaml": body}).want(422)
	if !strings.Contains(string(r.Body), "unknown_mcp_server") {
		t.Errorf("want unknown_mcp_server: %s", r.Body)
	}
	body = "lang: bash\nscript: echo '[]'\nkey: '{{.id}}'\nmcp_servers: [notion]\n"
	created := e.do("POST", "/api/sensors", map[string]any{"id": "reader", "yaml": body}).want(201).json()

	r = e.do("DELETE", "/api/mcp-servers/notion", nil).want(409)
	if !strings.Contains(string(r.Body), "reader") {
		t.Errorf("the problem does not name the sensor: %s", r.Body)
	}
	e.do("DELETE", "/api/sensors/reader?expected_hash="+created["hash"].(string), nil).want(204)
	e.do("DELETE", "/api/mcp-servers/notion", nil).want(204)
}

func TestOAuthConnectCallbackAndTools(t *testing.T) {
	e := newEnv(t, withMCPClient)
	srv := mcptest.New(mcptest.Options{})
	defer srv.Close()
	e.do("POST", "/api/mcp-servers", map[string]any{"name": "notion", "url": srv.URL}).want(201)
	ctx := context.Background()

	e.do("POST", "/api/mcp-servers/ghost/connect", nil).want(404)
	res := e.do("POST", "/api/mcp-servers/notion/connect", nil).want(200).json()
	authURL, _ := res["authorize_url"].(string)
	if !strings.HasPrefix(authURL, srv.Base+"/authorize?") {
		t.Fatalf("authorize_url: %q", authURL)
	}

	// A forged state, in the API shape and for a browser.
	if p := e.get("/oauth/callback?state=forged&code=x").want(400).problem(); p != "bad-request" {
		t.Errorf("problem: %s", p)
	}
	req := request("GET", "/oauth/callback?state=forged&code=x", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || strings.Contains(rec.Body.String(), "forged") {
		t.Errorf("browser answer: %d %s %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}

	cb, err := mcptest.Approve(ctx, authURL)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(cb)
	r := e.get("/oauth/callback?" + u.RawQuery).want(302)
	if loc := r.Header.Get("Location"); loc != "/settings/context?mcp_server=notion" {
		t.Errorf("redirect: %q", loc)
	}
	if r.Header.Get("Cache-Control") != "no-store" || strings.Contains(r.Header.Get("Location"), "code=") {
		t.Errorf("headers: %v", r.Header)
	}
	// The same redirect again (a reload, a replay) is refused.
	e.get("/oauth/callback?" + u.RawQuery).want(400)

	d := e.get("/api/mcp-servers/notion").want(200).json()
	if d["status"] != "connected" || d["tool_count"] != float64(4) || len(d["tools"].([]any)) != 4 {
		t.Fatalf("detail: %v", d)
	}
	for _, body := range []string{string(e.get("/api/mcp-servers").Body), string(e.get("/api/mcp-servers/notion").Body), readHome(t, e, "config.yaml")} {
		for _, secret := range []string{"access-", "refresh-", "dyn-client"} {
			if strings.Contains(body, secret) {
				t.Errorf("%q leaked into an API response or config.yaml", secret)
			}
		}
	}

	tr := e.do("POST", "/api/mcp-servers/notion/test", nil).want(200).json()
	if tr["ok"] != true || tr["tool_count"] != float64(4) {
		t.Errorf("test: %v", tr)
	}
	srv.SetTools(mcptest.DefaultTools()[:2])
	rf := e.do("POST", "/api/mcp-servers/notion/tools/refresh", nil).want(200).json()
	ch := rf["changes"].(map[string]any)
	if len(ch["removed"].([]any)) != 2 || len(rf["server"].(map[string]any)["tools"].([]any)) != 2 {
		t.Errorf("refresh: %v", rf)
	}

	if r := e.do("POST", "/api/mcp-servers/notion/disconnect", nil).want(200).json(); r["status"] != "disconnected" {
		t.Errorf("disconnect: %v", r)
	}
	e.do("POST", "/api/mcp-servers/notion/tools/refresh", nil).want(409)
	e.do("PUT", "/api/mcp-servers/notion", map[string]any{"url": srv.URL, "auth": "none"}).want(200)
	e.do("POST", "/api/mcp-servers/notion/disconnect", nil).want(422)
	e.do("POST", "/api/mcp-servers/notion/connect", nil).want(422)

	dead := mcptest.New(mcptest.Options{})
	deadURL := dead.URL
	dead.Close()
	e.do("POST", "/api/mcp-servers", map[string]any{"name": "dead", "url": deadURL, "auth": "none"}).want(201)
	if tr := e.do("POST", "/api/mcp-servers/dead/test", nil).want(200).json(); tr["ok"] != false || tr["error"] == "" {
		t.Errorf("test of a dead server: %v", tr)
	}
	if p := e.do("POST", "/api/mcp-servers/dead/tools/refresh", nil).want(502).problem(); p != "upstream-error" {
		t.Errorf("problem: %s", p)
	}
}

func TestCallEndpointAcceptsRunTokensForDeclaredServersAndAdminOtherwise(t *testing.T) {
	cfg := lockedConfig()
	var mgr *mcpclient.Manager
	e := newEnv(t, func(o *envOptions) {
		o.auth = &cfg
		o.mcpClient = func(e *env) *mcpclient.Manager {
			mgr = mcpclient.New(mcpclient.Options{Home: e.home, Servers: func() map[string]spec.RemoteMCP { return e.ws.Load().Config.Value.MCPServers }})
			return mgr
		}
	})
	srv := mcptest.New(mcptest.Options{Mode: mcptest.Open})
	defer srv.Close()
	e.writeHome("config.yaml", "mcp_servers:\n  notion:\n    url: "+srv.URL+"\n    auth: none\n  other:\n    url: "+srv.URL+"\n    auth: none\n")
	e.ws.Store(spec.Load(e.home))

	call := func(token, path, body string) *resp {
		req := request("POST", path, []byte(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return serve(t, e.srv, req)
	}
	acc, err := mgr.GrantScript("run-1", "reader", []string{"notion"}, time.Minute, "http://127.0.0.1:9001", "", "/usr/bin")
	if err != nil {
		t.Fatal(err)
	}
	tok := acc.Env[mcpclient.EnvRunToken]

	r := call(tok, "/api/mcp-servers/notion/call", `{"tool":"echo","arguments":{"message":"hi"}}`).want(200).json()
	if r["is_error"] != false || r["structured_content"].(map[string]any)["echo"].(map[string]any)["message"] != "hi" {
		t.Errorf("result: %v", r)
	}
	if r := call(tok, "/api/mcp-servers/notion/call", `{"tool":"plain"}`).want(200).json(); r["text"] != "line one\nline two" {
		t.Errorf("plain: %v", r)
	}
	if r := call(tok, "/api/mcp-servers/notion/call", `{"tool":"fail"}`).want(200).json(); r["is_error"] != true {
		t.Errorf("tool error: %v", r)
	}
	calls := len(srv.Calls())
	if p := call(tok, "/api/mcp-servers/other/call", `{"tool":"echo"}`).want(403).problem(); p != "forbidden" {
		t.Errorf("undeclared server: %s", p)
	}
	if len(srv.Calls()) != calls {
		t.Error("an undeclared server was called")
	}
	if p := call("hal_run_forged", "/api/mcp-servers/notion/call", `{"tool":"echo"}`).want(401).problem(); p != "unauthenticated" {
		t.Errorf("forged token: %s", p)
	}
	// No token: the admin service applies, and the locked stack admits nobody.
	call("", "/api/mcp-servers/notion/call", `{"tool":"echo"}`).want(401)
	if n := len(acc.Calls()); n != 4 {
		t.Errorf("logged calls: %d", n)
	}
	acc.Revoke()
	call(tok, "/api/mcp-servers/notion/call", `{"tool":"echo"}`).want(401)
}
