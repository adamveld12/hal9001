package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/auth"
	"hal9001/internal/httpsec"
	"hal9001/internal/mcpclient"
	"hal9001/internal/spec"
)

// The remote MCP servers hal9001 connects to as a client (docs/mcp-servers.md). Definitions live in config.yaml
// `mcp_servers`; credentials, tokens and the tool cache live in internal/mcpclient's own store.

const typeUpstream = httpsec.ProblemBase + "upstream-error"

// remoteMcpCallTimeout bounds one tool call.
const remoteMcpCallTimeout = 5 * time.Minute

// remoteMcp answers 503 itself and returns false when the daemon has no MCP client.
func (s *Server) remoteMcp(w http.ResponseWriter) (*mcpclient.Manager, bool) {
	if s.opts.MCPClient == nil {
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "the daemon has no MCP client", nil)
		return nil, false
	}
	return s.opts.MCPClient, true
}

// PublishMCPServer announces that a remote MCP server changed, as an `mcp_server.updated` event.
func (s *Server) PublishMCPServer(name string) {
	s.hub.publish("mcp_server.updated", struct {
		Name string `json:"name"`
	}{name}, nil, false)
}

// remoteMcpName reads and checks the {name} of the path; it answers 404 itself for a name that cannot exist.
func remoteMcpName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !spec.ValidMCPServerName(name) {
		notFound(w, "no MCP server named %q", name)
		return "", false
	}
	return name, true
}

// mcpError maps an error of the MCP client to a problem.
func (s *Server) mcpError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, mcpclient.ErrUnknownServer):
		notFound(w, "%s", err.Error())
	case errors.Is(err, mcpclient.ErrNotAllowed):
		problem(w, http.StatusForbidden, httpsec.TypeForbidden, "Forbidden", err.Error(), nil)
	case errors.Is(err, mcpclient.ErrNotConnected), errors.Is(err, mcpclient.ErrNeedsReconnect):
		conflictProblem(w, typeInvalidState, err.Error(), nil)
	case errors.Is(err, mcpclient.ErrNotOAuth), errors.Is(err, mcpclient.ErrNoRegistration), errors.Is(err, mcpclient.ErrNoAuthRequired):
		fieldProblem(w, "auth", err.Error())
	case r.Context().Err() != nil:
		problem(w, http.StatusServiceUnavailable, typeUnavailable, "Unavailable", "the request was cancelled", nil)
	default:
		problem(w, http.StatusBadGateway, typeUpstream, "Upstream error", err.Error(), nil)
	}
}

func (s *Server) listRemoteMcpServers(w http.ResponseWriter, _ *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []mcpclient.View `json:"items"`
	}{m.List()})
}

func (s *Server) getRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	d, err := m.Detail(name)
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// remoteMcpRequest is the body of create and update; name is only in the create request.
type remoteMcpRequest struct {
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Auth         string   `json:"auth"`
	ClientID     string   `json:"client_id"`
	ClientSecret *string  `json:"client_secret"`
	Header       string   `json:"header"`
	APIKey       *string  `json:"api_key"`
	Scopes       []string `json:"scopes"`
}

func (q remoteMcpRequest) definition() spec.RemoteMCP {
	return spec.RemoteMCP{URL: strings.TrimSpace(q.URL), Auth: q.Auth, ClientID: strings.TrimSpace(q.ClientID), Header: strings.TrimSpace(q.Header), Scopes: slices.Clone(q.Scopes)}
}

// check validates a definition and the secrets sent with it. It answers 422 itself.
func (q remoteMcpRequest) check(w http.ResponseWriter, name string) (spec.RemoteMCP, bool) {
	def := q.definition()
	ds := spec.ValidateRemoteMCPEntry(name, def)
	if q.ClientSecret != nil && *q.ClientSecret != "" && def.AuthMode() != spec.MCPAuthOAuthClient {
		ds = append(ds, spec.Diagnostic{Severity: spec.SeverityError, Code: spec.CodeFieldNotAllowed, Path: "client_secret", Message: "client_secret applies to auth oauth_client only"})
	}
	if q.APIKey != nil && *q.APIKey != "" && def.AuthMode() != spec.MCPAuthHeader {
		ds = append(ds, spec.Diagnostic{Severity: spec.SeverityError, Code: spec.CodeFieldNotAllowed, Path: "api_key", Message: "api_key applies to auth header only"})
	}
	if spec.HasErrors(ds) {
		validationProblem(w, "invalid MCP server", ds)
		return def, false
	}
	return def, true
}

