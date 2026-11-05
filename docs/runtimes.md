# ACP runtimes

Agent nodes drive coding agents over [ACP](https://agentclientprotocol.com) (JSON-RPC over stdio). A **runtime** is a
named, configured command that speaks ACP; an agent node selects one with `runtime: <name>`. Adding another ACP agent
needs only a `runtimes` entry. hal9001 is the ACP client: it advertises no `fs` or `terminal` capability, so agents use
their own tools, and every permission request comes to hal9001 to be decided by policy or by you.

What was measured against the real adapters (permission behavior, settings bypass, resume, model selection) is in
[acp-gate.md](acp-gate.md); this page is how to configure and check them.

## The `runtimes` block

The shipped `config.yaml` (seeded from `content/config.yaml`):

```yaml
runtimes:
  claude:
    command: [claude-agent-acp]
    modes: {ask: default, full: bypassPermissions}
    session_meta:
      claudeCode: {options: {settingSources: [project]}}
  codex:
    command: [codex-acp]
    modes: {ask: read-only, full: agent-full-access}
    env:
      CODEX_HOME: "{{data}}/runtimes/codex-home"
  omp:
    command: [omp, acp]
    modes: {ask: "", full: ""}
    ask: {args: [--approval-mode, always-ask]}
    full: {args: [--approval-mode, yolo]}
    env:
      PI_CODING_AGENT_DIR: "{{data}}/runtimes/omp-home"
    elicitation: true
```

| Field | Meaning |
|---|---|
| `command` | Required argv of the ACP agent. `command[0]` is resolved on the daemon's `PATH`. |
| `modes.ask` | The agent's own session-mode id for the restrictive policy: used by `readonly` nodes, the proposal phase and `per_tool` approvals. It must be a mode that asks before changing things. |
| `modes.full` | Session-mode id for the permissive policy: `auto` nodes and the approved phase of a proposal. |
| `ask`, `full` | `{args: [...], env: {...}}` appended to the spawn for that policy. For agents that pick their approval behavior with a flag instead of a session mode (omp). |
| `env` | Environment variables added to the agent process (all policies). |
| `session_meta` | JSON object sent as `_meta` on `session/new` and `session/load` (runtime-specific session options). |
| `elicitation` | Advertise `elicitation.form` to the agent. omp needs it to route edit and MCP approvals to hal9001; without it omp refuses those calls itself and hal9001 never sees them. |

An empty mode name (`""`) means "do not set a session mode"; the policy is enforced another way (for omp, the process
flag in `ask` / `full`).

### `{{data}}` and isolation homes

`{{data}}` in `command`, `env` values and `ask`/`full` `args` and `env` is replaced with `<home>/data`. Write it quoted
in YAML.

Why isolation matters: user settings that auto-allow tools bypass the approval policy, because the agent never asks the
client. The shipped config isolates what the gate found to bypass (see [acp-gate.md](acp-gate.md), check 1b):

- **Claude:** `settingSources: [project]` in `session_meta` drops user and local settings (including `permissions.allow`)
  and keeps the project's `CLAUDE.md`. (`CLAUDE_CONFIG_DIR` does not work: the login is lost.)
- **Codex:** `CODEX_HOME` points to a stable directory under `data/runtimes/`. The user's exec rules in
  `~/.codex/rules` would otherwise allow commands silently. The directory must stay the same between a proposal and its
  resume, because Codex keeps sessions there.
- **omp:** `PI_CODING_AGENT_DIR` likewise, because `tools.approval.<tool>: allow` in the user config beats
  `--approval-mode always-ask`. Project config (`<cwd>/.omp/config.yml`) can still carry allow rules and is not isolated.

When a runtime's environment sets `CODEX_HOME` or `PI_CODING_AGENT_DIR`, the daemon creates that directory (mode 0700)
and symlinks the existing login into it: `auth.json` -> `~/.codex/auth.json` for Codex, `agent.db` ->
`~/.omp/agent/agent.db` for omp. A symlink keeps token refreshes in the real credential store. A missing credential file
is skipped and the agent reports its own login error. These two variable names are the only ones with built-in
handling; for any other isolation, create the directory yourself.

### Installing the adapters and finding them

`claude-agent-acp` (0.100.0) and `codex-acp` (2.5.0) are pinned in `mise.toml`, so `mise install` installs them. Outside
the repository a bare `claude-agent-acp` shim fails with `No version is set for shim`, and agents run in their own cwd, so
the seeded `config.yaml` runs the adapters through `mise exec` with explicit versions:

```yaml
claude:
  command: [mise, exec, "npm:@agentclientprotocol/claude-agent-acp@0.100.0", --, claude-agent-acp]
```

This needs only `mise` on the daemon's PATH (`hal9001 install` captures the login-shell PATH). When you bump an adapter in
`mise.toml`, bump the version in `config.yaml` too. Check that each command resolves for the daemon with
`bin/hal9001 runtimes probe <name>`: it returns `ok: false` with the error and the tail of the agent's stderr when the
command cannot start. An absolute path also works:

```sh
mise which claude-agent-acp     # run inside the repo
# ~/.local/share/mise/installs/npm-agentclientprotocol-claude-agent-acp/0.100.0/node_modules/.bin/claude-agent-acp
```

```yaml
claude:
  command: [/Users/you/.local/share/mise/installs/npm-agentclientprotocol-claude-agent-acp/0.100.0/node_modules/.bin/claude-agent-acp]
```

omp is not in `mise.toml` (Homebrew install, `omp acp` is built in). The adapters are installed rather than run through
`npx`, so startup is fast and versions do not drift.

Environment from your `env` file is part of the agent process environment, as for nodes.

### Models

Agent nodes can set `model:`. hal9001 sets it through the agent's `model` config option after the session starts (and
again after `session/load` for omp, whose load result omits config options). The value format is runtime specific: Claude
takes an alias or id (`haiku`, `sonnet`, `claude-haiku-4-5-20251001`), Codex takes a model id (reasoning effort is a
separate option and is not set by the node), omp takes `provider/model` (`anthropic/claude-haiku-4-5`). A rejected model
fails the run with `model_rejected`.

