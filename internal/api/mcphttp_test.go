package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hal9001/internal/auth"
	"hal9001/internal/core"
	"hal9001/internal/httpsec"
	"hal9001/internal/tools"
)

// operatorApprover answers every operator approval the same way.
type operatorApprover struct {
	allow bool
	asked atomic.Int32
}

func (a *operatorApprover) ApproveOperator(context.Context, tools.OperatorApproval) (tools.Decision, error) {
	a.asked.Add(1)
	return tools.Decision{Allowed: a.allow, Comment: "not now"}, nil
}

func newOperatorEnv(t *testing.T, allow bool, mods ...func(*envOptions)) (*env, *operatorApprover) {
	t.Helper()
	host := &operatorApprover{allow: allow}
	op, err := tools.NewOperator(host, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return newEnv(t, append([]func(*envOptions){func(o *envOptions) { o.operator = op }}, mods...)...), host
}

// serveHTTP puts the router behind the daemon's baseline middleware on a real listener, as the daemon does.
func serveHTTP(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	var port int
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpsec.Middleware(httpsec.Options{Port: port})(h).ServeHTTP(w, r)
	}))
	srv.Start()
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if port, err = strconv.Atoi(u.Port()); err != nil {
		t.Fatal(err)
	}
	return srv
}

func connect(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	s, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", endpoint, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestMcpHTTPInitializeListAndCallThroughTheRouter(t *testing.T) {
	e, host := newOperatorEnv(t, true)
	var ranFlow atomic.Value
	e.eng.runFlow = func(_ context.Context, flowID string, opts core.RunFlowOptions) (core.RunFlowResult, error) {
		ranFlow.Store(flowID)
		return core.RunFlowResult{FlowID: flowID}, nil
	}
	srv := serveHTTP(t, e.srv)
	s := connect(t, srv.URL+"/mcp")

	if init := s.InitializeResult(); init == nil || init.ServerInfo.Name != "hal9001" {
		t.Fatalf("initialize result = %+v", init)
	}
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range res.Tools {
		have[tool.Name] = true
	}
	for _, want := range []string{"status", "flows_list", "flow_run", "inbox_list"} {
		if !have[want] {
			t.Errorf("tools/list lacks %q", want)
		}
	}

	// A read goes through the REST API in-process and returns its JSON.
	out, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "status", Arguments: map[string]any{}})
	if err != nil || out.IsError {
		t.Fatalf("status: %v %+v", err, out)
	}
	var status map[string]any
	if err := json.Unmarshal([]byte(textOf(out)), &status); err != nil || status["kill_switch"] == nil {
		t.Fatalf("status text is not the API's response: %q (%v)", textOf(out), err)
	}

	// A tool with side effects asks first and then calls the endpoint with the caller's credentials.
	out, err = s.CallTool(context.Background(), &mcp.CallToolParams{Name: "flow_run", Arguments: map[string]any{"id": "demo"}})
	if err != nil || out.IsError {
		t.Fatalf("flow_run: %v %+v", err, out)
	}
	if got, _ := ranFlow.Load().(string); got != "demo" || host.asked.Load() != 1 {
		t.Errorf("flow ran = %q after %d approval(s)", got, host.asked.Load())
	}
}

func TestMcpHTTPDeniedSideEffectDoesNotRun(t *testing.T) {
	e, host := newOperatorEnv(t, false)
	e.eng.runFlow = func(context.Context, string, core.RunFlowOptions) (core.RunFlowResult, error) {
		t.Error("a denied flow_run reached the engine")
		return core.RunFlowResult{}, nil
	}
	s := connect(t, serveHTTP(t, e.srv).URL+"/mcp")
	out, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "flow_run", Arguments: map[string]any{"id": "demo"}})
	if err != nil || !out.IsError || !strings.Contains(textOf(out), "denied by the user: not now") {
		t.Fatalf("denied flow_run: %v %+v", err, out)
	}
	if host.asked.Load() != 1 {
		t.Errorf("approvals asked = %d", host.asked.Load())
	}
}

// Tools carry the caller's credentials: a stack that admits the MCP request for `api` but not `admin` lets the read
// tools work and refuses flow_run, even after an approval.
func TestMcpToolsAreHeldToTheServiceOfTheEndpointTheyCall(t *testing.T) {
	cfg := lockedConfig()
	cfg.Stacks["local"] = []auth.StackEntry{{Module: "localhost-trust", Control: auth.Sufficient}}
	cfg.Services[auth.ServiceAPI] = "local"
	e, _ := newOperatorEnv(t, true, func(o *envOptions) { o.auth = &cfg })
	e.eng.runFlow = func(context.Context, string, core.RunFlowOptions) (core.RunFlowResult, error) {
		t.Error("flow_run ran without the admin service")
		return core.RunFlowResult{}, nil
	}
	s := connect(t, serveHTTP(t, e.srv).URL+"/mcp")
	if out, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "flows_list", Arguments: map[string]any{}}); err != nil || out.IsError {
		t.Fatalf("flows_list: %v %+v", err, out)
	}
	out, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "flow_run", Arguments: map[string]any{"id": "demo"}})
	if err != nil || !out.IsError || !strings.Contains(textOf(out), "401") {
		t.Fatalf("flow_run without admin: %v %+v", err, out)
	}
}

func TestMcpHTTPRequiresTheAPIAuthService(t *testing.T) {
	cfg := lockedConfig()
	e, _ := newOperatorEnv(t, true, func(o *envOptions) { o.auth = &cfg })
	rec := httptest.NewRecorder()
	req := request(http.MethodPost, "/mcp", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", rec.Code, rec.Body)
	}
	var p map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p["service"] != "api" {
		t.Errorf("problem = %v", p)
	}
}

