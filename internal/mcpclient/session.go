package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"hal9001/internal/spec"
)

// Transports reported in View.Transport.
const (
	transportStreamable = "streamable_http"
	transportSSE        = "sse"
)

// callState is what one connection learns about authentication: the reason a request was refused.
type callState struct {
	mu sync.Mutex
	// authErr is set when no credentials could be produced (not connected, refresh refused).
	authErr error
	// unauthorized is set when the server answered 401 even with fresh credentials.
	unauthorized bool
}

func (c *callState) setAuthErr(err error) {
	c.mu.Lock()
	if c.authErr == nil {
		c.authErr = err
	}
	c.mu.Unlock()
}

func (c *callState) setUnauthorized() {
	c.mu.Lock()
	c.unauthorized = true
	c.mu.Unlock()
}

func (c *callState) get() (error, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authErr, c.unauthorized
}

// authTransport adds the server's credentials to every request and, for OAuth, refreshes the token and replays a request
// the server answered with 401.
type authTransport struct {
	m    *Manager
	name string
	cfg  spec.RemoteMCP
	st   *callState
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header, value, used, err := t.m.credentials(req.Context(), t.name, t.cfg, "")
	if err != nil {
		t.st.setAuthErr(err)
		return nil, err
	}
	resp, err := t.send(req, header, value)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	if !t.cfg.UsesOAuth() {
		t.st.setUnauthorized()
		return resp, nil
	}
	if req.Body != nil && req.GetBody == nil {
		t.st.setUnauthorized()
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	header, value, _, err = t.m.credentials(req.Context(), t.name, t.cfg, used)
	if err != nil {
		t.st.setAuthErr(err)
		return nil, err
	}
	replay := req
	if req.Body != nil {
		body, berr := req.GetBody()
		if berr != nil {
			return nil, berr
		}
		replay = req.Clone(req.Context())
		replay.Body = body
	}
	resp, err = t.send(replay, header, value)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		t.st.setUnauthorized()
		t.m.noteErr(t.name, errors.New("the server rejected the refreshed access token (HTTP 401)"), true)
	}
	return resp, err
}

func (t *authTransport) send(req *http.Request, header, value string) (*http.Response, error) {
	r := req.Clone(req.Context())
	if header != "" {
		r.Header.Set(header, value)
	}
	return t.m.opt.Transport.RoundTrip(r)
}

// open connects to the server: Streamable HTTP, then the older SSE transport when that cannot be established.
func (m *Manager) open(ctx context.Context, name string, cfg spec.RemoteMCP) (*mcp.ClientSession, string, *callState, error) {
	cs := &callState{}
	hc := &http.Client{Transport: &authTransport{m: m, name: name, cfg: cfg, st: cs}}
	impl := &mcp.Implementation{Name: "hal9001", Version: m.opt.Version}
	sess, err := mcp.NewClient(impl, nil).Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: cfg.URL, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err == nil {
		return sess, transportStreamable, cs, nil
	}
	if authErr, unauthorized := cs.get(); authErr != nil || unauthorized || ctx.Err() != nil {
		return nil, "", cs, err
	}
	if sess, serr := mcp.NewClient(impl, nil).Connect(ctx, &mcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: hc}, nil); serr == nil {
		return sess, transportSSE, cs, nil
	}
	return nil, "", cs, err
}

// failure turns an error of an operation into the error to return and records it on the server.
func (m *Manager) failure(ctx context.Context, name string, cs *callState, err error) error {
	authErr, unauthorized := cs.get()
	switch {
	case authErr != nil:
		// Not connected, or the refresh was refused for good (accessToken recorded that); anything else is a failure.
		if !errors.Is(authErr, ErrNeedsReconnect) && !errors.Is(authErr, ErrNotConnected) {
			m.noteErr(name, authErr, false)
		}
		return authErr
	case unauthorized:
		if cfg, cerr := m.config(name); cerr == nil && cfg.UsesOAuth() {
			return fmt.Errorf("%w: %q", ErrNeedsReconnect, name)
		}
		err = errors.New("the server rejected the credentials (HTTP 401)")
	case ctx.Err() != nil:
		return err
	}
	m.noteErr(name, err, false)
	return err
}

