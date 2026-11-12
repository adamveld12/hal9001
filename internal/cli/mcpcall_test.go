package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"hal9001/internal/cli"
)

func TestMCPCallPrintsStructuredContentOrTheTextAndSendsTheRunToken(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/mcp-servers/notion/call",
		ok(map[string]any{"is_error": false, "text": "{\"results\":[1]}", "structured_content": map[string]any{"results": []any{1}}}),
		ok(map[string]any{"is_error": false, "text": "line one\nline two"}))

	res := f.run(runOpts{env: map[string]string{cli.EnvRunToken: "hal_run_abc"}}, "mcp", "call", "notion", "search", "--args", `{"query":"roadmap"}`)
	if res.code != 0 || !jsonEqual(t, res.stdout, map[string]any{"results": []any{1}}) {
		t.Fatalf("structured: exit %d out %q err %q", res.code, res.stdout, res.stderr)
	}
	req := f.requests()[0]
	if req.Header.Get("Authorization") != "Bearer hal_run_abc" {
		t.Errorf("Authorization = %q", req.Header.Get("Authorization"))
	}
	var body struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil || body.Tool != "search" || body.Arguments["query"] != "roadmap" {
		t.Errorf("body = %s (%v)", req.Body, err)
	}

	res = f.run(runOpts{}, "mcp", "call", "notion", "plain")
	if res.code != 0 || !jsonEqual(t, res.stdout, map[string]any{"text": "line one\nline two"}) {
		t.Errorf("text: exit %d out %q", res.code, res.stdout)
	}
	if got := f.requests()[1].Header.Get("Authorization"); got != "" {
		t.Errorf("without a run token the CLI sent Authorization %q", got)
	}
}

func TestMCPCallFailsWithTheToolErrorOnStderr(t *testing.T) {
	f := newFakeDaemon(t)
	f.on("POST /api/mcp-servers/notion/call", ok(map[string]any{"is_error": true, "text": "it broke"}))
	res := f.run(runOpts{}, "mcp", "call", "notion", "fail")
	if res.code != 1 || res.stdout != "" || !strings.Contains(res.stderr, "it broke") {
		t.Errorf("exit %d out %q err %q", res.code, res.stdout, res.stderr)
	}

	f = newFakeDaemon(t)
	f.on("POST /api/mcp-servers/other/call", problem(403, "forbidden", "Forbidden", `"other" is not among this run's mcp_servers`, nil))
	res = f.run(runOpts{env: map[string]string{cli.EnvRunToken: "hal_run_abc"}}, "mcp", "call", "other", "x")
	if res.code != 1 || !strings.Contains(res.stderr, "not among this run's mcp_servers") {
		t.Errorf("refused: exit %d err %q", res.code, res.stderr)
	}
}

func TestMCPCallArgumentsAndUsage(t *testing.T) {
	f := newFakeDaemon(t)
	for name, args := range map[string][]string{
		"not an object": {"mcp", "call", "notion", "echo", "--args", "[1]"},
		"not JSON":      {"mcp", "call", "notion", "echo", "--args", "{"},
		"no tool":       {"mcp", "call", "notion"},
	} {
		if res := f.run(runOpts{}, args...); res.code != 2 {
			t.Errorf("%s: exit %d, want 2 (stderr %q)", name, res.code, res.stderr)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests for invalid invocations", n)
	}
	f.on("POST /api/mcp-servers/notion/call", ok(map[string]any{"is_error": false, "text": "ok", "structured_content": map[string]any{"a": 1}}))
	res := f.run(runOpts{stdin: `{"x": 2}`}, "mcp", "call", "notion", "echo", "--args", "-")
	var body struct {
		Arguments map[string]any `json:"arguments"`
	}
	if res.code != 0 || json.Unmarshal([]byte(f.only().Body), &body) != nil || body.Arguments["x"] != float64(2) {
		t.Errorf("--args - : exit %d body %s", res.code, f.only().Body)
	}
}

func TestMCPStdioBridgeIsStillTheDefaultOfMCP(t *testing.T) {
	// `hal9001 mcp --token T` is what hal hands to the agents it starts; it still needs the daemon URL and starts the
	// proxy rather than treating the flags as a subcommand.
	res := run(t, runOpts{}, "mcp", "--token", "hal_mcp_x")
	if res.code != 2 || !strings.Contains(res.stderr, "--url is required") {
		t.Errorf("exit %d, stderr %q", res.code, res.stderr)
	}
	res = run(t, runOpts{}, "mcp", "nope")
	if res.code != 2 || !strings.Contains(res.stderr, `unknown command`) {
		t.Errorf("unknown subcommand: exit %d, stderr %q", res.code, res.stderr)
	}
	res = run(t, runOpts{}, "mcp", "--help")
	if res.code != 0 || !strings.Contains(res.stdout, "call") {
		t.Errorf("help: exit %d, stdout %q", res.code, res.stdout)
	}
}
