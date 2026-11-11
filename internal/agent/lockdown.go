package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Runtime kinds ApplyLockdown knows (docs/acp-lockdown.md).
const (
	KindClaude = "claude"
	KindCodex  = "codex"
	KindOmp    = "omp"
)

// DetectKind guesses the runtime family from its command: claude-agent-acp, codex-acp or omp as any argv element.
func DetectKind(command []string) string {
	for _, a := range command {
		switch base := filepath.Base(a); {
		case strings.HasPrefix(base, "claude-agent-acp"):
			return KindClaude
		case strings.HasPrefix(base, "codex-acp"):
			return KindCodex
		case base == "omp":
			return KindOmp
		}
	}
	return ""
}

// GuardPrompt makes sure the first line of a prompt cannot be taken for a slash command: omp (verified), Codex and
// Claude run a prompt that starts with "/" as a command before the model sees it. Text that already starts with
// something else is returned unchanged; the hal context preamble satisfies this by itself.
func GuardPrompt(text string) string {
	if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "/") {
		return "[hal9001]\n" + text
	}
	return text
}

func (l Lockdown) check(cwd string) error {
	if l.Err != "" {
		return fmt.Errorf("lockdown of this runtime is not in place: %s", l.Err)
	}
	for _, name := range l.RefuseCwdEntries {
		if _, err := os.Lstat(filepath.Join(cwd, name)); err == nil {
			return fmt.Errorf("working directory %s contains %s, which this runtime would load as repository configuration (it could start MCP servers the lockdown does not control): remove it or use another directory", cwd, name)
		}
	}
	return nil
}

// LockdownOptions describe where lockdown files go and the MCP server name hal injects.
type LockdownOptions struct {
	// Dir is <data>/runtimes: generated files and default isolated homes live here.
	Dir string
	// Server is the name of hal's MCP server in session/new.
	Server string
	// MCPCallTimeout bounds one MCP call that blocks (e.g. on a human approval). Default 24h.
	MCPCallTimeout time.Duration
	// ManagedAdapter marks Command as the adapter hal9001 installed and resolved itself, `<node> <script>` with
	// absolute paths: the Codex empty HOME then wraps the whole command instead of being inserted before a
	// `codex-acp` element, which such a command does not have.
	ManagedAdapter bool
	// AdapterVersion is the version of the adapter behind Command; a new version regenerates the Codex model
	// catalog even when the command line is the same.
	AdapterVersion string
}

// ApplyLockdown returns cfg configured so the agent has no built-in tools and can only call the MCP tools hal injects
// (docs/acp-lockdown.md). kind is KindClaude, KindCodex or KindOmp ("" detects it from the command; an unknown runtime
// is returned unchanged). Generated files are written under opt.Dir. A failure is reported in cfg.Lockdown.Err, which
// makes every run of the runtime fail instead of starting unlocked.
func ApplyLockdown(cfg RuntimeConfig, kind string, opt LockdownOptions) RuntimeConfig {
	if kind == "" {
		kind = DetectKind(cfg.Command)
	}
	if opt.Server == "" {
		opt.Server = "hal"
	}
	if opt.MCPCallTimeout <= 0 {
		opt.MCPCallTimeout = 24 * time.Hour
	}
	cfg.Env = copyMap(cfg.Env)
	var err error
	switch kind {
	case KindClaude:
		cfg = lockClaude(cfg, opt)
	case KindCodex:
		cfg, err = lockCodex(cfg, opt)
	case KindOmp:
		cfg, err = lockOmp(cfg, opt)
	default:
		return cfg
	}
	cfg.Lockdown.Kind = kind
	if err != nil {
		cfg.Lockdown.Err = err.Error()
	}
	return cfg
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+4)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func lockClaude(cfg RuntimeConfig, opt LockdownOptions) RuntimeConfig {
	cfg.Modes = ModeNames{Ask: "default", Full: "default"}
	meta := map[string]any{}
	for k, v := range cfg.SessionMeta {
		meta[k] = v
	}
	cc, _ := meta["claudeCode"].(map[string]any)
	cc = cloneAny(cc)
	opts, _ := cc["options"].(map[string]any)
	opts = cloneAny(opts)
	opts["tools"] = []string{}
	opts["strictMcpConfig"] = true
	opts["settingSources"] = []string{}
	opts["allowedTools"] = []string{"mcp__" + opt.Server}
	cc["options"] = opts
	meta["claudeCode"] = cc
	cfg.SessionMeta = meta
	return cfg
}

