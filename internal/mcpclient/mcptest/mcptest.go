// Package mcptest is a fake remote MCP server with a fake OAuth authorization server in front of it, for tests and
// screenshots (never real services or accounts). One httptest server hosts both: the MCP endpoint at /mcp, protected
// resource metadata, authorization server metadata, dynamic client registration, authorize and token endpoints.
package mcptest

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Mode is how the MCP endpoint authenticates callers.
type Mode int

// The modes.
const (
	// OAuth: a bearer token issued by the fake authorization server; anything else is a 401 with a challenge.
	OAuth Mode = iota
	// Header: the request must carry Header = HeaderValue.
	Header
	// Open: no authentication.
	Open
)

// Options configures New.
type Options struct {
	Mode Mode
	// Header and HeaderValue are the key header of Mode Header.
	Header, HeaderValue string
	// NoDCR removes the registration endpoint from the authorization server metadata.
	NoDCR bool
	// Client, when set, is the only client id the authorization server accepts (a pasted client); ClientSecret is
	// required at the token endpoint when set.
	Client, ClientSecret string
	// AccessTTL is the lifetime of an access token in seconds (default 3600).
	AccessTTL int
	// NoRotate keeps the refresh token the same on refresh.
	NoRotate bool
	// Scope is announced in the challenge and the resource metadata (default "read write").
	Scope string
}

// Tool is a tool of the fake server.
type Tool struct {
	Name, Description string
	// Schema is the input schema; nil means an empty object schema.
	Schema map[string]any
	// Handler returns the result; nil echoes the arguments as structured content.
	Handler func(args map[string]any) *mcp.CallToolResult
}

// Call is a tool call the server received.
type Call struct {
	Tool string
	Args map[string]any
	// Auth is the Authorization header the call carried.
	Auth string
	// Header is the value of Options.Header, when the call carried it.
	Header string
}

// Server is the fake. Its fields are guarded by mu; use the methods.
type Server struct {
	// URL is the MCP endpoint (http://127.0.0.1:port/mcp); Base the origin and the issuer.
	URL, Base string
	opt       Options
	srv       *httptest.Server

	mu       sync.Mutex
	tools    []Tool
	access   map[string]string // access token -> refresh token it belongs to
	refresh  map[string]string // refresh token -> client id
	codes    map[string]*authCode
	clients  map[string]string // registered client id -> redirect
	calls    []Call
	regs     int
	tokenReq []url.Values
	authReq  []url.Values
	failNext string // failing refresh: the error code to answer once
	now      int
}

type authCode struct {
	client, redirect, challenge, resource string
	used                                  bool
}

// New starts the fake. Close stops it.
func New(opt Options) *Server {
	if opt.AccessTTL == 0 {
		opt.AccessTTL = 3600
	}
	if opt.Scope == "" {
		opt.Scope = "read write"
	}
	s := &Server{opt: opt, access: map[string]string{}, refresh: map[string]string{}, codes: map[string]*authCode{}, clients: map[string]string{}}
	s.tools = DefaultTools()
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.serveMCP)
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", s.resourceMeta)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.asMeta)
	mux.HandleFunc("/register", s.register)
	mux.HandleFunc("/authorize", s.authorize)
	mux.HandleFunc("/token", s.token)
	s.srv = httptest.NewServer(mux)
	s.Base = s.srv.URL
	s.URL = s.srv.URL + "/mcp"
	return s
}

// Close stops the server.
func (s *Server) Close() { s.srv.Close() }