## Per-runtime caveats

Summarized from [acp-gate.md](acp-gate.md) (tested 2026-11-05):

| | Claude | Codex | omp |
|---|---|---|---|
| Adapter | `claude-agent-acp` 0.100.0 | `codex-acp` 2.5.0 | `omp acp`, omp 18.16.0 |
| Gate | Pass | Pass with caveats | Pass with caveats |
| Restrictive mode | session mode `default` | session mode `read-only` | not a session mode: `--approval-mode always-ask` |
| Permissive mode | `bypassPermissions` | `agent-full-access` (not run end to end) | `--approval-mode yolo` |
| Resume (`session/load`) | yes | yes, only with the same `CODEX_HOME` | yes; set the model again after load |
| What reaches hal9001 | Edit, execute and MCP requests. Claude auto-approves read-only tools and read-only-safe Bash itself. | Escalated shell, edits, MCP. Commands that run inside Codex's read-only sandbox never prompt, so `readonly` is enforced by the sandbox there, not by rejection. | `bash` through `session/request_permission`; edits and MCP through `elicitation/create` (needs `elicitation: true`). |
| A denied call | The agent continues. | **Ends the turn** (the only reject option is `cancel`), so "deny and continue" is not possible; a policy reject ends the run as `policy_rejected`. | The agent continues. |
| Project instructions | `CLAUDE.md` (not `AGENTS.md`) | `AGENTS.md` (not `CLAUDE.md`) | `AGENTS.md` |

Other points:

- hal9001 never selects an `allow_always` / "don't ask again" option (Codex `accept_execpolicy_amendment` persists a
  rule): it always chooses the one-time allow.
- `allow.commands` needs the command from `rawInput.command` of `execute`-kind calls; all three runtimes provide it.
  The Codex MCP and edit requests carry no input, so hal9001 merges the preceding `tool_call` updates into the request.
