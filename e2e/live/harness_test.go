// Package live holds the live smoke tests: the real daemon (in process, on
// 127.0.0.1:0, with a temporary home) against real ACP agents, the real `gh`
// CLI and the starter sensors. They spend model tokens and need logins, so every
// test skips unless HAL9001_LIVE=1; see docs/live-smoke.md.
package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/content"
	"hal9001/internal/daemon"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
)

const (
	envLive     = "HAL9001_LIVE"
	envEvidence = "HAL9001_LIVE_EVIDENCE"
)

// requireLive skips the test unless live tests were asked for.
func requireLive(t *testing.T) {
	t.Helper()
	if testing.Short() || os.Getenv(envLive) != "1" {
		t.Skipf("live test: set %s=1 (without -short) to run it against real agents and services; see docs/live-smoke.md", envLive)
	}
}

// stack is one in-process daemon on a loopback port with a temporary home.
type stack struct {
	t    *testing.T
	home string
	base string
	http *http.Client
}

type stackOpts struct {
	// keepSensors lists seeded sensors that stay as shipped; every other seeded
	// sensor is replaced by a silent one so nothing polls credentials or noise.
	keepSensors []string
	// configPatch rewrites the seeded config.yaml (nil keeps it).
	configPatch func(string) string
	// env is written to <home>/env.
	env string
	// binDir, when set, is put first on the PATH of the daemon and everything it starts (for example a
	// directory holding a built hal9001, which assist agents run as `hal9001 assist test`).
	binDir string
}

// startStack seeds a temp home from the shipped content, quiets the starter
// sensors and flows, and runs the real daemon on 127.0.0.1:0 with the PATH that
// `hal9001 install` would capture from the login shell.
func startStack(t *testing.T, o stackOpts) *stack {
	t.Helper()
	adapters := adapterPaths(t) // resolved in the repo, where mise.toml pins them
	// launchd starts the daemon in /, outside any repo: mise shims resolve
	// tool versions from the working directory, so do the same here.
	t.Chdir("/")
	home := t.TempDir()
	if _, err := spec.Init(home, content.FS); err != nil {
		t.Fatal(err)
	}
	keep := map[string]bool{}
	for _, k := range o.keepSensors {
		keep[k] = true
	}
	sensors, err := filepath.Glob(filepath.Join(home, "sensors", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sensors {
		id := strings.TrimSuffix(filepath.Base(p), ".yaml")
		if keep[id] {
			continue
		}
		silent := fmt.Sprintf("id: %s\ninterval: 24h\nlang: bash\nscript: 'true'\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n", id)
		writeFile(t, p, silent)
	}
	// The seeded demo flow reacts to demo-ticker; the live checks run their own flows.
	demo := filepath.Join(home, "flows", "demo.yaml")
	if b, err := os.ReadFile(demo); err == nil {
		writeFile(t, demo, strings.Replace(string(b), "enabled: true", "enabled: false", 1))
	}
	patchRuntimeCommands(t, home, adapters)
	if o.configPatch != nil {
		cfg := filepath.Join(home, "config.yaml")
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, cfg, o.configPatch(string(b)))
	}
	if o.env != "" {
		writeFile(t, filepath.Join(home, "env"), o.env)
	}

	// Capture the login-shell PATH from launchd's minimal PATH, as a fresh
	// login would, not from the (mise-managed) PATH of the test process.
	t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
	pathCtx, cancelPath := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelPath()
	basePATH, err := runner.LoginShellPATH(pathCtx, "", 0)
	if err != nil {
		t.Fatalf("login shell PATH (what `hal9001 install` captures): %v", err)
	}

	if o.binDir != "" {
		basePATH = o.binDir + string(os.PathListSeparator) + basePATH
	}
	// Under launchd the plist's PATH is both the daemon's own PATH (which Go's
	// exec uses to find the runtime commands) and the PATH of every node.
	t.Setenv("PATH", basePATH)

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan net.Addr, 1)
	done := make(chan error, 1)
	level := slog.LevelWarn
	if os.Getenv("HAL9001_LIVE_DEBUG") != "" {
		level = slog.LevelDebug
	}
	go func() {
		done <- daemon.Run(ctx, daemon.Options{
			Home: home, Version: "live", BasePATH: basePATH, ListenAddr: "127.0.0.1:0",
			Log:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
			Ready: func(a net.Addr) { ready <- a },
		})
	}()
	var addr net.Addr
	select {
	case addr = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(60 * time.Second):
		cancel()
		t.Fatal("daemon did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon shutdown: %v", err)
			}
		case <-time.After(60 * time.Second):
			t.Error("daemon did not shut down")
		}
	})
	return &stack{t: t, home: home, base: "http://" + addr.String(), http: &http.Client{Timeout: 5 * time.Minute}}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// call sends a JSON request and decodes the JSON response into out (may be nil).