func cloneAny(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// codexConfig is the CODEX_CONFIG JSON of docs/acp-lockdown.md.
const codexConfig = `{"features":{"shell_tool":false,"unified_exec":false,"view_image":false,"image_generation":false,"apps":false,` +
	`"plugins":false,"remote_plugin":false,"tool_suggest":false,"skill_search":false,"skill_mcp_dependency_install":false,` +
	`"hooks":false,"goals":false,"memories":false,"multi_agent":false,"multi_agent_v2":false,"browser_use":false,` +
	`"browser_use_external":false,"computer_use":false,"in_app_browser":false,"sleep_tool":false,"recommended_plugins":false},` +
	`"agents":{"enabled":false},"web_search":"disabled",` +
	`"tools":{"experimental_request_user_input":{"enabled":false}},` +
	`"skills":{"include_instructions":false,"bundled":{"enabled":false}},` +
	`"notify":[],"project_doc_max_bytes":0}`

var catalogMu sync.Mutex

func lockCodex(cfg RuntimeConfig, opt LockdownOptions) (RuntimeConfig, error) {
	cfg.Modes = ModeNames{Ask: "read-only", Full: "read-only"}
	cfg.Lockdown.RefuseCwdEntries = []string{".codex"}
	home := cfg.Env["CODEX_HOME"]
	if home == "" {
		home = filepath.Join(opt.Dir, "codex-home")
		cfg.Env["CODEX_HOME"] = home
	}
	fake := filepath.Join(opt.Dir, "codex-fakehome")
	cfg.Env["DISABLE_MCP_CONFIG_FILTERING"] = "true"
	cfg.Env["CODEX_CONFIG"] = codexConfig
	// An empty HOME hides ~/.agents/skills and other home-based sources. The managed adapter runs as an absolute
	// `<node> <script>`, so the whole command gets it. In a command the user wrote it is set right before the
	// codex-acp element, after any wrapper (such as `mise exec`) that finds its installs through HOME.
	if opt.ManagedAdapter {
		cfg.Command = append([]string{"env", "HOME=" + fake}, cfg.Command...)
	} else {
		for i, a := range cfg.Command {
			if filepath.Base(a) == "codex-acp" || strings.HasPrefix(filepath.Base(a), "codex-acp@") {
				cmd := append([]string(nil), cfg.Command[:i]...)
				cmd = append(cmd, "env", "HOME="+fake)
				cfg.Command = append(cmd, cfg.Command[i:]...)
				break
			}
		}
	}
	for _, d := range []string{home, fake} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return cfg, err
		}
	}
	catalog := filepath.Join(home, "lockdown-catalog.json")
	toml := fmt.Sprintf("# generated by hal9001; do not edit\nmodel_catalog_json = %q\n\n[mcp_servers.%s]\ncommand = \"/usr/bin/false\"\ndefault_tools_approval_mode = \"approve\"\ntool_timeout_sec = %d\n",
		catalog, opt.Server, int(opt.MCPCallTimeout/time.Second))
	if err := writeIfChanged(filepath.Join(home, "config.toml"), []byte(toml)); err != nil {
		return cfg, err
	}
	return cfg, ensureCatalog(cfg, opt.AdapterVersion, home, catalog)
}

// ensureCatalog writes the bundled model catalog with the tool fields switched off. It is regenerated when the
// codex-acp command or the adapter version changes (a new adapter version brings a new bundled catalog).
func ensureCatalog(cfg RuntimeConfig, adapterVersion, home, catalog string) error {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	sum := sha256.Sum256([]byte(strings.Join(cfg.Command, "\x00") + "\x00" + adapterVersion))
	stamp := hex.EncodeToString(sum[:])
	stampFile := catalog + ".stamp"
	if b, err := os.ReadFile(stampFile); err == nil && string(b) == stamp { //nolint:gosec // generated file under hal9001's data directory
		if _, err := os.Stat(catalog); err == nil {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Command[0], append(append([]string(nil), cfg.Command[1:]...), "cli", "debug", "models", "--bundled")...) //nolint:gosec // the configured runtime command
	cmd.Env = os.Environ()
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+home)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("print the bundled codex model catalog: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		return fmt.Errorf("parse the bundled codex model catalog: %w", err)
	}
	models, ok := doc["models"].([]any)
	if !ok {
		return errors.New("the bundled codex model catalog has no models list")
	}
	for _, m := range models {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		mm["apply_patch_tool_type"] = nil
		mm["experimental_supported_tools"] = []any{}
		mm["tool_mode"] = "direct"
		mm["multi_agent_version"] = nil
		mm["shell_type"] = "disabled"
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := writeIfChanged(catalog, b); err != nil {
		return err
	}
	return os.WriteFile(stampFile, []byte(stamp), 0o600)
}

const ompOverlay = `# generated by hal9001; do not edit
memory:
  backend: "off"
autolearn:
  enabled: false
skills:
  enabled: false
mcp:
  enableProjectConfig: false
  startupTimeoutMs: 30000
tools:
  xdev: false
disabledProviders: [native, claude, claude-plugins, codex, gemini, github, opencode, cursor, windsurf, vscode, agents-md, agents, agent-plugins, omp-plugins, skillshare, mcp-json]
`

func lockOmp(cfg RuntimeConfig, opt LockdownOptions) (RuntimeConfig, error) {
	cfg.Modes = ModeNames{}
	cfg.Elicitation = false
	cfg.Lockdown.WaitForMCP = true
	overlay := filepath.Join(opt.Dir, "omp-lockdown.yml")
	flags := []string{"--no-tools", "--no-extensions", "--no-skills", "--no-rules", "--no-context-files", "--no-lsp", "--no-pty", "--config", overlay}
	at := len(cfg.Command)
	for i, a := range cfg.Command {
		if a == "acp" {
			at = i + 1
			break
		}
	}
	cmd := append([]string(nil), cfg.Command[:at]...)
	cmd = append(cmd, flags...)
	cfg.Command = append(cmd, cfg.Command[at:]...)
	cfg.Ask = PolicySpawn{Args: []string{"--approval-mode", "write"}, Env: cfg.Ask.Env}
	cfg.Full = PolicySpawn{Args: []string{"--approval-mode", "write"}, Env: cfg.Full.Env}
	if cfg.Env["PI_CODING_AGENT_DIR"] == "" {
		cfg.Env["PI_CODING_AGENT_DIR"] = filepath.Join(opt.Dir, "omp-home")
	}
	cfg.Env["OMP_MCP_TIMEOUT_MS"] = "0"
	if err := os.MkdirAll(opt.Dir, 0o700); err != nil {
		return cfg, err
	}
	return cfg, writeIfChanged(overlay, []byte(ompOverlay))
}

func writeIfChanged(path string, data []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) { //nolint:gosec // generated file under hal9001's data directory
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