// DefaultTools are the tools a new server has: echo (structured result), plain (text only), fail (a tool error) and a
// search tool with a nested schema.
func DefaultTools() []Tool {
	return []Tool{
		{Name: "echo", Description: "Return the arguments as structured content.", Schema: map[string]any{
			"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}},
		}},
		{Name: "plain", Description: "Return a text answer without structured content.", Handler: func(map[string]any) *mcp.CallToolResult {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "line one"}, &mcp.TextContent{Text: "line two"}}}
		}},
		{Name: "fail", Description: "Always report a tool error.", Handler: func(map[string]any) *mcp.CallToolResult {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "it broke"}}}
		}},
		{Name: "search", Description: "Search the workspace.", Schema: map[string]any{
			"type": "object", "required": []any{"query"},
			"properties": map[string]any{
				"query":   map[string]any{"type": "string", "description": "Words to look for."},
				"filters": map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []any{"page", "database"}}}},
			},
		}},
	}
}

// SetTools replaces the tool list (the next request sees it).
func (s *Server) SetTools(t []Tool) {
	s.mu.Lock()
	s.tools = t
	s.mu.Unlock()
}

// Calls returns the tool calls received so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Call(nil), s.calls...)
}

// Registrations is how many clients registered dynamically.
func (s *Server) Registrations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.regs
}

// TokenRequests returns the form of every token request (grant_type, resource, verifier, ...), in order.
func (s *Server) TokenRequests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.tokenReq...)
}

// AuthRequests returns the query of every authorization request.
func (s *Server) AuthRequests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.authReq...)
}

// ExpireAccessTokens makes every issued access token unacceptable at the MCP endpoint (a 401), as if it had expired;
// refresh tokens keep working.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	s.access = map[string]string{}
	s.mu.Unlock()
}

// RevokeRefreshTokens makes every refresh token invalid_grant, and expires the access tokens.
func (s *Server) RevokeRefreshTokens() {
	s.mu.Lock()
	s.access, s.refresh = map[string]string{}, map[string]string{}
	s.mu.Unlock()
}

// Approve plays the user's browser: it opens the authorization URL without following the redirect and returns where the
// authorization server sent the user back to (the daemon's callback with code and state).
func Approve(ctx context.Context, authorizeURL string) (callback string, err error) {
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizeURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		return "", fmt.Errorf("authorize answered %d", resp.StatusCode)
	}
	return resp.Header.Get("Location"), nil
}

func (s *Server) validAccess(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.access[token]
	return ok
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	switch s.opt.Mode {
	case OAuth:
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" || !s.validAccess(tok) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, s.Base+"/.well-known/oauth-protected-resource/mcp", s.opt.Scope))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	case Header:
		if r.Header.Get(s.opt.Header) != s.opt.HeaderValue {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.build(r) }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	h.ServeHTTP(w, r)
}

// build makes the MCP server of one request from the current tool list.
func (s *Server) build(r *http.Request) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-notion", Version: "1.2.3"}, nil)
	s.mu.Lock()
	tools := append([]Tool(nil), s.tools...)
	s.mu.Unlock()
	for _, t := range tools {
		schema := t.Schema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: schema}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args map[string]any
			if len(req.Params.Arguments) > 0 {
				_ = json.Unmarshal(req.Params.Arguments, &args)
			}
			s.mu.Lock()
			s.calls = append(s.calls, Call{Tool: t.Name, Args: args, Auth: r.Header.Get("Authorization"), Header: r.Header.Get(s.opt.Header)})
			s.mu.Unlock()
			if t.Handler != nil {
				return t.Handler(args), nil
			}
			b, _ := json.Marshal(map[string]any{"echo": args})
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: map[string]any{"echo": args}}, nil
		})
	}
	return srv
}

func (s *Server) resourceMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"resource": s.URL, "authorization_servers": []string{s.Base}, "scopes_supported": strings.Fields(s.opt.Scope),
	})
}