// It returns the HTTP status; it fails the test only on transport errors.
func (s *stack) call(method, path string, body, out any) int {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	if out != nil && resp.StatusCode < 300 && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			s.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	if resp.StatusCode >= 300 {
		s.t.Logf("%s %s -> %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode
}

// must calls and fails the test unless the status is want.
func (s *stack) must(want int, method, path string, body, out any) {
	s.t.Helper()
	if got := s.call(method, path, body, out); got != want {
		s.t.Fatalf("%s %s = %d, want %d", method, path, got, want)
	}
}

// Minimal views of the API records the checks read.
type flowRunBrief struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type injectResult struct {
	Event struct {
		ID string `json:"id"`
	} `json:"event"`
	Deduplicated bool           `json:"deduplicated"`
	FlowRuns     []flowRunBrief `json:"flow_runs"`
}

type flowRunDetail struct {
	FlowRun struct {
		ID     string  `json:"id"`
		Status string  `json:"status"`
		DryRun bool    `json:"dry_run"`
		Error  *string `json:"error"`
	} `json:"flow_run"`
	NodeRuns []struct {
		ID       string   `json:"id"`
		NodeID   string   `json:"node_id"`
		Status   string   `json:"status"`
		Decision *string  `json:"decision"`
		Reason   *string  `json:"reason"`
		Error    *string  `json:"error"`
		Ports    []string `json:"ports"`
		Stdout   any      `json:"stdout"`
		Stderr   any      `json:"stderr"`
	} `json:"node_runs"`
}

type agentRun struct {
	ID           string          `json:"id"`
	NodeID       string          `json:"node_id"`
	Runtime      string          `json:"runtime"`
	Mode         string          `json:"mode"`
	ApproveStyle *string         `json:"approve_style"`
	Policy       string          `json:"policy"`
	Phase        string          `json:"phase"`
	SessionID    *string         `json:"session_id"`
	ResumedVia   *string         `json:"resumed_via"`
	Fallback     *string         `json:"resume_fallback"`
	Cwd          *string         `json:"cwd"`
	Argv         []string        `json:"argv"`
	Status       string          `json:"status"`
	StopReason   *string         `json:"stop_reason"`
	FinalMessage *string         `json:"final_message"`
	Result       json.RawMessage `json:"result"`
	ParseError   bool            `json:"parse_error"`
	Error        *string         `json:"error"`
	AgentInfo    json.RawMessage `json:"agent_info"`
}

type permissionRequest struct {
	ID          string          `json:"id"`
	ToolCallID  string          `json:"tool_call_id"`
	ToolKind    string          `json:"tool_kind"`
	Title       string          `json:"title"`
	RawInput    json.RawMessage `json:"raw_input"`
	Decision    *string         `json:"decision"`
	DecidedBy   *string         `json:"decided_by"`
	MatchedRule *string         `json:"matched_rule"`
	Reason      *string         `json:"reason"`
	ApprovalID  *string         `json:"approval_id"`
}

type approval struct {
	ID                string             `json:"id"`
	AgentRunID        string             `json:"agent_run_id"`
	FlowRunID         string             `json:"flow_run_id"`
	Type              string             `json:"type"`
	Status            string             `json:"status"`
	ProposalMD        *string            `json:"proposal_md"`
	PermissionRequest *permissionRequest `json:"permission_request"`
}

type inboxItem struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Priority   string  `json:"priority"`
	SummaryMD  string  `json:"summary_md"`
	AgentRunID *string `json:"agent_run_id"`
	FlowID     string  `json:"flow_id"`
	DryRun     bool    `json:"dry_run"`
	ParseError bool    `json:"parse_error"`
}

type sensorRun struct {
	ID        string  `json:"id"`
	SensorID  string  `json:"sensor_id"`
	Status    string  `json:"status"`
	Baseline  bool    `json:"baseline"`
	ExitCode  *int    `json:"exit_code"`
	ItemCount *int    `json:"item_count"`
	NewCount  *int    `json:"new_count"`
	Stdout    any     `json:"stdout"`
	Stderr    any     `json:"stderr"`
	Error     *string `json:"error"`
}

// createSensor/createFlow/createPrompt write through the API (so validation runs).
func (s *stack) createFlow(yaml string) {
	s.t.Helper()
	s.must(http.StatusCreated, http.MethodPost, "/api/flows", map[string]string{"yaml": yaml}, nil)
}

func (s *stack) createSensor(yaml string) {
	s.t.Helper()
	s.must(http.StatusCreated, http.MethodPost, "/api/sensors", map[string]string{"yaml": yaml}, nil)
}

func (s *stack) createPrompt(md string) {
	s.t.Helper()
	s.must(http.StatusCreated, http.MethodPost, "/api/prompts", map[string]string{"markdown": md}, nil)
}

// inject sends an item for a sensor and returns the created runs.
func (s *stack) inject(sensor string, item any) injectResult {
	s.t.Helper()
	var res injectResult
	s.must(http.StatusCreated, http.MethodPost, "/api/events?sensor_id="+url.QueryEscape(sensor), item, &res)
	return res
}

func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "killed", "interrupted":
		return true
	}
	return false
}

