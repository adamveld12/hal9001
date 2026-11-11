// Package runtimes finds the agent CLIs (claude, codex, omp) and their ACP adapters on this machine, installs the
// adapters hal9001 manages, and resolves a configured runtime to the argv the engine starts. It replaces running the
// adapters through mise or npx: a runtime either has a command the user wrote, an adapter hal9001 installed into
// <home>/data/adapters (run as `<node> <script>`, nothing is fetched at run time), or the CLI's own ACP mode (omp).
package runtimes

import (
	"path/filepath"

	"hal9001/internal/agent"
	"hal9001/internal/spec"
)

// Adapter versions the lockdown (docs/acp-lockdown.md) was tested with. hal9001 installs exactly these.
const (
	ClaudeAdapterVersion = "0.100.0"
	CodexAdapterVersion  = "2.5.0"
)

// MinNodeMajor is the oldest Node.js major the ACP adapters run on.
const MinNodeMajor = 22

// kindInfo is what hal9001 knows about one runtime family.
type kindInfo struct {
	kind string
	// title names the product in messages.
	title string
	// cli is the executable of the agent's own command line tool.
	cli string
	// loginCommand is what the user runs to sign in; it is shown in warnings.
	loginCommand string
	// installURL is where to get the CLI.
	installURL string
	// adapter is the ACP adapter package hal9001 can install; nil when the CLI speaks ACP itself (omp).
	adapter *adapterInfo
}

// adapterInfo describes an ACP adapter installed with npm.
type adapterInfo struct {
	// pkg is the npm package (scope included) and bin the name of its executable, also the folder name under
	// <data>/adapters.
	pkg, bin, pin string
}

var kinds = map[string]kindInfo{
	spec.RuntimeKindClaude: {
		kind: spec.RuntimeKindClaude, title: "Claude Code", cli: "claude", loginCommand: "claude login",
		installURL: "https://docs.claude.com/en/docs/claude-code/setup",
		adapter:    &adapterInfo{pkg: "@agentclientprotocol/claude-agent-acp", bin: "claude-agent-acp", pin: ClaudeAdapterVersion},
	},
	spec.RuntimeKindCodex: {
		kind: spec.RuntimeKindCodex, title: "Codex", cli: "codex", loginCommand: "codex login",
		installURL: "https://developers.openai.com/codex/cli",
		adapter:    &adapterInfo{pkg: "@agentclientprotocol/codex-acp", bin: "codex-acp", pin: CodexAdapterVersion},
	},
	spec.RuntimeKindOmp: {
		kind: spec.RuntimeKindOmp, title: "omp", cli: "omp", loginCommand: "omp login <provider>",
		installURL: "https://omp.sh",
	},
}

// kindOf is the runtime family of rt: its kind, else the one its command names ("" for an unknown agent).
func kindOf(rt spec.Runtime) string {
	if rt.Kind != "" {
		return rt.Kind
	}
	return agent.DetectKind(rt.Command)
}

// AdapterDir is where the managed adapter of kind is installed under data, or "" when the kind has none.
func AdapterDir(data, kind string) string {
	info, ok := kinds[kind]
	if !ok || info.adapter == nil {
		return ""
	}
	return filepath.Join(data, "adapters", info.adapter.bin)
}
