# ACP adapter gate (REQUIREMENTS §13)

Results of running the §13 gate against the three real runtimes with `cmd/acp-probe`. Every result below comes from a
real run on 2026-11-05 (macOS arm64, existing logins, cheapest selectable models). "Evidence" excerpts are compact
JSON copied from the probe's wire log. Anything not run is listed under [Not verified](#not-verified).

## Verdict

| Runtime | Adapter (tested version) | Gate | CLI-exec fallback needed? |
|---|---|---|---|
| Claude | `@agentclientprotocol/claude-agent-acp` **0.100.0** (`claude-agent-acp`) | **Pass** | No |
| Codex | `@agentclientprotocol/codex-acp` **2.5.0** (`codex-acp`) | **Pass with caveats** (a deny ends the turn; in-sandbox commands never prompt) | No |
| omp | `omp acp`, omp **18.16.0** | **Pass with caveats** (approval mode is a process flag; edit/MCP approvals use `elicitation/create`) | No |

No runtime fails the gate, so no CLI-exec adapter is needed. Each runtime needs configuration beyond a mode name; see
[Recommended `runtimes:` block](#recommended-runtimes-block).

`@zed-industries/codex-acp` (0.16.0) is deprecated on npm ("replaced by `@agentclientprotocol/codex-acp`"); the old
name `@zed-industries/claude-code-acp` (0.16.2) is likewise superseded by `@agentclientprotocol/claude-agent-acp`.
Only the new names were tested.

## Check results

| # | Check | Claude | Codex | omp |
|---|---|---|---|---|
| 1 | Restrictive "ask" mode and tool calls reaching the client | **Pass.** Mode `default` ("Manual"). Edit, execute and MCP all reach `session/request_permission`. Claude auto-approves read-only tools and read-only-safe Bash (`echo` ran with no request). | **Pass.** Mode `read-only`. Escalated shell, file edit and MCP all reach `session/request_permission`. Commands that run inside Codex's read-only sandbox (even `curl`, network blocked) run with no request. | **Pass with caveat.** Not a session mode: `omp acp --approval-mode always-ask`. Only `bash` reaches `session/request_permission`; edit and MCP reach `elicitation/create` if the client advertises `elicitation.form`, and are refused by omp itself (no client call) if it does not. |
| 1b | User settings bypass the client | **Yes**: `permissions.allow` from settings (tested via `.claude/settings.local.json` and flag `settings`; 0 requests). Isolated by `_meta.claudeCode.options.settingSources` on session/new. `CLAUDE_CONFIG_DIR` does not work (login is lost). | **Partly.** `approval_policy`/`sandbox_mode` in `config.toml` do **not** bypass (session mode wins). Exec rules (`$CODEX_HOME/rules/*.rules`, decision `allow`) **do**. Isolated by `CODEX_HOME`. | **Yes**: `tools.approval.<tool>: allow` in user config beats `--approval-mode always-ask`. Isolated by `PI_CODING_AGENT_DIR`. |
| 2 | `loadSession` and resume | **Pass.** `loadSession: true`; new process + `session/load` recalled a secret from the first process. | **Pass**, but only with the same `CODEX_HOME` in both processes (sessions live under it). | **Pass.** `loadSession: true`; context recalled. `session/load` result omits `configOptions`, so set the model again after load. |
| 3 | Model selection | **Pass.** `session/set_config_option` `configId: "model"`, value is an alias or id (`haiku`, `sonnet`, `opus`, `claude-haiku-4-5-20251001`). Effort is `configId: "effort"`. | **Pass.** `configId: "model"` (`gpt-6-luna`) plus separate `configId: "reasoning_effort"` (`low`..`ultra`). | **Pass.** `configId: "model"`, value `provider/model` (`anthropic/claude-haiku-4-5`). Thinking is `configId: "thinking"`. |
| 4 | What loads vs the normal CLI | User settings, user skills, user plugins (incl. plugin MCP server and agents), project `CLAUDE.md` (not `AGENTS.md`). With `settingSources: ["project"]`: project `CLAUDE.md` only. | User `config.toml`, skills, plugins, project `AGENTS.md` (not `CLAUDE.md`). With isolated `CODEX_HOME`: project `AGENTS.md` only. | User config, user skills, user MCP servers (`/mcp list` showed linear, notion `[user]`), project `AGENTS.md`. With isolated `PI_CODING_AGENT_DIR`: user config/MCP/skills gone. |
| 5 | Execute `kind` and `rawInput` | `kind: "execute"`, `rawInput: {"command","description"}` | `kind: "execute"`, `rawInput: {"command","cwd"}` | `kind: "execute"`, `rawInput: {"command"}` |
| 6 | Full-access mode | `bypassPermissions` (0 requests) | `agent-full-access` | `--approval-mode yolo` (0 requests; session mode stays `default`) |

## Claude (`claude-agent-acp` 0.100.0)

Handshake: `agentInfo.version 0.100.0`, `agentCapabilities.loadSession: true`, `mcpCapabilities {http, sse}`,
`sessionCapabilities {list, resume, fork, close, delete, additionalDirectories}`, `authMethods: []` (uses the existing
Claude login). Startup: initialize 0.16 s, `session/new` 2.7 s.

Session modes from `session/new`: `default` ("Manual", "Always ask before making changes"), `acceptEdits`, `plan`,
`auto`, `bypassPermissions`.

**(1) Permission requests in `default` mode** (probe `--mode default --policy log-and-reject`; all four rejected):

```json
{"toolCall":{"toolCallId":"toolu_01KqEuku…","name":"Bash","status":"pending","rawInput":{"command":"touch made-by-bash.txt","description":"Create empty file made-by-bash.txt in working directory"},"title":"touch made-by-bash.txt","kind":"execute","content":[…],"locations":[…]},"options":[{"optionId":"allow-once","name":"Yes","kind":"allow_once"},{"optionId":"reject","name":"No","kind":"reject_once"}],"sessionId":"…"}
{"toolCall":{"name":"Write","rawInput":{"file_path":"/private/var/…/work/note.txt","content":"probe\n"},"title":"Write /private/var/…/note.txt","kind":"edit","content":[{"type":"diff","path":"…","oldText":null,"newText":"probe\n"}],…},"options":[{"optionId":"allow-once",…},{"optionId":"allow-with-updates","kind":"allow_always"},{"optionId":"reject","kind":"reject_once"}]}
{"toolCall":{"name":"mcp__probe_echo__echo","rawInput":{"text":"mcp-ok"},"title":"mcp__probe_echo__echo","kind":"other","_meta":{"claudeCode":{"toolName":"mcp__probe_echo__echo","mcpServer":{"name":"probe_echo","source":"dynamic"}}}},"options":[…]}
```

- The initial `tool_call` update has `rawInput: {}` and a generic title (`Terminal`, `Read File`); the real `rawInput` is
  in the permission request and in later `tool_call_update`s. Use the permission request for `allow.commands`.
- Rejecting (`reject`) fails that tool call and the agent continues (stopReason `end_turn`).
- Read-only tools and read-only-safe Bash (`echo probe-ok`) ran with no request. This is Claude's own classification;
  it only matters for `allow.commands`, which is never consulted for those commands.

**(1b) Settings bypass and isolation.** The probe was run with the same prompt (Bash `touch`, `Write`, MCP echo) and
`--policy log-and-reject`. "Requests" is the number of `session/request_permission` calls the client saw.

| Run | Setting | Requests | Result |
|---|---|---|---|
| A | none | 3 | all three rejected |
| B2 | `_meta.claudeCode.options.settings` = `{"permissions":{"allow":["Bash(touch:*)","Write","mcp__probe_echo__echo"]}}` | 0 | all three executed; bypass |
| B3 | `.claude/settings.local.json` with the same allow list | 0 | bypass |
| B / B again | `.claude/settings.json` (project) with the same allow list | 3 | no bypass (reproduced twice; not investigated further) |
| C2 | B3 plus `settingSources: []` | 3 | isolated |
| C3 | B3 plus `settingSources: ["project"]` | 3 | isolated; project `CLAUDE.md` still loads |
| D | `CLAUDE_CONFIG_DIR=<empty dir>` | n/a | `{"code":-32000,"message":"Authentication required"}`; not usable with the existing login |

This machine's `~/.claude/settings.json` has no `permissions` block, so the user-level path itself was not exercised;
`settingSources` is the adapter option that selects the user, project and local sources (adapter default
`["user","project","local"]`, overridable by `_meta.claudeCode.options`), and run C2/C3 show it removes allow rules
from a source it excludes. The `_meta` must be sent on `session/new`, `session/load` and `session/resume`.

**(2) Resume.** `--resume-check`: prompt "remember secret word kumquat-…", agent process stopped, new process,
`session/load` (2 replay updates), "what was the secret word?" → reply contained the word. `RESUME-CHECK RESULT: PASS`.

**(3) Model.** `session/set_config_option {configId:"model", value:"haiku"}` returned ok; `usage_update._meta` then
reported `"_claude/model":"claude-haiku-5-5"`. `session/new` returns no `models` field; the list is in `configOptions`
(category `model`).

**(4) Loading.** Canary prompt with `CLAUDE.md` and `AGENTS.md` in the cwd (answers are the agent's own report):

| `settingSources` | Canary seen | MCP servers | Skills |
|---|---|---|---|
| default (`user,project,local`) | `CANARY-CLAUDE-MD-7421` only | `plugin:devin:devin` (user plugin) | user skills `cmux`, `glean-cli`, `devin:*`, plus managed `inclusive:*` |
| `[]` | none | none | only managed/built-in (`inclusive:*`, `dataviz`, `update-config`, …) |
| `["project"]` | `CANARY-CLAUDE-MD-7421` | none | managed/built-in only |

Managed plugins (`inclusive:*`) still appear with `[]`, so managed policy settings appear to apply regardless of `settingSources`.

**(5)** See (1). Bash: `kind: "execute"`, `rawInput.command` is the string, `title` is the command.

**(6)** `session/set_mode bypassPermissions`: 0 permission requests, Bash, Write and MCP all executed.

**Elicitation.** The adapter enables `AskUserQuestion` and routes MCP elicitation to `elicitation/create` only when the
client advertises `elicitation.form` (source: `dist/acp-agent.js`). Do not advertise it for Claude in v1.

## Codex (`codex-acp` 2.5.0)

Handshake: `agentInfo.version 2.5.0`, `loadSession: true`, `sessionCapabilities {resume, list, close, delete, fork,
additionalDirectories, subagents, systemPrompt}`, `mcpCapabilities {http: true, sse: false}`, `authMethods`
`api-key` and `chat-gpt` (the existing ChatGPT login in `~/.codex/auth.json` was used). Startup: initialize 6.4 s
(cold), `session/new` 2.0 s. The package bundles `@openai/codex ^0.165`; the user's own `codex` CLI is 0.162.1 and
was not used (`CODEX_PATH` would select it).

Session modes: `read-only` ("Requires approval to edit files and access the internet"), `workspace-write`,
`agent` ("Auto review", the default), `agent-full-access`.

**(1) Permission requests in `read-only`** (probe `--mode read-only`, model `gpt-6-luna`, effort `low`):

```json
{"toolCall":{"toolCallId":"exec-9a544f66…","title":"Run command","rawInput":{"command":"touch made-by-bash.txt","cwd":"/var/…/work"},"kind":"execute","status":"pending"},"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"accept_execpolicy_amendment","name":"Yes, and don't ask again for commands that start with `touch made-by-bash.txt`","kind":"allow_always"},{"optionId":"cancel","name":"No, and tell Codex what to do differently","kind":"reject_once"}]}
{"toolCall":{"toolCallId":"exec-4a1aab39…","title":"Edit files","locations":[{"path":"/var/…/work/note.txt"}],"kind":"edit","status":"pending"},"options":[{"optionId":"allow_once"},{"optionId":"allow_for_session","kind":"allow_always"},{"optionId":"cancel","kind":"reject_once"}]}
{"toolCall":{"toolCallId":"exec-1172ef1a…","kind":"execute","status":"pending"},"_meta":{"is_mcp_tool_approval":true},"options":[{"optionId":"allow_once"},{"optionId":"allow_session","kind":"allow_always"},{"optionId":"allow_always","kind":"allow_always"},{"optionId":"cancel","kind":"reject_once"}]}
```

- The MCP request has no `title` or `rawInput`; the details are in the preceding `tool_call` update:
  `{"kind":"execute","title":"mcp.probe_echo.echo","rawInput":{"server":"probe_echo","tool":"echo","arguments":{"text":"mcp-ok"}}}`.
  The edit request has no `rawInput` either (path in `locations`). The harness must keep the last `tool_call` /
  `tool_call_update` per `toolCallId` and merge it into the request.
- **A deny ends the turn.** The only reject option is `cancel`. Selecting it returned `stopReason: "cancelled"`
  immediately (run `codex-ro2`). "Deny and let the agent continue" is not possible for Codex.
- `accept_execpolicy_amendment` persists a rule for future runs. The harness must never select it (nor `allow_always`
  kinds generally).
- Commands that run inside the read-only sandbox are never offered to the client. Prompt
  `ls -la | wc -l && curl -sI https://example.com | head -1` ran with 0 requests and printed only `4` (the sandbox
  blocked the network silently). For Codex, `readonly` is enforced by the sandbox, not by client rejection.
- `rawInput.command` is a plain string, compound commands are not split.

**(1b) User settings.** In a temporary `CODEX_HOME` containing a symlink `auth.json -> ~/.codex/auth.json`:

| Run | `CODEX_HOME` contents | Result |
|---|---|---|
| e1 | `rules/default.rules`: `prefix_rule(pattern=["touch"], decision="allow")` | `touch` ran with no request (bypass); the next call (edit) prompted and the reject ended the turn, so MCP was not attempted |
| e2 | `config.toml`: `approval_policy = "never"`, `sandbox_mode = "danger-full-access"` | still prompted for `touch`; ACP session mode wins |
| e3 | only `auth.json` | prompted as normal |

The symlinked `auth.json` authenticated successfully. The adapter and its app-server both inherit `CODEX_HOME`.

**(2) Resume.** PASS with `CODEX_HOME=<e3>` in both processes (6 replay updates; secret recalled). Sessions are stored
under `$CODEX_HOME/sessions`, so an isolated home must be stable across the proposal and resume processes.

**(3) Model.** `configOptions` `model` (`gpt-6.1-sol`, `gpt-6-astra`, `gpt-6-luna`, …) and `reasoning_effort`; both set
ok via `session/set_config_option`. `session/new` also returns an unstable `models` list with combined ids such as
`gpt-6-luna[low]`.

**(4) Loading.** `AGENTS.md` from the cwd loaded in both the user and the isolated home (agent quoted
`CANARY-AGENTS-MD-7421`); `CLAUDE.md` did not. The user home also supplies skills (`imagegen`, `openai-docs`,
`anthropic-skills:*`, …) and `config.toml` plugins/MCP servers (`node_repl`, `computer-use`); the agent reported no MCP
servers in its context. The isolated home loads none of the user skills.

**(5)** See (1): `{"command": "<string>", "cwd": "<abs path>"}`.

**(6)** `agent-full-access`. Not run end to end (no prompt was sent in this mode); the mode id and description come
from `session/new`. `agent` ("Auto review") was also not exercised.

The `AppServerUnavailableError ... shutting down` lines the probe prints at exit are the adapter reacting to the
probe closing stdin while it was still publishing slash commands; they are not a failure.

## omp (`omp acp`, 18.16.0)

Handshake: `agentInfo.version 18.16.0`, `loadSession: true`, `sessionCapabilities {list, fork, resume, close}`,
`mcpCapabilities {http, sse}`, `authMethods [{id:"agent"}]` (existing `~/.omp` credentials), `_meta.steering.supported`.
Startup: initialize 0.1 s, `session/new` 0.1 s.

Session modes from `session/new` are only `default` and `plan`. `plan` is a planning workflow (it wrote a plan file
under `local://` and asked clarifying questions through tools); it is **not** a permission mode. Approval is a
process-level setting (`tools.approvalMode`, documented in `omp://approval-mode.md`): `always-ask`,
`always-ask-cached`, `write`, `yolo` (default). `omp acp --approval-mode <mode>` overrides config for the process, and
ACP has no per-session approval field. So the harness selects the policy by spawn arguments, not by `session/set_mode`.

**(1) Permission requests with `omp acp --approval-mode always-ask`** (model `anthropic/claude-haiku-4-5`):

Execute (via `session/request_permission`):

```json
{"toolCall":{"toolCallId":"toolu_01Dind…","title":"echo probe-ok","kind":"execute","status":"pending","rawInput":{"command":"echo probe-ok"},"content":[{"type":"content","content":{"type":"text","text":"$ echo probe-ok"}}],"locations":[]},"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"allow_always","kind":"allow_always"},{"optionId":"reject_once","kind":"reject_once"},{"optionId":"reject_always","kind":"reject_always"}]}
```

Edit and MCP:

- Client does **not** advertise `elicitation.form`: omp refuses the call itself (`"Tool call denied by user: write"`,
  `"... denied by user: eval"`) and the client sees no request at all. The file was not written.
- Client advertises `clientCapabilities.elicitation.form`: omp sends `elicitation/create` instead:

```json
{"mode":"form","sessionId":"…","message":"Allow tool: write\nPath: note.txt\nContent:\nprobe","requestedSchema":{"type":"object","properties":{"value":{"type":"string","enum":["Approve","Approve write for this session","Deny"]}},"required":["value"]}}
```

  The MCP call arrives the same way (`"Allow tool: write\nPath: xd://mcp__probe_echo_echo\nArguments:\n{\n  \"text\": \"mcp-ok\"\n}"`).
  The request has no `toolCallId`; correlate with the preceding `tool_call` update. Answering
  `{"action":"accept","content":{"value":"Approve"}}` ran the tool (file written, MCP echo executed, run
  `omp-ask-allow`); `{"action":"decline"}` refused it and the agent continued.
- Tool shapes: read `kind: "read"` `{"path":"hello.txt"}` (no prompt); edit `kind: "edit"`
  `{"path":"note.txt","content":"probe"}`; MCP tools are invoked through `xd://` writes, `kind: "execute"`,
  `{"path":"xd://mcp__probe_echo_echo","content":"{\"text\": \"mcp-ok\"}"}`, or through `eval`
  (`{"language":"py","code":"…"}`); question tools use `kind: "other"`.
- Default config (`yolo`, no flag): `bash` still reached `session/request_permission` but edits ran silently
  (note.txt written). Do not rely on the default.

**(1b) User settings.** `tools.approval.<tool>: allow` is a user policy and beats `always-ask`:

| Run | Setup | Requests / elicitations | Result |
|---|---|---|---|
| overlay | `--approval-mode always-ask --config overlay.yml` with `tools.approval.{bash,write,edit}: allow` | 0 | bash and edit executed; bypass |
| d1 | `PI_CODING_AGENT_DIR=<dir>` with `config.yml` = that overlay and a symlink `agent.db -> ~/.omp/agent/agent.db` | 0 | bypass (proves the dir is the user-config source and auth works) |
| d2 | `PI_CODING_AGENT_DIR=<dir>` with only the `agent.db` symlink | 1 + 1 | prompts return; edit and MCP declined |

`PI_CODING_AGENT_DIR` only works for the default profile. Project config (`<cwd>/.omp/config.yml`) can still carry
`tools.approval` allow rules and is not overridden by the flag.

**(2) Resume.** PASS (3 replay updates, secret recalled). The `session/load` result has no `configOptions`, so the
harness sets model again afterwards (probe does this automatically).

**(3) Model.** `session/set_config_option {configId:"model", value:"anthropic/claude-haiku-4-5"}` → ok (followed by a
large `config_option_update` of about 140 KB, so the raw-traffic blob grows quickly). No `models` field.

**(4) Loading.** `AGENTS.md` loaded (canary `CANARY-AGENTS-MD-7421`; `CLAUDE.md` was not reported, omp keeps one per
directory). User skills (`bootstrap-go-web-hooks`, `cmux`, `find-skills`, `glean-cli`) loaded. `/mcp list` in the ACP
session printed `linear | http | enabled … [user]` and `notion | http | enabled … [user]`; the agent reported no
linear/notion tools in a fresh session (OAuth servers connect lazily), so those servers are configured but were not
shown to be callable. ACP `mcpServers` passed in `session/new` worked (the probe's echo server executed).

**(5)** `kind: "execute"`, `rawInput: {"command": "<string>"}` for `bash`.

**(6)** `--approval-mode yolo`: 0 permission requests and 0 elicitations; read, write, bash and MCP all executed.

## Recommended `runtimes:` block

`config.yaml` shape follows the Agents slice's `RuntimeConfig` (`command`, `modes`, `env`, `ask`/`full` extras).
Fields marked **new** are not in REQUIREMENTS §7.3; the gate needs them. They are implemented in `spec.Runtime` (`ask`/`full` as `{args, env}`, `session_meta`, `elicitation`, `env`), and the engine resolves `{{data}}` to `<home>/data` and creates the isolated homes with the credential symlinks (`internal/engine`); `content/config.yaml` ships this block. The model option is found by `configOptions` category `model` (values: Claude alias or id, Codex model id with a separate `reasoning_effort` option, omp `provider/model`), so no model field is needed. `session/resume` is not used.

```yaml
runtimes:
  claude:
    command: [claude-agent-acp]
    modes: {ask: default, full: bypassPermissions}
    session_meta:                       # new: _meta for session/new, session/load, session/resume
      claudeCode: {options: {settingSources: [project]}}
    elicitation: false                  # new: do not advertise elicitation.form
  codex:
    command: [codex-acp]
    modes: {ask: read-only, full: agent-full-access}
    env:
      CODEX_HOME: "{{data}}/runtimes/codex-home"   # new: stable dir; contains symlink auth.json -> ~/.codex/auth.json
    elicitation: false
  omp:
    command: [omp, acp]
    modes: {ask: "", full: ""}          # session modes default|plan are not permission modes; do not set
    ask:  {args: [--approval-mode, always-ask]}
    full: {args: [--approval-mode, yolo]}
    env:
      PI_CODING_AGENT_DIR: "{{data}}/runtimes/omp-home"  # new: dir containing symlink agent.db -> ~/.omp/agent/agent.db
    elicitation: true                   # new: needed so edit/MCP approvals reach the harness
```

Harness behavior the gate requires:

- Create the isolated homes (`CODEX_HOME`, `PI_CODING_AGENT_DIR`) once, with a symlink to the credential file; keep them
  stable so `session/load` works across processes. A symlink keeps token refreshes in the real credential store.
- Merge every `tool_call` / `tool_call_update` by `toolCallId` and use the merge when applying `allow.tools` and
  `allow.commands` (Codex MCP/edit requests carry no input; Claude's first `tool_call` has empty `rawInput`).
- Extract the command from `rawInput.command` for `kind == "execute"` on all three runtimes.
- Handle `elicitation/create` for omp (map `Approve`/`Deny` onto the approval decision; use the `Allow tool: <name>`
  line of `message` for `allow.tools`).
- Never pick Codex `accept_execpolicy_amendment` or any `allow_always`/`allow_session` option; pick `allow_once`
  (Claude `allow-once`, omp `allow_once`, Codex `allow_once`).
- Reject maps to `reject_once` (omp, Claude `reject`) or Codex `cancel`. Codex's `cancel` ends the turn, so any `stopReason: cancelled` after a harness reject maps to the run's denied/policy-rejected outcome; no per-runtime flag is needed.
- Per-tool `deny` that must let the agent continue is only possible on Claude and omp.

## Recommended pins

Pinned versions: `@agentclientprotocol/claude-agent-acp` 0.100.0 and `@agentclientprotocol/codex-acp` 2.5.0. They were verified
when installed with npm; `claude-agent-acp --version` printed `0.100.0` and `codex-acp --version`
printed `@agentclientprotocol/codex-acp 2.5.0`. hal9001 installs these pins on request (see [runtimes.md](runtimes.md)).

Both adapters are `#!/usr/bin/env node` scripts, so hal9001 runs them as `<absolute node> <script>`. The
first install took about 33 s; `pnpm dlx` took about 19 s on a cold cache, which is why §7.3 installs them rather
than running them on demand. omp 18.16.0 is a Homebrew install; its `omp acp` is built in.

## Probe usage

`cmd/acp-probe` is a permanent diagnostic. Build: `go build -o /tmp/acp-probe ./cmd/acp-probe`.

```sh
acp-probe [flags] -- <agent command> [args...]
# omp, always-ask, edit/MCP via elicitation, tiny model, MCP echo server
acp-probe --elicitation --mcp-echo --policy log-and-reject --model anthropic/claude-haiku-4-5 \
  --prompt-file prompt.txt --env PI_CODING_AGENT_DIR=/tmp/omp-home -- omp acp --approval-mode always-ask
# claude, isolate user settings
acp-probe --mcp-echo --mode default --model haiku --policy log-and-reject \
  --session-meta '{"claudeCode":{"options":{"settingSources":["project"]}}}' -- claude-agent-acp
# codex
acp-probe --mcp-echo --mode read-only --config model=gpt-6-luna --config reasoning_effort=low \
  --env CODEX_HOME=/tmp/codex-home -- codex-acp
# context resume across processes
acp-probe --resume-check … -- <agent>
```

Flags: `--policy allow-all|reject-all|log-and-reject|cancel`, `--allow-always`, `--mode`, `--model`, `--config id=value`,
`--raw method=<json>` (`{{sid}}` expands), `--prompt`, `--prompt-file`, `--no-prompt`, `--mcp-echo`, `--canary`
(writes `AGENTS.md` and `CLAUDE.md` with canary tokens), `--write-file path=content`, `--cwd`, `--env K=V`,
`--session-meta JSON`, `--elicitation`, `--load ID`, `--resume ID`, `--resume-check`, `--list`, `--log FILE`,
`--timeout`. It advertises no `fs` or `terminal` capability. Output goes to stdout (summary: tool calls, permission
requests and decisions, whether the MCP echo tool executed, files created); every JSON-RPC line in both directions is
written to the JSONL wire log (`{"t","dir":"->"|"<-"|"stderr","msg"}`).

## Not verified

- A user-level Claude `permissions.allow` (this machine has none): shown through local-settings and flag-settings
  rules, and `settingSources` exclusion, not through `~/.claude/settings.json` itself.
- Why project `.claude/settings.json` allow rules did not bypass while `.claude/settings.local.json` did.
- Codex `agent-full-access` end to end, `workspace-write`, `agent`; Claude `plan` and `acceptEdits` behavior.
- `session/cancel` with an outstanding permission request, permission timeouts, and daemon-restart behavior on any runtime.
- omp user MCP servers (linear, notion) actually connecting inside an ACP session.
- The §7.2 proposal flow itself (propose phase, process exit, resume with approval message) beyond the `session/load`
  context check.
- HTTP/SSE MCP servers passed through `session/new` (only a stdio MCP server was used).