// waitFlowRun long-polls until the run is terminal or awaiting_approval or the
// deadline passes. The last detail is returned.
func (s *stack) waitFlowRun(id string, d time.Duration) flowRunDetail {
	s.t.Helper()
	deadline := time.Now().Add(d)
	var det flowRunDetail
	for {
		s.must(http.StatusOK, http.MethodGet, "/api/flow-runs/"+id+"?wait=20s", nil, &det)
		st := det.FlowRun.Status
		if terminal(st) || st == "awaiting_approval" {
			return det
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("flow run %s still %q after %s", id, st, d)
		}
	}
}

func (s *stack) agentRuns(flowRunID string) []agentRun {
	s.t.Helper()
	var list struct {
		Items []agentRun `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/agent-runs?limit=50&flow_run_id="+flowRunID, nil, &list)
	// API returns newest first; callers want phase order.
	for i, j := 0, len(list.Items)-1; i < j; i, j = i+1, j-1 {
		list.Items[i], list.Items[j] = list.Items[j], list.Items[i]
	}
	return list.Items
}

func (s *stack) permissionRequests(agentRunID string) []permissionRequest {
	s.t.Helper()
	var list struct {
		Items []permissionRequest `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/agent-runs/"+agentRunID+"/permission-requests", nil, &list)
	return list.Items
}

func (s *stack) approvals(query string) []approval {
	s.t.Helper()
	var list struct {
		Items []approval `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/approvals?limit=100&"+query, nil, &list)
	return list.Items
}

func (s *stack) inboxFor(flowID string) []inboxItem {
	s.t.Helper()
	var list struct {
		Items []inboxItem `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/inbox?limit=50&archived=any&flow_id="+flowID, nil, &list)
	return list.Items
}

func (s *stack) sensorRuns(sensor string) []sensorRun {
	s.t.Helper()
	var list struct {
		Items []sensorRun `json:"items"`
	}
	s.must(http.StatusOK, http.MethodGet, "/api/sensors/"+sensor+"/runs?limit=20", nil, &list)
	return list.Items
}

// pollSensor triggers a manual poll and waits for the run to finish.
func (s *stack) pollSensor(sensor string) sensorRun {
	s.t.Helper()
	var run sensorRun
	if code := s.call(http.MethodPost, "/api/sensors/"+sensor+"/poll", map[string]any{}, &run); code != http.StatusAccepted && code != http.StatusOK && code != http.StatusCreated {
		s.t.Fatalf("poll %s = %d", sensor, code)
	}
	s.must(http.StatusOK, http.MethodGet, "/api/sensor-runs/"+run.ID+"?wait=60s", nil, &run)
	return run
}

// evidence records a scenario's API responses for docs/live-smoke.md: logged,
// and written under $HAL9001_LIVE_EVIDENCE when set.
func evidence(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("EVIDENCE %s:\n%s", name, b)
	dir := os.Getenv(envEvidence)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// adapterPaths resolves the pinned npm ACP adapters to their installed
// scripts. The mise shims for them only work inside the repo (the pins live in
// its mise.toml), but agents run in their own cwd, so the live config names the
// scripts by absolute path. The scripts start with `#!/usr/bin/env node`; node
// comes from the login-shell PATH.
func adapterPaths(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for cmd, tool := range map[string]string{
		"claude-agent-acp": "npm:@agentclientprotocol/claude-agent-acp",
		"codex-acp":        "npm:@agentclientprotocol/codex-acp",
	} {
		c := exec.Command("mise", "where", tool)
		c.Dir = "../.." // repository root
		b, err := c.Output()
		if err != nil {
			t.Fatalf("mise where %s: %v (run `mise install` in the repo)", tool, err)
		}
		p := filepath.Join(strings.TrimSpace(string(b)), "node_modules", ".bin", cmd)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("adapter %s: %v", cmd, err)
		}
		out[cmd] = p
	}
	return out
}

// patchRuntimeCommands replaces bare adapter command names in the seeded
// config.yaml with absolute paths (a config that already pins them through
// `mise exec` is left as it is).
func patchRuntimeCommands(t *testing.T, home string, paths map[string]string) {
	t.Helper()
	cfg := filepath.Join(home, "config.yaml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for cmd, p := range paths {
		// The shipped config runs the adapters through `mise exec <tool>@<version> -- <adapter>`, which
		// needs no patching; only a bare adapter command is replaced by its absolute path.
		s = strings.Replace(s, "command: ["+cmd+"]", "command: ["+p+"]", 1)
	}
	writeFile(t, cfg, s)
}
