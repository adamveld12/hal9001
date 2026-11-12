package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// EnvMCPToken is the fallback of `hal9001 mcp --token`.
const EnvMCPToken = "HAL9001_MCP_TOKEN" //nolint:gosec // an environment variable name, not a credential

// mcpLeaf is hal's stdio MCP server, in two modes. With a run token (`--token`, which hal passes to the agents it
// starts) it serves that run's tools by proxying tools/list and tools/call to the daemon's /api/mcp/ endpoints. Without
// one it is for a client the user runs themselves (Claude Code, Codex, omp, pi): it serves the daemon's operator tools
// by bridging to its Streamable HTTP endpoint (POST /mcp), so both transports expose the same tools and the same
// approvals. It holds no tool logic either way.
func mcpLeaf() *leaf {
	var token string
	return &leaf{
		maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&token, "token", "", "per-run capability token (env "+EnvMCPToken+"); without it, serve the operator tools of the daemon at --url")
		},
		run: func(ctx context.Context, c *call) error {
			if token == "" {
				token = c.env.Getenv(EnvMCPToken)
			}
			rc := io.NopCloser(c.env.Stdin)
			wc := nopWriteCloser{c.env.Stdout}
			if token == "" {
				return bridgeOperator(ctx, c, &mcp.IOTransport{Reader: rc, Writer: wc})
			}
			if !c.flagSet("url") && c.env.Getenv(EnvURL) == "" {
				return usagef("mcp: --url is required (the daemon this run belongs to)")
			}
			hc := c.env.HTTP
			if hc == nil {
				hc = http.DefaultClient
			}
			p := &mcpProxy{base: strings.TrimRight(c.url, "/"), token: token, hc: hc}
			srv, err := p.server(ctx)
			if err != nil {
				return err
			}
			return srv.Run(ctx, &mcp.IOTransport{Reader: rc, Writer: wc})
		},
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// bridgeOperator serves the operator tools of the daemon at c.url over t (stdio): it connects to <url>/mcp with the
// session cookie, if any, lists the tools once, and forwards each tools/call.
func bridgeOperator(ctx context.Context, c *call, t mcp.Transport) error {
	base := strings.TrimRight(c.url, "/")
	var rt http.RoundTripper
	if c.env.HTTP != nil {
		rt = c.env.HTTP.Transport
	}
	hc := &http.Client{Transport: cookieTransport{rt: rt, cookie: cookieHeader(c.cookie)}} // no timeout: a call may wait for an approval
	client := mcp.NewClient(&mcp.Implementation{Name: "hal9001-stdio", Version: "1"}, nil)
	remote, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return fmt.Errorf("mcp: cannot reach hal9001 at %s/mcp (is the daemon running? when its auth stack asks for a login, pass --session-cookie or set %s): %w", base, EnvSessionCookie, err)
	}
	defer func() { _ = remote.Close() }()

	srv := mcp.NewServer(&mcp.Implementation{Name: "hal9001", Version: "1"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}}})
	var cursor string
	for {
		page, err := remote.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return fmt.Errorf("mcp: list tools: %w", err)
		}
		for _, tool := range page.Tools {
			name := tool.Name
			srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var args json.RawMessage
				if req.Params != nil {
					args = req.Params.Arguments
				}
				// The daemon is stateless, so the agent's own clientInfo (from its initialize to this bridge) travels with
				// every call; approvals and notes then name the agent, not the bridge.
				params := &mcp.CallToolParams{Name: name, Arguments: args}
				if info := req.ClientInfo(); info != nil {
					params.Meta = mcp.Meta{mcp.MetaKeyClientInfo: info}
				}
				res, err := remote.CallTool(ctx, params)
				if err != nil {
					return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "hal9001: " + err.Error()}}}, nil
				}
				return res, nil
			})
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	return srv.Run(ctx, t)
}

// cookieTransport adds the session cookie to every request.
type cookieTransport struct {
	rt     http.RoundTripper
	cookie string
}

func (t cookieTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt := t.rt
	if rt == nil {
		rt = http.DefaultTransport
	}
	if t.cookie != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Cookie", t.cookie)
	}
	return rt.RoundTrip(r)
}

type mcpProxy struct {
	base, token string
	hc          *http.Client
}

type mcpToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// server builds an MCP server whose tools are the ones the daemon lists for the token.
func (p *mcpProxy) server(ctx context.Context) (*mcp.Server, error) {
	var list struct {
		Tools []mcpToolInfo `json:"tools"`
	}
	if err := p.do(ctx, http.MethodGet, "/api/mcp/tools", nil, &list); err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "hal", Version: "1"}, &mcp.ServerOptions{
		// Tell the daemon the agent's client is connected: it holds the first prompt until then.
		InitializedHandler: func(ctx context.Context, _ *mcp.InitializedRequest) {
			_ = p.do(ctx, http.MethodPost, "/api/mcp/ready", map[string]any{}, nil)
		},
	})
	for _, t := range list.Tools {
		srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, p.handler(t.Name))
	}
	return srv, nil
}

