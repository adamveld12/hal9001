package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hal9001/internal/auth"
	"hal9001/internal/httpsec"
	"hal9001/internal/tools"
)

// McpServerName is the name hal9001 suggests for its MCP server in other clients' configuration.
const McpServerName = "hal9001"

// operatorServer builds the MCP server of one request to POST /mcp: hal's operator tools (internal/tools), bound to the
// caller's identity. The endpoint is stateless, so a server is built per request; it holds no state.
func (s *Server) operatorServer(r *http.Request) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: McpServerName, Version: s.opts.Version}, &mcp.ServerOptions{
		// The tool set is fixed per request and the endpoint opens no stream, so there is nothing to notify about.
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
	})
	principalName := principal(r)
	api := s.dispatcher(r)
	authorize := s.authorizer(r)
	key := mcpClientKey(r, principalName)
	// The server is stateless, so a later tools/call does not carry the clientInfo of the initialize that came
	// before it. Remember it, per caller, for the client name of approvals and notes.
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if p, ok := req.GetParams().(*mcp.InitializeParams); ok && method == "initialize" && p != nil {
				if name := clientLabel(p.ClientInfo); name != "" {
					s.mcpClients.set(key, name)
				}
			}
			return next(ctx, method, req)
		}
	})
	for _, info := range s.opts.Operator.Tools() {
		name := info.Name
		srv.AddTool(&mcp.Tool{Name: name, Description: info.Description, InputSchema: info.InputSchema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var args []byte
				if req.Params != nil {
					args = req.Params.Arguments
				}
				client := clientLabel(req.ClientInfo())
				if client == "" {
					client = s.mcpClients.get(key)
				}
				if client == "" {
					client = userAgentName(r)
				}
				env := tools.OperatorEnv{API: api, Authorize: authorize, Caller: tools.OperatorCaller{Principal: principalName, Client: client}}
				res, err := s.opts.Operator.Call(ctx, env, name, args)
				if err != nil {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Text}}}, nil
			})
	}
	return srv
}

// authorizer returns what lets an operator tool ask, before it does anything, whether this MCP request is admitted for
// an auth service: it runs the service's own auth stack (the one the REST endpoint of that service uses) over the
// request and reports the refusal it would have answered.
func (s *Server) authorizer(orig *http.Request) func(context.Context, string) error {
	return func(ctx context.Context, service string) error {
		reached := false
		inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		s.opts.Auth.Require(auth.Service(service))(inner).ServeHTTP(rec, orig.Clone(ctx))
		if reached {
			return nil
		}
		var p struct{ Title, Detail string }
		_ = json.Unmarshal(rec.body.Bytes(), &p)
		msg := p.Detail
		if msg == "" {
			msg = p.Title
		}
		return fmt.Errorf("hal9001: %d %s: this tool needs the %s service, which this MCP request is not admitted for: %s", rec.status, http.StatusText(rec.status), service, msg)
	}
}

// maxClientName bounds a client name taken from the request.
const maxClientName = 64

// clientLabel is the name of an MCP client from its clientInfo ("claude-code 2.1.3"), cleaned of anything that is not
// printable text.
func clientLabel(info *mcp.Implementation) string {
	if info == nil {
		return ""
	}
	name := info.Title
	if name == "" {
		name = info.Name
	}
	if info.Version != "" && name != "" {
		name += " " + info.Version
	}
	return cleanName(name)
}

// userAgentName is the product of the request's User-Agent ("claude-code/2.1.3 (cli)" gives "claude-code/2.1.3"), the
// client name when the client never said who it is.
func userAgentName(r *http.Request) string {
	ua, _, _ := strings.Cut(strings.TrimSpace(r.Header.Get("User-Agent")), " ")
	return cleanName(ua)
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > maxClientName {
		s = strings.ToValidUTF8(s[:maxClientName], "")
	}
	return s
}

// mcpClientKey identifies a caller across the requests of one MCP connection that has no session: the principal, the
// peer address without its port and the User-Agent.
func mcpClientKey(r *http.Request, principal string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return principal + "|" + host + "|" + r.Header.Get("User-Agent")
}

// clientNames remembers the client name of recent initialize requests, by caller key. It is a label for people, not
// a credential: a client can claim any name.
type clientNames struct {
	mu sync.Mutex
	m  map[string]clientName
}