// Test connects, initializes and lists the tools, and reports what it found. The tool cache is left alone.
func (m *Manager) Test(ctx context.Context, name string) TestResult {
	cfg, err := m.config(name)
	if err != nil {
		return TestResult{Status: StatusError, Error: err.Error()}
	}
	start := m.now()
	res := TestResult{}
	finish := func(err error) TestResult {
		res.LatencyMS = m.now().Sub(start).Milliseconds()
		if v, verr := m.Server(name); verr == nil {
			res.Status = v.Status
		}
		if err != nil {
			res.Error = err.Error()
		} else {
			res.OK = true
		}
		return res
	}
	sess, transport, cs, err := m.open(ctx, name, cfg)
	if err != nil {
		return finish(m.failure(ctx, name, cs, err))
	}
	defer func() { _ = sess.Close() }()
	res.Transport = transport
	var serverName, serverVersion string
	if init := sess.InitializeResult(); init != nil {
		res.ProtocolVersion = init.ProtocolVersion
		if init.ServerInfo != nil {
			serverName, serverVersion = init.ServerInfo.Name, init.ServerInfo.Version
		}
	}
	res.ServerName, res.ServerVersion = serverName, serverVersion
	tools, err := listTools(ctx, sess)
	if err != nil {
		return finish(m.failure(ctx, name, cs, err))
	}
	res.ToolCount = len(tools)
	m.noteOK(name, serverName, serverVersion, transport)
	return finish(nil)
}

// RefreshTools lists the server's tools again and caches them. When the list differs from the cached one (a tool was
// added, removed, or its description or schema changed) it posts an informational Inbox note.
func (m *Manager) RefreshTools(ctx context.Context, name string) (Detail, ToolChanges, error) {
	cfg, err := m.config(name)
	if err != nil {
		return Detail{}, ToolChanges{}, err
	}
	sess, transport, cs, err := m.open(ctx, name, cfg)
	if err != nil {
		return Detail{}, ToolChanges{}, m.failure(ctx, name, cs, err)
	}
	defer func() { _ = sess.Close() }()
	tools, err := listTools(ctx, sess)
	if err != nil {
		return Detail{}, ToolChanges{}, m.failure(ctx, name, cs, err)
	}
	var serverName, serverVersion string
	if init := sess.InitializeResult(); init != nil && init.ServerInfo != nil {
		serverName, serverVersion = init.ServerInfo.Name, init.ServerInfo.Version
	}
	s := m.get(name)
	s.mu.Lock()
	m.load(name, s)
	first := s.st.ToolsRefreshedAt == nil
	changes := diffTools(s.st.Tools, tools)
	at := m.now().UTC()
	s.st.Tools, s.st.ToolsRefreshedAt = tools, &at
	s.st.LastError, s.st.LastErrorAt, s.st.NeedsReconnect = "", nil, false
	s.st.ServerName, s.st.ServerVersion, s.st.Transport = serverName, serverVersion, transport
	err = m.saveState(name, s)
	view := viewLocked(name, cfg, s)
	s.mu.Unlock()
	if err != nil {
		return Detail{}, ToolChanges{}, err
	}
	m.changed(name)
	if !first && changes.Any() {
		m.postNote(toolsNote(name, changes))
	}
	return Detail{View: view, Tools: append([]Tool{}, tools...)}, changes, nil
}