func (p *mcpProxy) handler(name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args json.RawMessage
		if req.Params != nil {
			args = req.Params.Arguments
		}
		body := map[string]any{"name": name}
		if len(args) > 0 {
			body["arguments"] = args
		}
		var res struct {
			Text    string `json:"text"`
			IsError bool   `json:"is_error"`
		}
		if err := p.do(ctx, http.MethodPost, "/api/mcp/call", body, &res); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		return &mcp.CallToolResult{IsError: res.IsError, Content: []mcp.Content{&mcp.TextContent{Text: res.Text}}}, nil
	}
}

func (p *mcpProxy) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var pr struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
		}
		_ = json.Unmarshal(data, &pr)
		msg := pr.Detail
		if msg == "" {
			msg = pr.Title
		}
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return errors.New("hal daemon: " + resp.Status + ": " + msg)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// EnvRunToken is the run token of a sensor poll or draft test (docs/mcp-servers.md). With it, `hal9001 mcp call` is
// authorized for exactly the servers the sensor lists in mcp_servers; the daemon holds the servers' credentials.
const EnvRunToken = "HAL9001_RUN_TOKEN" //nolint:gosec // an environment variable name, not a credential

// mcpCommand is `hal9001 mcp`: the stdio MCP server when named alone (or with flags), and `mcp call` for scripts.
func mcpCommand() *command {
	return &command{
		name:    "mcp",
		summary: "hal's stdio MCP server (alone: for one agent run with --token, or the operator tools for another MCP client); `mcp call` calls a tool of a connected remote MCP server",
		subs:    []*command{cmd("call", "call a tool of a remote MCP server (a sensor script's declared mcp_servers) and print its result as JSON", mcpCallLeaf())},
		def:     mcpLeaf(),
	}
}

// mcpCallLeaf is `hal9001 mcp call <server> <tool> --args '<json>'`. It posts to the daemon, which holds the server's
// credentials; the token in HAL9001_RUN_TOKEN (set for a sensor that lists mcp_servers) limits it to those servers. The
// output is the tool's structured content when it has any, else {"text": "..."}; a tool that reports an error exits
// non-zero with the message on stderr.
func mcpCallLeaf() *leaf {
	var args string
	return &leaf{
		usage:   "<server> <tool> [--args '<json object>']",
		minArgs: 2, maxArgs: 2,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&args, "args", "", "the tool's arguments as a JSON object (default {}; - reads it from stdin)")
		},
		run: func(ctx context.Context, c *call) error {
			arguments := map[string]any{}
			raw := strings.TrimSpace(args)
			if raw == "-" {
				b, err := io.ReadAll(io.LimitReader(c.env.Stdin, 1<<20))
				if err != nil {
					return fmt.Errorf("read --args from stdin: %w", err)
				}
				raw = strings.TrimSpace(string(b))
			}
			if raw != "" {
				if err := json.Unmarshal([]byte(raw), &arguments); err != nil || arguments == nil {
					return usagef("mcp call: --args must be a JSON object")
				}
			}
			body, err := json.Marshal(map[string]any{"tool": c.args[1], "arguments": arguments})
			if err != nil {
				return err
			}
			cl := c.client()
			if tok := c.env.Getenv(EnvRunToken); tok != "" {
				cl.headers = http.Header{"Authorization": {"Bearer " + tok}}
			}
			resp, err := cl.do(ctx, http.MethodPost, "/api/mcp-servers/"+url.PathEscape(c.args[0])+"/call", nil, body)
			if err != nil {
				return err
			}
			var res struct {
				IsError    bool            `json:"is_error"`
				Text       string          `json:"text"`
				Structured json.RawMessage `json:"structured_content"`
			}
			if err := json.Unmarshal(resp.Body, &res); err != nil {
				return fmt.Errorf("mcp call: unexpected response: %w", err)
			}
			if res.IsError {
				_, _ = fmt.Fprintf(c.stderr(), "hal9001: %s on %s reported an error: %s\n", c.args[1], c.args[0], res.Text)
				return &silentError{}
			}
			out := res.Structured
			if len(out) == 0 || string(out) == "null" {
				if out, err = json.Marshal(map[string]string{"text": res.Text}); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(c.stdout(), "%s\n", out)
			return err
		},
	}
}
