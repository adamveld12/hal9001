package assist

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hal9001/internal/agent"
	"hal9001/internal/spec"
	"hal9001/internal/tools"
)

// The app's configuration as the assistant sees it: ids and non-secret settings only, never env values, runtime
// commands, kubeconfig paths, hosts, keys or tokens. The files are rebuilt before every turn from the user's home.

// sandboxTools are the tools a sandboxed agent node gets in addition to the built-in ones.
var sandboxTools = []string{"fs_read", "fs_write", "fs_list", "fs_search", "shell"}

// configSandbox is one sandbox backend as the assistant sees it.
type configSandbox struct {
	ID      string
	Enabled bool
	Invalid bool // the file exists but does not validate
	Cfg     spec.Sandbox
}

// configSandboxes reads every backend, enabled or not, in display order.
func configSandboxes(home string) []configSandbox {
	var out []configSandbox
	for _, b := range spec.SandboxBackends {
		raw, _, err := spec.ReadFile(home, spec.KindSandbox, b)
		if err != nil {
			raw = nil // no file: the backend's defaults
		}
		cfg, ds := spec.CheckSandboxIn(home, b, raw)
		cs := configSandbox{ID: b}
		switch {
		case cfg == nil:
			cs.Invalid, cs.Cfg = true, spec.DefaultSandbox(b)
		case spec.HasErrors(ds):
			cs.Invalid, cs.Cfg = true, *cfg
		default:
			cs.Cfg, cs.Enabled = *cfg, cfg.Enabled
		}
		out = append(out, cs)
	}
	return out
}

// runtimeLockdown reports the runtime family and whether hal can lock the runtime down to its own tools.
func runtimeLockdown(rt spec.Runtime) (kind string, ok bool) {
	kind = rt.Kind
	if kind == "" {
		kind = agent.DetectKind(rt.Command)
	}
	switch kind {
	case agent.KindClaude, agent.KindCodex, agent.KindOmp:
		return kind, true
	}
	return kind, false
}