// editRemoteMcpConfig applies edit to config.yaml's top-level mapping (comments and order kept) and reloads. It retries
// when the file changed meanwhile and answers a problem itself on failure; it reports whether the write happened.
func (s *Server) editRemoteMcpConfig(w http.ResponseWriter, r *http.Request, edit func(root *yaml.Node) (bool, error)) bool {
	for range 3 {
		raw, hash, exists, err := s.configFile()
		if err != nil {
			s.writeError(w, r, err)
			return false
		}
		before, bds := spec.CheckConfig(raw)
		if before == nil {
			validationProblem(w, "config.yaml does not parse; fix it first", bds)
			return false
		}
		updated, changed, err := spec.EditConfigYAML(raw, edit)
		if err != nil {
			badRequest(w, "%s", err.Error())
			return false
		}
		if !changed {
			return true
		}
		after, ads := spec.CheckConfig(updated)
		if after == nil {
			validationProblem(w, "the change makes config.yaml invalid", ads)
			return false
		}
		if introduced := newErrors(bds, ads); len(introduced) > 0 {
			validationProblem(w, "the change makes config.yaml invalid", introduced)
			return false
		}
		expected := hash
		if !exists {
			expected = ""
		}
		if _, err := spec.WriteFile(s.opts.Home, spec.KindConfig, "", updated, expected); err != nil {
			if errors.Is(err, spec.ErrConflict) {
				continue
			}
			s.writeError(w, r, err)
			return false
		}
		s.reload()
		s.publishFileChange(spec.KindConfig, "")
		return true
	}
	fileConflict(w, "config.yaml kept changing; try again", "", "")
	return false
}

func (s *Server) currentRemoteMcp(name string) (spec.RemoteMCP, bool) {
	ws := s.opts.Workspace()
	if ws == nil || ws.Config.Value == nil {
		return spec.RemoteMCP{}, false
	}
	def, ok := ws.Config.Value.MCPServers[name]
	return def, ok
}

func (s *Server) createRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	var req remoteMcpRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if !spec.ValidMCPServerName(req.Name) {
		fieldProblem(w, "name", fmt.Sprintf("MCP server name %q is invalid: use lowercase letters, digits, - and _ (max 32, starting with a letter or digit)", req.Name))
		return
	}
	def, ok := req.check(w, req.Name)
	if !ok {
		return
	}
	if _, exists := s.currentRemoteMcp(req.Name); exists {
		conflictProblem(w, typeAlreadyExists, fmt.Sprintf("an MCP server named %q already exists", req.Name), nil)
		return
	}
	created := true
	if !s.editRemoteMcpConfig(w, r, func(root *yaml.Node) (bool, error) {
		if spec.HasRemoteMCP(root, req.Name) {
			created = false
			return false, nil
		}
		return spec.SetRemoteMCP(root, req.Name, def)
	}) {
		return
	}
	if !created {
		conflictProblem(w, typeAlreadyExists, fmt.Sprintf("an MCP server named %q already exists", req.Name), nil)
		return
	}
	if err := m.SetSecrets(req.Name, mcpclient.Secrets{ClientSecret: req.ClientSecret, APIKey: req.APIKey}); err != nil {
		s.writeError(w, r, err)
		return
	}
	d, err := m.Detail(req.Name)
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) updateRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	old, exists := s.currentRemoteMcp(name)
	if !exists {
		notFound(w, "no MCP server named %q", name)
		return
	}
	var req remoteMcpRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if req.Name != "" {
		badRequest(w, "name cannot be changed: remove the server and add it again")
		return
	}
	def, ok := req.check(w, name)
	if !ok {
		return
	}
	if !s.editRemoteMcpConfig(w, r, func(root *yaml.Node) (bool, error) { return spec.SetRemoteMCP(root, name, def) }) {
		return
	}
	// Tokens were issued for one url, one auth mode and one client: any other is a new connection.
	if old.UsesOAuth() && (old.URL != def.URL || old.AuthMode() != def.AuthMode() || old.ClientID != def.ClientID) {
		if err := m.Disconnect(name); err != nil && !errors.Is(err, mcpclient.ErrUnknownServer) {
			s.writeError(w, r, err)
			return
		}
	}
	secrets := mcpclient.Secrets{ClientSecret: req.ClientSecret, APIKey: req.APIKey}
	empty := ""
	if def.AuthMode() != spec.MCPAuthOAuthClient && secrets.ClientSecret == nil {
		secrets.ClientSecret = &empty // a secret the new mode does not use is not kept
	}
	if def.AuthMode() != spec.MCPAuthHeader && secrets.APIKey == nil {
		secrets.APIKey = &empty
	}
	if err := m.SetSecrets(name, secrets); err != nil {
		s.writeError(w, r, err)
		return
	}
	d, err := m.Detail(name)
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	if _, exists := s.currentRemoteMcp(name); !exists {
		notFound(w, "no MCP server named %q", name)
		return
	}
	if users := s.sensorsUsingMcp(name); len(users) > 0 {
		conflictProblem(w, typeInvalidState, fmt.Sprintf("MCP server %q is used by sensor(s) %s: remove it from their mcp_servers first", name, strings.Join(users, ", ")),
			map[string]any{"sensors": users})
		return
	}
	if !s.editRemoteMcpConfig(w, r, func(root *yaml.Node) (bool, error) { return spec.RemoveRemoteMCP(root, name), nil }) {
		return
	}
	if err := m.Forget(name); err != nil {
		s.log().Warn("api: forget MCP server", "server", name, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// sensorsUsingMcp lists the sensors whose mcp_servers name the server.
func (s *Server) sensorsUsingMcp(name string) []string {
	var out []string
	if ws := s.opts.Workspace(); ws != nil {
		for _, f := range ws.Sensors {
			if f.Value != nil && slices.Contains(f.Value.MCPServers, name) {
				out = append(out, f.Name)
			}
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) connectRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := m.Connect(ctx, name)
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) disconnectRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	if err := m.Disconnect(name); err != nil {
		s.mcpError(w, r, err)
		return
	}
	d, err := m.Detail(name)
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) testRemoteMcpServer(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	if _, err := m.Server(name); err != nil {
		s.mcpError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, m.Test(ctx, name))
}

func (s *Server) refreshRemoteMcpServerTools(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	d, changes, err := m.RefreshTools(ctx, name)
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Server  mcpclient.Detail      `json:"server"`
		Changes mcpclient.ToolChanges `json:"changes"`
	}{d, changes})
}