type clientName struct {
	name string
	at   time.Time
}

const (
	clientNameTTL = time.Hour
	clientNameCap = 64
)

func (c *clientNames) set(key, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]clientName{}
	}
	now := time.Now()
	for k, v := range c.m {
		if now.Sub(v.at) > clientNameTTL {
			delete(c.m, k)
		}
	}
	if _, ok := c.m[key]; !ok && len(c.m) >= clientNameCap {
		for k := range c.m { // evict an arbitrary entry
			delete(c.m, k)
			break
		}
	}
	c.m[key] = clientName{name: name, at: now}
}

func (c *clientNames) get(key string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	if !ok || time.Since(v.at) > clientNameTTL {
		return ""
	}
	return v.name
}

// dispatcher returns the API an operator tool runs against: this server's own routes, called in-process with the
// identity of the MCP request (its cookie, peer address and Host), so every endpoint applies the auth service it
// declares exactly as it would for the MCP client calling the REST API directly.
func (s *Server) dispatcher(orig *http.Request) tools.APIFunc { return dispatchTo(s.mux, orig) }

// readDispatcher is the API the read tools of an assist session run against: the GET operations of the `api` service,
// called in-process without the auth stack (the session belongs to a user who is signed in to the UI; the agent's own
// request carries only a run token). It refuses every other method, so no tool call through it changes anything.
func (s *Server) readDispatcher(orig *http.Request) tools.APIFunc {
	call := dispatchTo(s.readMux, orig)
	return func(ctx context.Context, method, path string, q url.Values, body []byte) (int, []byte, error) {
		if method != http.MethodGet {
			return 0, nil, fmt.Errorf("read tools only GET: %s %s refused", method, path)
		}
		return call(ctx, method, path, q, body)
	}
}

func dispatchTo(mux *http.ServeMux, orig *http.Request) tools.APIFunc {
	return func(ctx context.Context, method, path string, q url.Values, body []byte) (int, []byte, error) {
		u := url.URL{Path: path, RawQuery: q.Encode()}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Host, req.RemoteAddr = orig.Host, orig.RemoteAddr
		req.Header = orig.Header.Clone()
		for _, h := range []string{"Content-Length", "Content-Type", "Accept", "Accept-Encoding", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id"} {
			req.Header.Del(h)
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := &recorder{header: http.Header{}, status: http.StatusOK}
		mux.ServeHTTP(rec, req)
		return rec.status, rec.body.Bytes(), nil
	}
}

// recorder is the minimal http.ResponseWriter the dispatcher needs.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(status int)      { r.status = status }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }

// serveMcp is POST /mcp: the Streamable HTTP transport of hal's operator MCP server. It accepts only loopback
// requests, on top of the baseline every request passes (Host and Origin allow-list, JSON content type) and the `api`
// auth service the contract declares.
func (s *Server) serveMcp(w http.ResponseWriter, r *http.Request) {
	if s.opts.Operator == nil {
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "the daemon has no MCP operator tools", nil)
		return
	}
	if !loopbackOnly(w, r) {
		return
	}
	s.mcpHandler.ServeHTTP(w, r)
}

// mcpMethodNotAllowed answers every method of /mcp but POST. The endpoint opens no server-to-client stream and has no
// sessions to end, so GET and DELETE are 405 as the transport specification allows.
func (s *Server) mcpMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	problem(w, http.StatusMethodNotAllowed, httpsec.ProblemBase+"method-not-allowed", "Method not allowed", r.Method+" /mcp is not supported; POST JSON-RPC messages to it", nil)
}

