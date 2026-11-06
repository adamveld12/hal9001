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
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// EnvMCPToken is the fallback of `hal9001 mcp --token`.
const EnvMCPToken = "HAL9001_MCP_TOKEN" //nolint:gosec // an environment variable name, not a credential

// mcpLeaf is the stdio MCP server hal injects into agent sessions. It proxies tools/list and tools/call to the
// daemon's /api/mcp/ endpoints with the run's capability token; it holds no tool logic.
func mcpLeaf() *leaf {
	var token string
	return &leaf{
		maxArgs: 0,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&token, "token", "", "per-run capability token (env "+EnvMCPToken+")")
		},
		run: func(ctx context.Context, c *call) error {
			if token == "" {
				token = c.env.Getenv(EnvMCPToken)
			}
			if token == "" {
				return usagef("mcp: --token is required")
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
			rc := io.NopCloser(c.env.Stdin)
			wc := nopWriteCloser{c.env.Stdout}
			return srv.Run(ctx, &mcp.IOTransport{Reader: rc, Writer: wc})
		},
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

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