func runtimeNames(ws *spec.Workspace) []string {
	var names []string
	if ws.Config.Value != nil {
		for name := range ws.Config.Value.Runtimes {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func runtimesText(ws *spec.Workspace) string {
	var b strings.Builder
	b.WriteString("# Configured agent runtimes: the only valid values of `runtime:` on an agent node.\n# name  kind  lockdown (ok: the runtime is locked down to hal's tools; none: its kind is unknown, so it runs without lockdown)\n")
	for _, name := range runtimeNames(ws) {
		kind, ok := runtimeLockdown(ws.Config.Value.Runtimes[name])
		lock := "ok"
		if !ok {
			lock = "none"
		}
		if kind == "" {
			kind = "unknown"
		}
		fmt.Fprintf(&b, "%s\tkind=%s\tlockdown=%s\n", name, kind, lock)
	}
	return b.String()
}

func backendTitle(id string) string {
	return strings.ToUpper(id[:1]) + id[1:]
}

func sandboxesText(home string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# Sandboxes\n\n")
	w("An agent node runs in a sandbox when it has `sandbox: <id>`; the id is exactly one of the backends below and the backend must be enabled. ")
	w("`image:` is only valid with `docker` and `kubernetes` and must be one of that backend's images (the first is the default). ")
	w("Settings live in Configure > Sandboxes (one tab per backend); credentials live in the backend's environment and are never shown here.\n\n")
	for _, s := range configSandboxes(home) {
		state := "disabled"
		switch {
		case s.Invalid:
			state = "NOT USABLE (its configuration has errors; fix it in Configure > Sandboxes > " + backendTitle(s.ID) + ")"
		case s.Enabled:
			state = "enabled"
		}
		w("## `%s`: %s\n\n", s.ID, state)
		if !s.Enabled {
			if !s.Invalid {
				w("Not usable by flows yet. To use it, the user enables it in Configure > Sandboxes > %s. Do not write `sandbox: %s` until it is enabled.\n\n", backendTitle(s.ID), s.ID)
			}
			if len(s.Cfg.Images) > 0 && (s.ID == spec.SandboxDocker || s.ID == spec.SandboxKubernetes) {
				w("Images once enabled: %s\n\n", strings.Join(s.Cfg.Images, ", "))
			}
			continue
		}
		w("- lifecycle: `%s`\n", s.Cfg.Lifecycle)
		if s.ID == spec.SandboxDocker || s.ID == spec.SandboxKubernetes {
			w("- images (valid `image:` values, first is the default): %s\n", codeList(s.Cfg.Images))
		}
		if s.ID == spec.SandboxKubernetes {
			w("- namespace: `%s`\n", s.Cfg.Namespace)
		}
		w("- approval: %s\n", approvalSummary(s.Cfg))
		w("- default command timeout: %s\n\n", s.Cfg.Timeout.Std())
	}
	return b.String()
}

func approvalSummary(s spec.Sandbox) string {
	switch s.Approval {
	case spec.ApprovalAuto:
		return "`auto`: sandbox tool calls run without asking"
	case spec.ApprovalAlways:
		return "`always`: every sandbox tool call asks the user"
	}
	var parts []string
	for _, r := range s.Rules.Shell {
		parts = append(parts, fmt.Sprintf("shell `%s` -> %s", r.Match, r.Action))
	}
	for _, r := range s.Rules.FSWrite {
		parts = append(parts, fmt.Sprintf("fs_write `%s` -> %s", r.Path, r.Action))
	}
	out := "`rules` (first matching rule wins; no match asks the user)"
	if len(parts) > 0 {
		out += ": " + strings.Join(parts, "; ")
	}
	return out
}

func codeList(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = "`" + it + "`"
	}
	return strings.Join(q, ", ")
}

func toolsText(ws *spec.Workspace) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# Tools\n\n")
	w("An agent node lists custom tools as `tools: [{id: <tool id>}]` (optionally `approve: never|always|{when: ...}` per entry); only the ids below exist. ")
	w("Every agent also has the built-in tools and, with a `sandbox:`, the sandbox tools; those need no `tools:` entry.\n\n")
	w("## Custom tools\n\n")
	if len(ws.Tools) == 0 {
		w("None are defined. The user creates them in Tools (a tool assistant session can write one).\n\n")
	}
	for _, f := range ws.Tools {
		t := f.Value
		if t == nil {
			w("### `%s`\n\nNOT USABLE: the file has errors.\n\n", f.Name)
			continue
		}
		w("### `%s`\n\n- description: %s\n", f.Name, oneLine(t.Description))
		names := t.ParamNames()
		if len(names) == 0 {
			w("- params: none\n")
		}
		for _, n := range names {
			p := t.Params[n]
			req := "optional"
			if p.Required {
				req = "required"
			}
			w("- param `%s` (%s, %s)%s\n", n, p.Type, req, descSuffix(p.Description))
		}
		w("- approve: %s\n- runs_in_sandbox: %t\n\n", toolApproveText(t.Approve), t.RunsInSandbox)
	}
	w("## Built-in tools (every agent run)\n\n%s\n\n", codeList(tools.BuiltinToolNames()))
	w("## Sandbox tools (only on a node with `sandbox:`)\n\n%s\n", codeList(sandboxTools))
	return b.String()
}

func toolApproveText(a spec.ToolApprove) string {
	switch a.Mode {
	case "", spec.ToolApproveAlways:
		return "`always` (every call asks the user)"
	case spec.ToolApproveNever:
		return "`never`"
	}
	return "`when` (asks only when conditions on the arguments match)"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func descSuffix(s string) string {
	if s = oneLine(s); s != "" {
		return ": " + s
	}
	return ""
}

func settingsText(ws *spec.Workspace) string {
	cfg := spec.Config{}
	if ws.Config.Value != nil {
		cfg = *ws.Config.Value
	}
	cfg.ApplyDefaults()
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# Settings (config.yaml, non-secret, effective values)\n\n")
	w("The user changes these in Configure > Settings. Flows cannot change them.\n\n")
	w("| Setting | Value | Meaning |\n|---|---|---|\n")
	w("| `max_agent_runs` | %d | Agent runs that may execute at once; more wait in the queue. |\n", cfg.MaxAgentRuns)
	w("| `max_command_runs` | %d | Command (script) runs that may execute at once. |\n", cfg.MaxCommandRuns)
	w("| `approval_timeout` | %s | How long an agent node waits for the user's approval unless the node sets its own `approval_timeout`. |\n", cfg.ApprovalTimeout.Std())
	w("| `notify_min_priority` | %s | Lowest inbox priority that sends a notification. |\n", cfg.NotifyMinPriority)
	w("| `notify_via` | %s | Where notifications go: auto (the macOS app when connected, else terminal-notifier), app, terminal-notifier or none (log only). |\n", cfg.NotifyVia)
	w("| `retention_days` | %d | Days runs, events and logs are kept. |\n", cfg.RetentionDays)
	w("| `dedup_retention_days` | %d | Days dedup keys are kept. |\n", cfg.DedupRetentionDays)
	return b.String()
}

// agentNodeReference documents the node fields that reference configured ids.
const agentNodeReference = `# Agent and approval node fields

Values that name something configured (` + "`runtime`, `sandbox`, `image`, `tools`, `prompt`" + `) MUST come from the files in reference/:
runtimes.txt, sandboxes.md, tools.md and prompts/. Never guess them.

## Agent node (type: agent)

| Field | Meaning |
|---|---|
| ` + "`runtime`" + ` | Required. A name from runtimes.txt. |
| ` + "`mode`" + ` | ` + "`readonly`" + ` (default choice: reads, cannot act), ` + "`approve`" + ` (asks the user before acting) or ` + "`auto`" + `. |
| ` + "`approve`" + ` | With ` + "`mode: approve`" + `: ` + "`proposal`" + ` (the agent proposes, the user approves, then it acts) or ` + "`per_tool`" + ` (asks per tool call). |
| ` + "`prompt`" + ` / ` + "`instructions`" + ` | Exactly one: ` + "`prompt`" + ` names an existing prompt file (reference/prompts/); ` + "`instructions: |`" + ` is an inline prompt template. |
| ` + "`output`" + ` | ` + "`note`" + ` (default: the agent's note becomes an inbox item) or ` + "`none`" + `; applies to inline instructions. |
| ` + "`extra_prompt`" + `, ` + "`model`" + ` | Optional extra prompt text; optional model name the runtime understands. |
| ` + "`sandbox`" + ` | Optional. A backend id from sandboxes.md that is enabled. Adds the sandbox tools, executed there. |
| ` + "`image`" + ` | Needs ` + "`sandbox`" + ` docker or kubernetes; must be one of that backend's images in sandboxes.md. |
| ` + "`repo`" + `, ` + "`ref`" + ` | Need a ` + "`sandbox`" + `. ` + "`repo`" + ` is cloned into the sandbox; ` + "`ref`" + ` (needs ` + "`repo`" + `) is the branch, tag or commit. |
| ` + "`tools`" + ` | List of ` + "`{id: <custom tool id>}`" + ` from tools.md. |
| ` + "`timeout`" + `, ` + "`approval_timeout`" + ` | Durations (` + "`30m`" + `, ` + "`1h`" + `). ` + "`approval_timeout`" + ` defaults to the approval_timeout of settings.md. |
| ` + "`cwd`" + `, ` + "`allow`" + `, ` + "`mcp_servers`" + ` | Working directory; ` + "`allow.tools`" + ` regexes for MCP tools; extra MCP servers. Rarely needed. |

## Approval node (type: approval)

| Field | Meaning |
|---|---|
| ` + "`title`" + `, ` + "`body`" + ` | Templates shown to the approver. |
| ` + "`fields`" + ` | Input paths shown as a table. |
| ` + "`editable`" + ` | Input paths the approver may change before approving. |
| ` + "`priority`" + ` | ` + "`low`, `normal`, `high`, `urgent`" + `. |
| ` + "`timeout`" + `, ` + "`on_timeout`" + ` | How long to wait (default: approval_timeout of settings.md) and what happens then: ` + "`denied`, `approved` or `error`" + `. |

Ports: ` + "`approved`" + ` and ` + "`denied`" + `.
`

// writeConfigFiles writes the configuration reference into ref (which must exist). The files are read-only.
func (m *Manager) writeConfigFiles(ws *spec.Workspace, put func(rel string, data []byte) error) error {
	for rel, text := range map[string]string{
		"runtimes.txt":        runtimesText(ws),
		"sandboxes.md":        sandboxesText(m.home),
		"tools.md":            toolsText(ws),
		"settings.md":         settingsText(ws),
		"docs/agent-nodes.md": agentNodeReference,
	} {
		if err := put(rel, []byte(text)); err != nil {
			return err
		}
	}
	return nil
}

// writeConfigReference rebuilds <dir>/reference with only the configuration files, for sensor and tool sessions.
func (m *Manager) writeConfigReference(dir string) error {
	ref := filepath.Join(dir, ReferenceDir)
	if err := os.RemoveAll(ref); err != nil {
		return fmt.Errorf("assist: reset reference: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(ref, "docs"), 0o700); err != nil {
		return fmt.Errorf("assist: create reference: %w", err)
	}
	return m.writeConfigFiles(spec.Load(m.home), func(rel string, data []byte) error {
		if err := os.WriteFile(filepath.Join(ref, rel), data, 0o400); err != nil {
			return fmt.Errorf("assist: write reference %s: %w", rel, err)
		}
		return nil
	})
}

// configSummary is the per-turn preamble line block: what is configured, so the agent sees it without reading.
func (m *Manager) configSummary() string {
	ws := spec.Load(m.home)
	var enabled, off []string
	var images []string
	for _, s := range configSandboxes(m.home) {
		if s.Enabled {
			enabled = append(enabled, s.ID)
			if len(s.Cfg.Images) > 0 && (s.ID == spec.SandboxDocker || s.ID == spec.SandboxKubernetes) {
				images = append(images, s.ID+": "+strings.Join(s.Cfg.Images, ", "))
			}
		} else {
			off = append(off, s.ID)
		}
	}
	var toolIDs []string
	for _, f := range ws.Tools {
		toolIDs = append(toolIDs, f.Name)
	}
	var b strings.Builder
	b.WriteString("Configured here (use only these ids, never guess; details in `" + ReferenceDir + "/sandboxes.md`, `runtimes.txt`, `tools.md`, `settings.md`): ")
	b.WriteString("enabled sandboxes: " + nameList(enabled))
	if len(images) > 0 {
		b.WriteString(" (images " + strings.Join(images, "; ") + ")")
	}
	b.WriteString("; disabled sandboxes (the user enables them in Configure > Sandboxes): " + nameList(off))
	b.WriteString("; runtimes: " + nameList(runtimeNames(ws)) + "; custom tools: " + nameList(toolIDs) + ".\n")
	return b.String()
}

// configGuide is the instructions section about the configuration, shared by every session kind.
func configGuide() string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("## What is configured (read this before you name a sandbox, runtime, tool or prompt)\n\n")
	w("The app's configuration is in `%s/` (read it with draft_read, rebuilt every turn, no secrets):\n\n", ReferenceDir)
	w("- `%s/runtimes.txt`: the agent runtimes (`runtime:`), with kind and whether they are locked down.\n", ReferenceDir)
	w("- `%s/sandboxes.md`: every sandbox backend (`local`, `docker`, `depot`, `kubernetes`, `ssh`), whether it is enabled, its lifecycle, its images (the valid `image:` values) and its approval policy.\n", ReferenceDir)
	w("- `%s/tools.md`: the custom tools (id, description, params, approval, runs_in_sandbox) and the built-in tool names.\n", ReferenceDir)
	w("- `%s/settings.md`: non-secret settings (agent and command concurrency, approval timeout, notification priority, retention).\n", ReferenceDir)
	w("- `%s/docs/agent-nodes.md`: the fields of agent and approval nodes.\n\n", ReferenceDir)
	w("Rules:\n\n")
	w("- Use ONLY sandbox, image, runtime, tool and prompt ids that appear in those files. NEVER guess or invent one; a guessed name fails validation and at run time.\n")
	w("- If the user asks for something that is not configured (for example a sandbox that is disabled, an image that is not listed, a runtime or tool that does not exist), say so plainly, do not write it into the draft, and tell them where to set it up: sandboxes and images in Configure > Sandboxes > <backend> (for example Configure > Sandboxes > Kubernetes), runtimes in Configure > Runtimes, custom tools in Tools. Then offer what works now.\n")
	w("- Validation diagnostics for an unknown or disabled sandbox, an image not in the list, an unknown tool or an unknown runtime list the valid options: use them to correct the draft.\n\n")
	return b.String()
}