// loopbackOnly is the DNS-rebinding and exposure check of the MCP specification. It refuses, with 403, a request
// whose peer, Host or Origin is not loopback and one that came through a proxy (tailscale serve, or anything else
// that adds forwarding headers), even for a host the baseline allows: the MCP endpoint is for clients on this Mac.
func loopbackOnly(w http.ResponseWriter, r *http.Request) bool {
	deny := func(typ, title, detail string) bool {
		httpsec.WriteProblem(w, httpsec.Problem{Type: typ, Title: title, Status: http.StatusForbidden, Detail: detail})
		return false
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !net.ParseIP(ip).IsLoopback() {
		return deny(httpsec.TypeForbidden, "Not a loopback peer", "the MCP endpoint only accepts connections from this Mac")
	}
	if !isLoopbackName(r.Host) {
		return deny(httpsec.TypeHostNotAllowed, "Host not allowed", "the MCP endpoint is served on localhost only; Host "+strconv.Quote(r.Host)+" is not loopback")
	}
	for _, o := range r.Header.Values("Origin") {
		u, err := url.Parse(o)
		if err != nil || !isLoopbackName(u.Host) {
			return deny(httpsec.TypeOriginNotAllowed, "Origin not allowed", "Origin "+strconv.Quote(o)+" is not loopback")
		}
	}
	for k := range r.Header {
		if k == "Forwarded" || strings.HasPrefix(k, "X-Forwarded-") || strings.HasPrefix(k, "Tailscale-") {
			return deny(httpsec.TypeForbidden, "Proxied request", "the MCP endpoint does not accept proxied requests ("+k+")")
		}
	}
	return true
}

// isLoopbackName reports whether a Host or Origin host[:port] names this machine: localhost, 127.0.0.1 or [::1].
func isLoopbackName(hostport string) bool {
	h := strings.ToLower(hostport)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// ---- GET /api/mcp/info ----

type mcpInfoView struct {
	ServerName string `json:"server_name"`
	// HTTPURL is the Streamable HTTP endpoint on this daemon's loopback address.
	HTTPURL string `json:"http_url"`
	// BaseURL is the daemon's address, the --url of the stdio command.
	BaseURL string `json:"base_url"`
	Stdio   struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"stdio"`
	// CLILink is where the app's "Install command-line tool" puts a symlink to the binary.
	CLILink string      `json:"cli_link"`
	Auth    mcpAuthView `json:"auth"`
	// ApprovalTimeoutSeconds is how long a tool call that needs approval waits for the user.
	ApprovalTimeoutSeconds int           `json:"approval_timeout_seconds"`
	Tools                  []mcpToolView `json:"tools"`
}

type mcpAuthView struct {
	// LocalhostTrust per service: whether the service's stack admits a request from this Mac without credentials.
	// api covers the read tools; admin the tools with side effects (flow_run, sensor_poll).
	LocalhostTrust struct {
		API   bool `json:"api"`
		Admin bool `json:"admin"`
	} `json:"localhost_trust"`
	// CookieName is the session cookie to send (header Cookie) when a stack asks for a login.
	CookieName string `json:"cookie_name"`
}

type mcpToolView struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	NeedsApproval bool   `json:"needs_approval"`
	// NeedsAdmin is true for a tool that changes state: the request must be admitted for the admin service too.
	NeedsAdmin bool `json:"needs_admin"`
}

func (s *Server) getMcpInfo(w http.ResponseWriter, _ *http.Request) {
	port := s.opts.Port
	if port == 0 {
		port = httpsec.DefaultPort
	}
	base := "http://localhost:" + strconv.Itoa(port)
	v := mcpInfoView{
		ServerName: McpServerName, HTTPURL: base + "/mcp", BaseURL: base, CLILink: "~/.local/bin/hal9001",
		Tools: []mcpToolView{},
	}
	exe := s.opts.Executable
	if exe == "" {
		exe, _ = os.Executable()
	}
	v.Stdio.Command, v.Stdio.Args = exe, []string{"mcp", "--url", base}
	cfg := s.opts.Auth.Config()
	v.Auth.LocalhostTrust.API = stackTrustsLocalhost(cfg, auth.ServiceAPI)
	v.Auth.LocalhostTrust.Admin = stackTrustsLocalhost(cfg, auth.ServiceAdmin)
	v.Auth.CookieName = auth.SessionCookie
	if s.opts.Operator != nil {
		v.ApprovalTimeoutSeconds = int(s.opts.Operator.Timeout().Seconds())
		for _, t := range s.opts.Operator.Tools() {
			v.Tools = append(v.Tools, mcpToolView{Name: t.Name, Description: t.Description, NeedsApproval: s.opts.Operator.NeedsApproval(t.Name), NeedsAdmin: s.opts.Operator.Service(t.Name) != ""})
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// stackTrustsLocalhost reports whether svc's stack lists the localhost-trust module.
func stackTrustsLocalhost(c auth.Config, svc auth.Service) bool {
	for _, e := range c.Stacks[c.Services[svc]] {
		if e.Module == "localhost-trust" {
			return true
		}
	}
	return false
}