func toolsNote(name string, c ToolChanges) Note {
	var b []byte
	line := func(label string, names []string) {
		if len(names) == 0 {
			return
		}
		b = append(b, "- "+label+": "...)
		for i, n := range names {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = append(b, '`')
			b = append(b, n...)
			b = append(b, '`')
		}
		b = append(b, '\n')
	}
	line("added", c.Added)
	line("removed", c.Removed)
	line("description or schema changed", c.Changed)
	return Note{
		Server: name, Kind: "tools", Priority: "low",
		Title:     fmt.Sprintf("MCP server %s: tools changed", name),
		BodyMD:    "The tool list of this MCP server differs from the one hal9001 had cached:\n\n" + string(b) + "\nSensors that call these tools with `hal9001 mcp call` may need an update.",
		LinkLabel: "Open MCP servers", LinkPath: "/settings/context?mcp_server=" + name,
	}
}

// Tools returns the cached tool list and when it was fetched.
func (m *Manager) Tools(name string) ([]Tool, *time.Time, error) {
	if _, err := m.config(name); err != nil {
		return nil, nil, err
	}
	s := m.get(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	m.load(name, s)
	return append([]Tool{}, s.st.Tools...), s.st.ToolsRefreshedAt, nil
}

// Call calls one tool and returns its result. A tool that reports an error is a successful call with IsError set; an
// error return means the call did not reach the tool (not connected, unreachable, unknown tool, invalid arguments).
func (m *Manager) Call(ctx context.Context, name, tool string, args map[string]any) (CallResult, error) {
	cfg, err := m.config(name)
	if err != nil {
		return CallResult{}, err
	}
	sess, transport, cs, err := m.open(ctx, name, cfg)
	if err != nil {
		return CallResult{}, m.failure(ctx, name, cs, err)
	}
	defer func() { _ = sess.Close() }()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return CallResult{}, m.failure(ctx, name, cs, fmt.Errorf("call %s on %s: %w", tool, name, err))
	}
	if init := sess.InitializeResult(); init != nil && init.ServerInfo != nil {
		m.noteOK(name, init.ServerInfo.Name, init.ServerInfo.Version, transport)
	}
	return resultOf(res), nil
}

func listTools(ctx context.Context, sess *mcp.ClientSession) ([]Tool, error) {
	var out []Tool
	cursor := ""
	for range 100 { // a server cannot keep a client paging forever
		page, err := sess.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		for _, t := range page.Tools {
			out = append(out, toolOf(t))
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return out, nil
}

func toolOf(t *mcp.Tool) Tool {
	return Tool{
		Name: t.Name, Title: t.Title, Description: t.Description,
		InputSchema: schemaJSON(t.InputSchema, `{"type":"object"}`), OutputSchema: schemaJSON(t.OutputSchema, ""),
	}
}

func schemaJSON(v any, fallback string) json.RawMessage {
	if v == nil {
		if fallback == "" {
			return nil
		}
		return json.RawMessage(fallback)
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		if fallback == "" {
			return nil
		}
		return json.RawMessage(fallback)
	}
	return b
}

// diffTools compares the cached tool list with a fresh one.
func diffTools(old, now []Tool) ToolChanges {
	c := ToolChanges{Added: []string{}, Removed: []string{}, Changed: []string{}}
	prev := make(map[string]Tool, len(old))
	for _, t := range old {
		prev[t.Name] = t
	}
	seen := make(map[string]bool, len(now))
	for _, t := range now {
		seen[t.Name] = true
		p, ok := prev[t.Name]
		switch {
		case !ok:
			c.Added = append(c.Added, t.Name)
		case p.Title != t.Title || p.Description != t.Description || !sameJSON(p.InputSchema, t.InputSchema) || !sameJSON(p.OutputSchema, t.OutputSchema):
			c.Changed = append(c.Changed, t.Name)
		}
	}
	for _, t := range old {
		if !seen[t.Name] {
			c.Removed = append(c.Removed, t.Name)
		}
	}
	return c
}

// sameJSON compares two JSON documents by value (key order and spacing do not matter).
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	return string(xb) == string(yb)
}