type remoteMcpCallRequest struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// callRemoteMcpServerTool serves two kinds of caller: a run token (the sensor script's `hal9001 mcp call`), which
// grants the servers its sensor declares and nothing else, and every other caller, which needs the admin service.
func (s *Server) callRemoteMcpServerTool(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	if token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found && strings.HasPrefix(strings.TrimSpace(token), mcpclient.RunTokenPrefix) {
		grant, live := m.LookupRunToken(strings.TrimSpace(token))
		if !live {
			problem(w, http.StatusUnauthorized, httpsec.TypeUnauthenticated, "Unauthorized",
				"a valid, unrevoked run token is required", map[string]any{"service": "admin", "methods": []string{}})
			return
		}
		s.doRemoteMcpCall(w, r, m, grant)
		return
	}
	s.opts.Auth.Require(auth.ServiceAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.doRemoteMcpCall(w, r, m, nil)
	})).ServeHTTP(w, r)
}

func (s *Server) doRemoteMcpCall(w http.ResponseWriter, r *http.Request, m *mcpclient.Manager, grant *mcpclient.RunGrant) {
	name, ok := remoteMcpName(w, r)
	if !ok {
		return
	}
	var req remoteMcpCallRequest
	if !decodeJSON(w, r, &req, false) {
		return
	}
	if strings.TrimSpace(req.Tool) == "" {
		badRequest(w, "tool is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteMcpCallTimeout)
	defer cancel()
	var (
		res mcpclient.CallResult
		err error
	)
	if grant != nil {
		res, err = m.CallWith(ctx, grant, name, req.Tool, req.Arguments)
	} else {
		res, err = m.Call(ctx, name, req.Tool, req.Arguments)
	}
	if err != nil {
		s.mcpError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// oauthCallback is where the authorization server sends the browser back. The state, not a session, is the credential.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	m, ok := s.remoteMcp(w)
	if !ok {
		return
	}
	q := r.URL.Query()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	name, err := m.Callback(r.Context(), mcpclient.CallbackQuery{
		State: q.Get("state"), Code: q.Get("code"), Error: q.Get("error"), ErrorDescription: q.Get("error_description"), Iss: q.Get("iss"),
	})
	if name == "" {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>hal9001</title><p>%s</p><p><a href=\"/settings/context\">Back to hal9001</a></p>",
				html.EscapeString("This authorization link is no longer valid: it expired or was already used. Start Connect again from Settings → Context → MCP servers."))
			return
		}
		badRequest(w, "%s", err.Error())
		return
	}
	if err != nil {
		s.log().Warn("api: MCP authorization failed", "server", name, "err", err)
	}
	http.Redirect(w, r, "/settings/context?mcp_server="+url.QueryEscape(name), http.StatusFound)
}
