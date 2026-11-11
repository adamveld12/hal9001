package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyConfig = `# my config

max_agent_runs: 3 # keep this comment

# agents
runtimes:
  claude:
    command: [mise, exec, "npm:@agentclientprotocol/claude-agent-acp@0.100.0", --, claude-agent-acp]
    env: {FOO: bar}
  codex:
    kind: codex
    command:
      - mise
      - exec
      - "npm:@agentclientprotocol/codex-acp@2.4.0"
      - --
      - codex-acp
  omp:
    command: [omp, acp]
  pinned:
    command: [mise, exec, "npm:@agentclientprotocol/claude-agent-acp@0.100.0", --, claude-agent-acp, --extra]
  wrapped:
    kind: omp
    command: [mise, exec, "npm:@agentclientprotocol/claude-agent-acp@0.100.0", --, claude-agent-acp]
`

func writeHomeConfig(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, FileConfig), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestMigrateRuntimeCommandsDropsOnlyTheSeededMiseForm(t *testing.T) {
	home := writeHomeConfig(t, legacyConfig)
	migrated, err := MigrateRuntimeCommands(home)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(migrated, ",") != "claude,codex" {
		t.Fatalf("migrated = %v, want claude and codex", migrated)
	}
	got, _ := os.ReadFile(filepath.Join(home, FileConfig))
	cfg, ds := CheckConfig(got)
	if len(ds) != 0 {
		t.Fatalf("migrated config does not validate: %v\n%s", ds, got)
	}
	if rt := cfg.Runtimes["claude"]; rt.Kind != "claude" || len(rt.Command) != 0 || rt.Env["FOO"] != "bar" {
		t.Errorf("claude = %+v, want kind claude, no command, env kept", rt)
	}
	if rt := cfg.Runtimes["codex"]; rt.Kind != "codex" || len(rt.Command) != 0 {
		t.Errorf("codex = %+v, want kind codex, no command", rt)
	}
	if strings.Join(cfg.Runtimes["omp"].Command, " ") != "omp acp" {
		t.Errorf("omp command changed: %v", cfg.Runtimes["omp"].Command)
	}
	if len(cfg.Runtimes["pinned"].Command) != 6 {
		t.Errorf("customized command was touched: %v", cfg.Runtimes["pinned"].Command)
	}
	if rt := cfg.Runtimes["wrapped"]; rt.Kind != "omp" || len(rt.Command) != 5 {
		t.Errorf("runtime with another kind was touched: %+v", rt)
	}
	for _, c := range []string{"# my config", "# keep this comment", "# agents"} {
		if !strings.Contains(string(got), c) {
			t.Errorf("comment %q lost:\n%s", c, got)
		}
	}
	bak, err := os.ReadFile(filepath.Join(home, FileConfigBackup))
	if err != nil || string(bak) != legacyConfig {
		t.Errorf("backup = %q (%v), want the original file", bak, err)
	}
}

func TestMigrateRuntimeCommandsIsIdempotent(t *testing.T) {
	home := writeHomeConfig(t, legacyConfig)
	if _, err := MigrateRuntimeCommands(home); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(home, FileConfig))
	if err := os.Remove(filepath.Join(home, FileConfigBackup)); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateRuntimeCommands(home)
	if err != nil || len(migrated) != 0 {
		t.Fatalf("second run = %v, %v; want nothing to do", migrated, err)
	}
	second, _ := os.ReadFile(filepath.Join(home, FileConfig))
	if string(first) != string(second) {
		t.Error("second run rewrote the file")
	}
	if _, err := os.Stat(filepath.Join(home, FileConfigBackup)); err == nil {
		t.Error("second run wrote a backup")
	}
}

func TestMigrateRuntimeCommandsLeavesCustomConfigAlone(t *testing.T) {
	const custom = "runtimes:\n  claude:\n    command: [/opt/claude-agent-acp]\n"
	home := writeHomeConfig(t, custom)
	migrated, err := MigrateRuntimeCommands(home)
	if err != nil || len(migrated) != 0 {
		t.Fatalf("= %v, %v", migrated, err)
	}
	if got, _ := os.ReadFile(filepath.Join(home, FileConfig)); string(got) != custom {
		t.Errorf("config changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, FileConfigBackup)); err == nil {
		t.Error("a backup was written although nothing changed")
	}
}

func TestMigrateRuntimeCommandsToleratesMissingAndBrokenConfig(t *testing.T) {
	if m, err := MigrateRuntimeCommands(t.TempDir()); err != nil || m != nil {
		t.Errorf("missing config: %v, %v", m, err)
	}
	home := writeHomeConfig(t, "runtimes: [unclosed\n")
	if m, err := MigrateRuntimeCommands(home); err != nil || m != nil {
		t.Errorf("broken config: %v, %v", m, err)
	}
}

func TestRuntimeNeedsCommandOrKind(t *testing.T) {
	if _, ds := CheckConfig([]byte("runtimes: {x: {kind: claude}}\n")); len(ds) != 0 {
		t.Errorf("kind without command: %v", ds)
	}
	_, ds := CheckConfig([]byte("runtimes: {x: {env: {A: b}}}\n"))
	if len(ds) != 1 || ds[0].Code != CodeRequired || ds[0].Path != "runtimes.x.command" {
		t.Errorf("neither command nor kind: %v", ds)
	}
}
