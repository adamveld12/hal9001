package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hal9001/internal/agent"
	"hal9001/internal/agent/fakeagent"
)

func TestLockdownClaudeMetaKeepsUserOptionsAndWins(t *testing.T) {
	cfg := agent.RuntimeConfig{
		Command:     []string{"mise", "exec", "npm:@agentclientprotocol/claude-agent-acp@0.100.0", "--", "claude-agent-acp"},
		SessionMeta: map[string]any{"claudeCode": map[string]any{"options": map[string]any{"settingSources": []string{"project"}, "model": "x"}}},
	}
	out := agent.ApplyLockdown(cfg, "", agent.LockdownOptions{Dir: t.TempDir()})
	b, _ := json.Marshal(out.SessionMeta)
	for _, want := range []string{`"tools":[]`, `"strictMcpConfig":true`, `"settingSources":[]`, `"allowedTools":["mcp__hal"]`, `"model":"x"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("session meta lacks %s: %s", want, b)
		}
	}
	if out.Lockdown.Kind != agent.KindClaude || out.Lockdown.Err != "" || out.Modes.Ask != "default" || out.Modes.Full != "default" {
		t.Errorf("lockdown = %+v modes %+v", out.Lockdown, out.Modes)
	}
	if strings.Contains(string(mustJSON(cfg.SessionMeta)), `"tools"`) {
		t.Error("the input config was modified")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestLockdownOmpFlagsOverlayAndEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := agent.RuntimeConfig{Command: []string{"omp", "acp"}, Elicitation: true,
		Ask: agent.PolicySpawn{Args: []string{"--approval-mode", "always-ask"}}, Full: agent.PolicySpawn{Args: []string{"--approval-mode", "yolo"}}}
	out := agent.ApplyLockdown(cfg, "", agent.LockdownOptions{Dir: dir})
	if out.Lockdown.Err != "" {
		t.Fatal(out.Lockdown.Err)
	}
	overlay := filepath.Join(dir, "omp-lockdown.yml")
	want := []string{"omp", "acp", "--no-tools", "--no-extensions", "--no-skills", "--no-rules", "--no-context-files", "--no-lsp", "--no-pty", "--config", overlay}
	if !slices.Equal(out.Command, want) {
		t.Errorf("command = %v", out.Command)
	}
	if !slices.Equal(out.Ask.Args, []string{"--approval-mode", "write"}) || !slices.Equal(out.Full.Args, []string{"--approval-mode", "write"}) || out.Elicitation {
		t.Errorf("spawn args %v / %v elicitation %v", out.Ask.Args, out.Full.Args, out.Elicitation)
	}
	if out.Env["OMP_MCP_TIMEOUT_MS"] != "0" || out.Env["PI_CODING_AGENT_DIR"] == "" {
		t.Errorf("env = %v", out.Env)
	}
	b, err := os.ReadFile(overlay)
	if err != nil || !strings.Contains(string(b), `backend: "off"`) || !strings.Contains(string(b), "disabledProviders") {
		t.Errorf("overlay: %q %v", b, err)
	}
}

func TestLockdownCodexHomeCatalogAndCwdRefusal(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex-acp")
	script := "#!/bin/sh\necho '{\"models\":[{\"slug\":\"m\",\"shell_type\":\"shell_command\",\"tool_mode\":\"code\",\"apply_patch_tool_type\":\"freeform\"}]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	cfg := agent.RuntimeConfig{Command: []string{bin}}
	out := agent.ApplyLockdown(cfg, "", agent.LockdownOptions{Dir: dir, MCPCallTimeout: 2 * 3600e9})
	if out.Lockdown.Err != "" {
		t.Fatal(out.Lockdown.Err)
	}
	if len(out.Command) != 3 || out.Command[0] != "env" || !strings.HasPrefix(out.Command[1], "HOME=") || out.Command[2] != bin {
		t.Errorf("command = %v", out.Command)
	}
	home := out.Env["CODEX_HOME"]
	toml, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	for _, want := range []string{"[mcp_servers.hal]", `default_tools_approval_mode = "approve"`, "tool_timeout_sec = 7200", "lockdown-catalog.json"} {
		if !strings.Contains(string(toml), want) {
			t.Errorf("config.toml lacks %q:\n%s", want, toml)
		}
	}
	cat, _ := os.ReadFile(filepath.Join(home, "lockdown-catalog.json"))
	for _, want := range []string{`"shell_type":"disabled"`, `"tool_mode":"direct"`, `"apply_patch_tool_type":null`} {
		if !strings.Contains(string(cat), want) {
			t.Errorf("catalog lacks %s: %s", want, cat)
		}
	}
	if out.Env["DISABLE_MCP_CONFIG_FILTERING"] != "true" || !strings.Contains(out.Env["CODEX_CONFIG"], `"shell_tool":false`) {
		t.Errorf("env = %v", out.Env)
	}

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("x")}}}})
	req := e.req(agent.ModeReadonly, "")
	req.Config.Lockdown = out.Lockdown
	req.Cwd = repo
	if _, err := e.exec.Run(t.Context(), req); err == nil || !strings.Contains(err.Error(), ".codex") {
		t.Errorf("a cwd with .codex was accepted: %v", err)
	}
	req.Config.Lockdown.Err = "boom"
	req.Cwd = t.TempDir()
	if _, err := e.exec.Run(t.Context(), req); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a failed lockdown still started the agent: %v", err)
	}
}

func TestLockdownLeavesUnknownRuntimesAlone(t *testing.T) {
	cfg := agent.RuntimeConfig{Command: []string{"my-agent"}}
	out := agent.ApplyLockdown(cfg, "", agent.LockdownOptions{Dir: t.TempDir()})
	if out.Lockdown.Kind != "" || !slices.Equal(out.Command, cfg.Command) {
		t.Errorf("out = %+v", out)
	}
}

// fakeCodexNode writes an executable that prints a model catalog whatever its arguments, standing in for
// `node <adapter script>`, and counts its runs in dir/runs.
func fakeCodexNode(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "node")
	script := "#!/bin/sh\necho x >> '" + filepath.Join(dir, "runs") + "'\necho '{\"models\":[{\"slug\":\"m\",\"shell_type\":\"shell_command\"}]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return bin
}

func TestLockdownCodexHomeWrapperPerCommandForm(t *testing.T) {
	dir := t.TempDir()
	node := fakeCodexNode(t, dir)
	script := "/adapters/codex-acp/node_modules/@agentclientprotocol/codex-acp/dist/index.js"

	// The adapter hal9001 installed: the whole absolute command runs with the empty HOME.
	managed := agent.ApplyLockdown(agent.RuntimeConfig{Command: []string{node, script}}, "codex",
		agent.LockdownOptions{Dir: dir, ManagedAdapter: true, AdapterVersion: "2.5.0"})
	if managed.Lockdown.Err != "" {
		t.Fatal(managed.Lockdown.Err)
	}
	if len(managed.Command) != 4 || managed.Command[0] != "env" || !strings.HasPrefix(managed.Command[1], "HOME=") || managed.Command[2] != node || managed.Command[3] != script {
		t.Errorf("managed command = %v, want env HOME=<fake> <node> <script>", managed.Command)
	}

	// A command the user wrote keeps the old placement: after a wrapper, right before the codex-acp element.
	wrapped := agent.ApplyLockdown(agent.RuntimeConfig{Command: []string{node, "exec", "tool", "--", "codex-acp"}}, "codex", agent.LockdownOptions{Dir: dir})
	want := []string{node, "exec", "tool", "--", "env", "HOME=" + filepath.Join(dir, "codex-fakehome"), "codex-acp"}
	if !slices.Equal(wrapped.Command, want) {
		t.Errorf("wrapped command = %v, want %v", wrapped.Command, want)
	}

	// A user command without a codex-acp element is left as written (no HOME is invented for a wrapper that needs it).
	plain := agent.ApplyLockdown(agent.RuntimeConfig{Command: []string{node, "serve"}}, "codex", agent.LockdownOptions{Dir: dir})
	if !slices.Equal(plain.Command, []string{node, "serve"}) {
		t.Errorf("plain command = %v", plain.Command)
	}
}

func TestLockdownCodexCatalogIsRegeneratedForANewAdapterVersion(t *testing.T) {
	dir := t.TempDir()
	node := fakeCodexNode(t, dir)
	run := func(version string) {
		out := agent.ApplyLockdown(agent.RuntimeConfig{Command: []string{node, "script.js"}}, "codex",
			agent.LockdownOptions{Dir: dir, ManagedAdapter: true, AdapterVersion: version})
		if out.Lockdown.Err != "" {
			t.Fatal(out.Lockdown.Err)
		}
	}
	runs := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "runs"))
		return strings.Count(string(b), "x")
	}
	run("2.5.0")
	run("2.5.0")
	if runs() != 1 {
		t.Errorf("catalog generated %d times for the same command and version, want 1", runs())
	}
	run("2.6.0")
	if runs() != 2 {
		t.Errorf("catalog generated %d times after an adapter upgrade, want 2", runs())
	}
}

func TestRuntimeWithoutACommandFailsRunsWithItsReason(t *testing.T) {
	e := newEnv(t, fakeagent.Script{Turns: []fakeagent.Turn{{Steps: []fakeagent.Step{msg("x")}}}})
	req := e.req(agent.ModeReadonly, "")
	req.Config = agent.RuntimeConfig{Unavailable: `runtime "claude" is not available: the adapter is not installed`}
	if _, err := e.exec.Run(t.Context(), req); err == nil || !strings.Contains(err.Error(), "adapter is not installed") {
		t.Errorf("Run = %v, want the unavailable reason", err)
	}
}