- The default omp mode (`yolo`) runs edits silently; never rely on it. The shipped `ask` args avoid it.
- If a configured mode name is not offered by the agent, a run fails with `mode_unavailable`; this is what a probe's
  `ask_mode_available` / `full_mode_available` predict.
- Runtime `env` values are redacted from `hal9001 settings get` and the API.
- Not verified in the gate: the proposal flow end to end on real agents beyond a `session/load` context check, HTTP/SSE
  MCP servers, permission timeouts and daemon-restart behavior on real runtimes ([acp-gate.md](acp-gate.md#not-verified)).

## Probing

List the configured runtimes and the last probe result of each (since the daemon started):

```sh
bin/hal9001 runtimes list --table
```

Probe one: the daemon spawns the command with the restrictive (`ask`) spawn settings, runs `initialize` and a throwaway
`session/new`, reports what the agent advertised, and stops it (60 second limit). A runtime that fails to start is a
normal result with `ok: false`, an `error`, and the tail of its stderr, not a CLI error:

```sh
bin/hal9001 runtimes probe claude
```

The result includes `ok`, `agent_info` (name and version), `capabilities`, `load_session`, `available_modes`,
`available_models`, `ask_mode_available` / `full_mode_available` (the configured `modes.ask` / `modes.full` are among the
available modes) and `duration_ms`. Check `load_session: true` (needed to resume an approved proposal in the same
session; without it hal9001 falls back to a new session, recorded as `resumed_via: new_session`) and that
`ask_mode_available` is true for runtimes that use session modes. Probing needs the `admin` service because it runs the
configured command.

### `cmd/acp-probe`

`acp-probe` is a standalone diagnostic that runs the checks of the ACP gate by hand: permission requests and what the
client answered, `loadSession` across processes, model selection, what a runtime loads from the project and your
settings. Use it when adding a runtime or when an adapter version changes.

```sh
mise exec -- go build -o /tmp/acp-probe ./cmd/acp-probe

# omp, always-ask, edit/MCP through elicitation, MCP echo tool, small model
/tmp/acp-probe --elicitation --mcp-echo --policy log-and-reject --model anthropic/claude-haiku-4-5 \
  --env PI_CODING_AGENT_DIR=/tmp/omp-home -- omp acp --approval-mode always-ask

# claude, with user settings isolated
/tmp/acp-probe --mcp-echo --mode default --model haiku --policy log-and-reject \
  --session-meta '{"claudeCode":{"options":{"settingSources":["project"]}}}' -- claude-agent-acp

# codex
/tmp/acp-probe --mcp-echo --mode read-only --config model=gpt-6-luna --config reasoning_effort=low \
  --env CODEX_HOME=/tmp/codex-home -- codex-acp

# does context survive a restart (session/load)?
/tmp/acp-probe --resume-check -- <agent command>
```

Run `acp-probe -h` for every flag. The main ones: `--policy allow-all|reject-all|log-and-reject|cancel`, `--mode`,
`--model`, `--config id=value`, `--prompt` / `--prompt-file`, `--no-prompt`, `--mcp-echo`, `--canary` (writes `AGENTS.md`
and `CLAUDE.md` with canary tokens to see which the agent loads), `--cwd`, `--env K=V`, `--session-meta JSON`,
`--elicitation`, `--load ID`, `--resume-check`, `--log FILE`, `--timeout`. The summary goes to stdout; every JSON-RPC
line in both directions goes to a JSONL wire log. The probe advertises no `fs` or `terminal` capability and does real
work with your real login, so run it against a scratch directory (it defaults to a fresh temp dir).

### Tests without real models

`cmd/fake-acp-agent` is a scripted ACP agent used by the tests and by test setups of `runtimes`: point a runtime
`command` at it to exercise modes, approvals, resume, timeouts and cancellation without a model. See
[its README](../cmd/fake-acp-agent/README.md).
