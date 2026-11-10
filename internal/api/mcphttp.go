package api

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

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
	caller := principal(r)
	api := s.dispatcher(r)
	for _, info := range s.opts.Operator.Tools() {
		name := info.Name
		srv.AddTool(&mcp.Tool{Name: name, Description: info.Description, InputSchema: info.InputSchema},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var args []byte
				if req.Params != nil {
					args = req.Params.Arguments
				}
				res, err := s.opts.Operator.Call(ctx, api, caller, name, args)
				if err != nil {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res.Text}}}, nil
			})
	}
	return srv
}

// dispatcher returns the API an operator tool runs against: this server's own routes, called in-process with the
// identity of the MCP request (its cookie, peer address and Host), so every endpoint applies the auth service it
// declares exactly as it would for the MCP client calling the REST API directly.
func (s *Server) dispatcher(orig *http.Request) tools.APIFunc {
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
		s.mux.ServeHTTP(rec, req)
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
			v.Tools = append(v.Tools, mcpToolView{Name: t.Name, Description: t.Description, NeedsApproval: s.opts.Operator.NeedsApproval(t.Name)})
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