func (s *Server) asMeta(w http.ResponseWriter, _ *http.Request) {
	m := map[string]any{
		"issuer": s.Base, "authorization_endpoint": s.Base + "/authorize", "token_endpoint": s.Base + "/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"},
		"scopes_supported":                      strings.Fields(s.opt.Scope),
	}
	if !s.opt.NoDCR {
		m["registration_endpoint"] = s.Base + "/register"
	}
	writeJSON(w, m)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		GrantTypes   []string `json:"grant_types"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.RedirectURIs) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	s.mu.Lock()
	s.regs++
	id := fmt.Sprintf("dyn-client-%d", s.regs)
	s.clients[id] = req.RedirectURIs[0]
	s.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{
		"client_id": id, "redirect_uris": req.RedirectURIs, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	})
}

func (s *Server) knownClient(id, redirect string) bool {
	if s.opt.Client != "" {
		return id == s.opt.Client
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	want, ok := s.clients[id]
	return ok && want == redirect
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.authReq = append(s.authReq, q)
	s.mu.Unlock()
	switch {
	case q.Get("response_type") != "code", q.Get("code_challenge_method") != "S256", q.Get("code_challenge") == "", q.Get("state") == "":
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	case !s.knownClient(q.Get("client_id"), q.Get("redirect_uri")):
		http.Error(w, "unauthorized_client", http.StatusBadRequest)
		return
	}
	code := fmt.Sprintf("code-%d", time.Now().UnixNano())
	s.mu.Lock()
	s.codes[code] = &authCode{client: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), resource: q.Get("resource")}
	s.mu.Unlock()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound) //nolint:gosec // a fake authorization server: it redirects to the registered redirect_uri
}

func tokenError(w http.ResponseWriter, code string) {
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, map[string]string{"error": code})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, "invalid_request")
		return
	}
	form := r.PostForm
	s.mu.Lock()
	s.tokenReq = append(s.tokenReq, form)
	fail := s.failNext
	s.failNext = ""
	s.mu.Unlock()
	client := form.Get("client_id")
	if user, _, ok := r.BasicAuth(); ok && client == "" {
		client = user
	}
	if s.opt.ClientSecret != "" && form.Get("client_secret") != s.opt.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]string{"error": "invalid_client"})
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		ac := s.codes[form.Get("code")]
		if ac != nil && ac.used {
			s.mu.Unlock()
			tokenError(w, "invalid_grant")
			return
		}
		if ac != nil {
			ac.used = true
		}
		s.mu.Unlock()
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		switch {
		case ac == nil, ac.client != client, ac.redirect != form.Get("redirect_uri"),
			base64.RawURLEncoding.EncodeToString(sum[:]) != ac.challenge, form.Get("resource") != s.URL:
			tokenError(w, "invalid_grant")
			return
		}
		s.issue(w, client)
	case "refresh_token":
		if fail != "" {
			tokenError(w, fail)
			return
		}
		s.mu.Lock()
		owner, ok := s.refresh[form.Get("refresh_token")]
		if ok && !s.opt.NoRotate {
			delete(s.refresh, form.Get("refresh_token"))
		}
		s.mu.Unlock()
		if !ok || owner != client {
			tokenError(w, "invalid_grant")
			return
		}
		s.issueWith(w, client, form.Get("refresh_token"))
	default:
		tokenError(w, "unsupported_grant_type")
	}
}

// FailNextRefresh makes the next refresh request answer with the OAuth error code (for example invalid_grant, or
// temporarily_unavailable).
func (s *Server) FailNextRefresh(code string) {
	s.mu.Lock()
	s.failNext = code
	s.mu.Unlock()
}

func (s *Server) issue(w http.ResponseWriter, client string) { s.issueWith(w, client, "") }

func (s *Server) issueWith(w http.ResponseWriter, client, keepRefresh string) {
	s.mu.Lock()
	s.now++
	at := fmt.Sprintf("access-%d", s.now)
	rt := keepRefresh
	if rt == "" || !s.opt.NoRotate {
		rt = fmt.Sprintf("refresh-%d", s.now)
	}
	s.access[at] = rt
	s.refresh[rt] = client
	s.mu.Unlock()
	writeJSON(w, map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": s.opt.AccessTTL, "refresh_token": rt, "scope": s.opt.Scope})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