func TestMcpHTTPRefusesForeignHostOriginPeerAndProxies(t *testing.T) {
	e, _ := newOperatorEnv(t, true)
	post := func(mod func(*http.Request)) *resp {
		req := request(http.MethodPost, "/mcp", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Accept", "application/json, text/event-stream")
		mod(req)
		return serve(t, e.srv, req)
	}
	post(func(*http.Request) {}).want(200)
	post(func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:3000") }).want(200) // another local page is the baseline's call
	for name, mod := range map[string]func(*http.Request){
		"host":      func(r *http.Request) { r.Host = "my-mac.tail1234.ts.net" },
		"origin":    func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"peer":      func(r *http.Request) { r.RemoteAddr = "192.0.2.7:4000" },
		"forwarded": func(r *http.Request) { r.Header.Set("X-Forwarded-For", "100.64.0.9") },
		"tailscale": func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "a@example.com") },
	} {
		if r := post(mod); r.Status != 401 && r.Status != 403 {
			t.Errorf("%s: status %d, want 401 or 403: %s", name, r.Status, r.Body)
		}
	}
}

// loopbackOnly is a second line behind the auth stack: it must hold even when the stack admits the request.
func TestLoopbackOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		remote, host, origin string
		header               map[string]string
		ok                   bool
	}{
		"localhost":        {remote: "127.0.0.1:5", host: "localhost:9001", ok: true},
		"ip":               {remote: "127.0.0.1:5", host: "127.0.0.1:9001", ok: true},
		"ipv6":             {remote: "[::1]:5", host: "[::1]:9001", ok: true},
		"local origin":     {remote: "127.0.0.1:5", host: "localhost:9001", origin: "http://localhost:9001", ok: true},
		"tailscale host":   {remote: "127.0.0.1:5", host: "my-mac.tail1234.ts.net", ok: false},
		"rebinding host":   {remote: "127.0.0.1:5", host: "evil.example:9001", ok: false},
		"foreign origin":   {remote: "127.0.0.1:5", host: "localhost:9001", origin: "https://evil.example", ok: false},
		"origin lookalike": {remote: "127.0.0.1:5", host: "localhost:9001", origin: "http://localhost.evil.example", ok: false},
		"lan peer":         {remote: "192.168.1.20:5", host: "localhost:9001", ok: false},
		"forwarded":        {remote: "127.0.0.1:5", host: "localhost:9001", header: map[string]string{"Forwarded": "for=1.2.3.4"}, ok: false},
		"x-forwarded":      {remote: "127.0.0.1:5", host: "localhost:9001", header: map[string]string{"X-Forwarded-Host": "x"}, ok: false},
		"tailscale header": {remote: "127.0.0.1:5", host: "localhost:9001", header: map[string]string{"Tailscale-User-Login": "a@b"}, ok: false},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.RemoteAddr, req.Host = tc.remote, tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		if got := loopbackOnly(rec, req); got != tc.ok {
			t.Errorf("%s: loopbackOnly = %v, want %v (%d %s)", name, got, tc.ok, rec.Code, rec.Body)
		}
		if !tc.ok && rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, rec.Code)
		}
	}
}

func TestMcpEndpointIsPostOnly(t *testing.T) {
	e, _ := newOperatorEnv(t, true)
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		r := e.raw(method, "/mcp").want(http.StatusMethodNotAllowed)
		if r.Header.Get("Allow") != "POST" {
			t.Errorf("%s /mcp: Allow = %q", method, r.Header.Get("Allow"))
		}
	}
}

func TestMcpInfoReportsTheRunningDaemon(t *testing.T) {
	cfg := lockedConfig()
	cfg.Stacks["local"] = []auth.StackEntry{{Module: "localhost-trust", Control: auth.Sufficient}}
	cfg.Services[auth.ServiceAPI] = "local"
	e, _ := newOperatorEnv(t, true, func(o *envOptions) {
		o.auth, o.port, o.executable = &cfg, 19011, "/Applications/HAL9001.app/Contents/MacOS/hal9001"
	})
	var info struct {
		ServerName string `json:"server_name"`
		HTTPURL    string `json:"http_url"`
		BaseURL    string `json:"base_url"`
		Stdio      struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"stdio"`
		CLILink string `json:"cli_link"`
		Auth    struct {
			LocalhostTrust struct{ API, Admin bool } `json:"localhost_trust"`
			CookieName     string                    `json:"cookie_name"`
		} `json:"auth"`
		ApprovalTimeoutSeconds int `json:"approval_timeout_seconds"`
		Tools                  []struct {
			Name          string `json:"name"`
			NeedsApproval bool   `json:"needs_approval"`
		} `json:"tools"`
	}
	e.get("/api/mcp/info").want(200).decode(&info)
	if info.ServerName != "hal9001" || info.HTTPURL != "http://localhost:19011/mcp" || info.BaseURL != "http://localhost:19011" ||
		info.Stdio.Command != "/Applications/HAL9001.app/Contents/MacOS/hal9001" || strings.Join(info.Stdio.Args, " ") != "mcp --url http://localhost:19011" ||
		info.CLILink != "~/.local/bin/hal9001" || info.Auth.CookieName != "hal9001_session" || info.ApprovalTimeoutSeconds != 60 {
		t.Errorf("info = %+v", info)
	}
	if !info.Auth.LocalhostTrust.API || info.Auth.LocalhostTrust.Admin {
		t.Errorf("localhost trust = %+v, want api only", info.Auth.LocalhostTrust)
	}
	approvals := map[string]bool{}
	for _, tl := range info.Tools {
		approvals[tl.Name] = tl.NeedsApproval
	}
	if !approvals["flow_run"] || !approvals["sensor_poll"] || approvals["flows_list"] || len(approvals) < 10 {
		t.Errorf("tools = %v", approvals)
	}
}
