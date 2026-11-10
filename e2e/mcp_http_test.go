package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"hal9001/internal/cli"
)

func toolNames(t *testing.T, s *mcpsdk.ClientSession) map[string]bool {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}
	return names
}

func resultText(t *testing.T, res *mcpsdk.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// callAsync runs a tool call in the background; wait returns its result.
func callAsync(s *mcpsdk.ClientSession, name string, args map[string]any) (wait func() (*mcpsdk.CallToolResult, error)) {
	var (
		res  *mcpsdk.CallToolResult
		err  error
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		res, err = s.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	}()
	return func() (*mcpsdk.CallToolResult, error) { <-done; return res, err }
}

// hal's remote MCP server and its stdio twin expose the same operator tools. Reads run directly; a tool with side
// effects waits for an approval that belongs to no run, and runs only when the user approves it in the Inbox.
func TestMCPOperatorToolsOverHTTPAndStdio(t *testing.T) {
	d := newDaemon(t)
	d.Write("items.jsonl", abcItems)
	manualSensor(d, "ticker", "fire", "items.jsonl")
	d.Flow("one", inboxFlow("one", true, "ticker"))
	d.Start()

	ctx := context.Background()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e", Version: "1"}, nil)
	httpSess, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: d.URL() + "/mcp", DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	t.Cleanup(func() { _ = httpSess.Close() })

	names := toolNames(t, httpSess)
	for _, want := range []string{"status", "inbox_list", "inbox_get", "approvals_list", "flows_list", "flow_run", "sensor_poll", "flow_runs_list"} {
		if !names[want] {
			t.Errorf("tools/list lacks %q: %v", want, names)
		}
	}
	for _, banned := range []string{"approve", "approval_decide", "approve_approval", "deny_approval", "gh_api", "run_context"} {
		if names[banned] {
			t.Errorf("tools/list offers %q, which an operator client must not have", banned)
		}
	}

	// A read runs at once.
	res, err := httpSess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "flows_list", Arguments: map[string]any{}})
	if err != nil || res.IsError || !strings.Contains(resultText(t, res), `"id":"one"`) {
		t.Fatalf("flows_list: %v %+v", err, res)
	}

	// flow_run waits for an approval that has no flow or agent run behind it.
	wait := callAsync(httpSess, "flow_run", map[string]any{"id": "one"})
	ap := d.WaitApproval("request")
	if ap.NodeRunID != "" || ap.AgentRunID != "" || ap.FlowID != "" {
		t.Errorf("an operator approval has run ids: %+v", ap)
	}
	var detail struct {
		Request struct {
			Title  string `json:"title"`
			BodyMD string `json:"body_md"`
			Fields []struct{ Key, Value string }
		} `json:"request"`
	}
	d.Get("/api/approvals/" + ap.ID).Want(200).JSON(&detail)
	if !strings.Contains(detail.Request.Title, "flow_run") || !strings.Contains(detail.Request.BodyMD, `"one"`) || len(detail.Request.Fields) != 3 {
		t.Errorf("approval content: %+v", detail.Request)
	}
	if n := len(d.FlowRuns("one")); n != 0 {
		t.Fatalf("the flow ran before it was approved: %d runs", n)
	}
	d.Decide(ap.ID, true, nil)
	res, err = wait()
	if err != nil || res.IsError {
		t.Fatalf("flow_run after approval: %v %+v", err, res)
	}
	var run runFlowResult
	if err := json.Unmarshal([]byte(resultText(t, res)), &run); err != nil || len(run.FlowRunIDs) != 3 {
		t.Fatalf("flow_run result: %v %s", err, resultText(t, res))
	}
	d.waitAll(run.FlowRunIDs)

	// A denial reaches the client as a tool error and nothing runs.
	events := len(d.Events("ticker"))
	wait = callAsync(httpSess, "sensor_poll", map[string]any{"id": "ticker", "rerun_seen": true})
	ap = d.WaitApproval("request")
	d.Decide(ap.ID, false, map[string]any{"comment": "not now"})
	res, err = wait()
	if err != nil || !res.IsError || !strings.Contains(resultText(t, res), "denied by the user: not now") {
		t.Fatalf("denied sensor_poll: %v %+v", err, res)
	}
	if got := len(d.Events("ticker")); got != events {
		t.Errorf("events changed after a denied poll: %d -> %d", events, got)
	}
	if n := len(d.FlowRuns("one")); n != 3 {
		t.Errorf("flow runs after a denied poll: %d, want 3", n)
	}

	// Invalid arguments are refused without asking anybody.
	res, err = httpSess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "flow_run", Arguments: map[string]any{"id": "../killswitch"}})
	if err != nil || !res.IsError {
		t.Fatalf("flow_run with a bad id: %v %+v", err, res)
	}
	if got := d.Approvals("pending"); len(got) != 0 {
		t.Errorf("a refused call left approvals: %+v", got)
	}

	// The stdio server is `hal9001 mcp` without a run token: same tools, same approvals.
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	runCtx, cancel := context.WithCancel(ctx)
	var cliDone sync.WaitGroup
	cliDone.Add(1)
	var cliErr strings.Builder
	go func() {
		defer cliDone.Done()
		code := cli.Run(runCtx, []string{"mcp"}, cli.Env{
			Stdin: stdinR, Stdout: stdoutW, Stderr: &cliErr, Version: "e2e",
			Getenv: func(k string) string {
				switch k {
				case cli.EnvURL:
					return d.URL()
				case cli.EnvHome:
					return d.Home
				}
				return os.Getenv(k)
			},
		})
		if code != 0 {
			t.Errorf("hal9001 mcp exited %d: %s", code, cliErr.String())
		}
		_ = stdoutW.Close()
	}()
	stdioSess, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "e2e-stdio", Version: "1"}, nil).
		Connect(ctx, &mcpsdk.IOTransport{Reader: stdoutR, Writer: stdinW}, nil)
	if err != nil {
		t.Fatalf("connect over stdio: %v", err)
	}
	stdioNames := toolNames(t, stdioSess)
	if len(stdioNames) != len(names) {
		t.Errorf("stdio tools %v differ from HTTP tools %v", stdioNames, names)
	}
	for n := range names {
		if !stdioNames[n] {
			t.Errorf("stdio server lacks %q", n)
		}
	}
	wait = callAsync(stdioSess, "sensor_poll", map[string]any{"id": "ticker"})
	ap = d.WaitApproval("request")
	d.Decide(ap.ID, true, nil)
	if res, err = wait(); err != nil || res.IsError {
		t.Fatalf("sensor_poll over stdio after approval: %v %+v", err, res)
	}
	_ = stdioSess.Close()
	_ = stdinW.Close()
	finished := make(chan struct{})
	go func() { cliDone.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Error("hal9001 mcp did not exit when its stdin closed")
	}
	cancel()
}

// The endpoint answers the loopback baseline of the MCP specification: no GET stream, no foreign Host or Origin.
func TestMCPEndpointIsLoopbackOnly(t *testing.T) {
	d := newDaemon(t)
	d.Start()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	post := func(headers map[string]string) int {
		req, err := http.NewRequest(http.MethodPost, d.URL()+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for k, v := range headers {
			if k == "Host" {
				req.Host = v
				continue
			}
			req.Header.Set(k, v)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := post(nil); got != 200 {
		t.Errorf("a plain loopback request: %d", got)
	}
	if got := post(map[string]string{"Origin": "https://evil.example"}); got != 403 {
		t.Errorf("a foreign Origin: %d, want 403", got)
	}
	if got := post(map[string]string{"Host": "evil.example"}); got != 403 {
		t.Errorf("a foreign Host: %d, want 403", got)
	}
	if got := post(map[string]string{"X-Forwarded-For": "100.64.0.1"}); got != 401 && got != 403 {
		t.Errorf("a proxied request: %d, want it refused", got)
	}
	req, _ := http.NewRequest(http.MethodGet, d.URL()+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Errorf("GET /mcp: %d Allow=%q, want 405 POST", resp.StatusCode, resp.Header.Get("Allow"))
	}
}
