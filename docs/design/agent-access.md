# Agent access: hal tools, sandboxes, custom tools

Status: design agreed with the user (2026-11-06); runtime lockdown details pending `docs/acp-lockdown.md`.

## Principles

- Agents have **no built-in system access** (file, shell, web, sub-agents). Each runtime runs in a locked-down
  configuration (from `docs/acp-lockdown.md`); a runtime that cannot be locked down is not usable.
- Every agent session (flow agent nodes and assist sessions) gets hal9001's MCP server injected. All actions go through
  it; hal9001 decides each call (policy or an approval in the Inbox) at execution time and records it in the trace.
- There is no `native` access mode.

## The hal MCP server

- `hal9001 mcp --token <run token>`: stdio MCP server spawned by the agent (injected via ACP `session/new`
  `mcpServers`, absolute path to the running binary). It proxies `tools/list` and `tools/call` to the daemon over
  HTTP using a per-run capability token (random, stored hashed, scoped to that run's tool set, revoked at run end).
- The runtime is configured to auto-allow hal's own MCP tools (no second prompt at the agent layer).
- Clients the user starts themselves (Claude Code, Codex, omp, pi) get a separate operator tool set, over Streamable HTTP
  (`POST /mcp`) and over stdio (`hal9001 mcp` without `--token`, a bridge to `/mcp`); see [../mcp.md](../mcp.md). Its tools
  with side effects wait for an approval in the Inbox that belongs to no run.

## Tool sets

Always available to every agent run (executed on the local hal instance):

| Tool | Notes |
|---|---|
| `gh_api(method, path, params)`, `pr_view`, `pr_diff`, `pr_list`, `issue_view`, `search` | `gh` CLI with the user's auth. GET auto-allowed; POST/PATCH/PUT/DELETE need approval by default. |
| `now(timezone?)` | Current time. |
| `run_context()`, `node_input()`, `event()` | The run's state: ids, trigger, node input payload, original event. |

Added when the agent node sets `sandbox:` (see below): `fs_read`, `fs_write`, `fs_list`, `fs_search`, `shell` executed in
that sandbox's working directory.

Added per agent node via `tools: [ids]`: user-defined custom tools.

Assist sessions get `draft_read`, `draft_write`, `draft_test`, `validate` (+ the always-available set) instead of editing
files. They also get read-only inspection tools (everything the CLI can list, get and inspect: runs, transcripts, sandbox
logs, sensors, events, inbox, approvals, config; no approval, capped results) and `github_issue_link`, which builds a
prefilled GitHub new-issue link and calls nothing. Flow agent nodes get neither. See [../assist.md](../assist.md#tools-and-permissions).

## Context preamble

The first prompt of every agent run starts with a context block: run/flow/node ids, current time and timezone, trigger
(sensor id, event id, polled at, run kind), the node input payload (pretty JSON, truncated with a note when large), and a
summary of available tools.

## Approvals

- Built-in tools: defaults above.
- Custom tools: `approve: never | always | {when: <conditions>}` where conditions reuse the Transform condition model,
  evaluated against the tool arguments. The agent node may override per tool.
- Sandbox `shell` / `fs_write`: per-sandbox policy `approval: auto | always | rules` with command-regex and path rules.
- Pending calls are tool_call approvals in the Inbox (tool, args, diff for `fs_write`); the agent waits up to
  `approval_timeout`. Deny returns a tool error to the agent.

## Sandboxes (Configure → Sandboxes)

One config per backend, one tab each. Stored in `<home>/sandboxes/<backend>.yaml`; per-sandbox env in
`env.d/sandboxes/<backend>.env` (plain/secret markers as elsewhere).

Common fields: `enabled`, `lifecycle: per_run | persistent`, `approval` (above), `env` (via the env section), `timeout`.

| Backend | Config | per_run | persistent |
|---|---|---|---|
| `local` | `root` dir | `<root>/runs/<run-id>/`, removed after a successful run | `<root>` |
| `docker` | `socket` (default from env/`DOCKER_HOST`), `images: [..]`, resources, network on/off | container per run, removed at end | one long-lived container per image, per-run workdir |
| `depot` | token (secret), `org_id`, `image_ref` (Depot snapshot image), `architecture`, `resources`, `timeout_minutes`, `disable_tailnet`, `disable_depot_cache`, CI `secrets` mapping | `Sandbox.create` per run, stopped at end | one sandbox kept alive (refreshed timeout), per-run workdir |
| `kubernetes` | `kubeconfig`, `context`, `namespace`, `images: [..]`, resources, `service_account` | `kubectl run` one pod per run, deleted at end | one hal-created pod per image, per-run workdir |
| `ssh` | `host`, `port`, `user`, auth (key file or password, stored as secrets), host key (pinned fingerprint; first connect shows it for confirmation), `root` | `<root>/runs/<run-id>/` | `<root>` |

- Implementation: Docker via the Engine API on the socket; Depot via the `depot.sandbox.v1` API from Go; Kubernetes by
  shelling out to `kubectl` (`run`, `exec`, files via exec + base64); SSH via the Go SSH client.
- Agent node picks the sandbox and, for Docker/Kubernetes, the `image:` from that sandbox's list (default first).
- Failed per_run sandboxes are kept for debugging until retention.

## Sandbox run log

Every agent run that uses a sandbox gets a log, so you can see what happened without the backend's own console. It is
shown under **Sandbox** on the agent run page and on the node run in the flow run detail, updates live while the sandbox
is in use, and can be copied or downloaded as text. The same data is served by `GET /api/agent-runs/{id}/sandbox-log`
and `GET /api/node-runs/{id}/sandbox-log` (a provisioning failure has a node run but no agent run), and
`hal9001 agent-runs sandbox-log <id>`.

Captured:
- Header: backend, sandbox id or name (Depot sandbox id, container name, pod name, `user@host`, run directory), image,
  repo and ref (without credentials), lifecycle, workdir, status (`provisioning`, `ready`, `failed`, `closed`,
  `interrupted`), provisioning time.
- Lifecycle: provisioning start, the names of the environment variables commands see (never values), clone and
  checkout, input.json, ready, the backend's error message when provisioning fails, teardown (workspace removed or kept
  for debugging) and its error.
- Backend messages, best effort: Docker image pull and container start, Kubernetes pod events (scheduling, image pull,
  failures), Depot sandbox states.
- Every `shell` command and the script of a custom tool that runs in the sandbox: command line, cwd, exit code or
  timeout, duration, stdout, stderr and how it was allowed (`auto: <policy reason>` or `approved by user`).
- Every `fs_read`, `fs_write`, `fs_list`, `fs_search`: path, size or hit count, duration, error.
- Calls that were not run: denied by policy, denied by a user (with the comment), expired, or held back because the run is
  read-only.

Limits and redaction:
- Secret values (global env, the sandbox's env, custom tool env, the git token) are replaced by `«redacted»` in commands,
  output, errors and fields, like everywhere else in traces. Encoded values are not caught.
- Each command's stdout and stderr is capped at 64 KiB: the first and last 32 KiB are kept with a marker between them.
- A run's log holds at most 5000 entries and 8 MiB. After the byte limit entries keep their metadata but lose output;
  after the entry limit only lifecycle entries are kept. The header says `truncated`.
- Stored in `sandbox_sessions` and `sandbox_log_entries`, deleted with the node run by retention pruning. Sandboxes still
  open when the daemon stops are marked `interrupted` at the next start.
- Denials of custom tools that run in a sandbox are in the agent run's trace, not in the sandbox log; assist sessions
  cannot use sandboxes, so they have no log.

## Workspace seeding

At run start the sandbox workdir gets:
- an optional git clone of the node's `repo:` at `ref:` (credentials from the user's `gh` auth, supplied only to the
  clone and not left in the environment),
- `input.json` with the node input payload.

## Environment and secrets

- hal's global env vars pass through to every sandbox; each sandbox adds/overrides its own.
- Commands get real values. Secret values are redacted in everything returned to the agent and in traces. (Encoded
  exfiltration remains possible; documented.)

## Custom tools (Configure → Tools)

YAML in `<home>/tools/<id>.yaml`: `id`, `description`, `params` (name → `{type, description, required, enum, default}`)
or `schema:` (raw JSON Schema override), `lang: bash | python | node | exec`, `script` / `command`, `approve`,
`timeout`, `runs_in_sandbox: bool`, credentials in `env.d/tools/<id>.env`.

- Invocation: arguments as JSON on stdin and as `ARG_<UPPER_NAME>` env vars (scalars as strings, arrays/objects as
  JSON). Stdout is the result (structured when it parses as JSON). Non-zero exit is a tool error with the stderr tail.
- `runs_in_sandbox: true` runs the tool in the calling node's sandbox (error if the node has none); otherwise on the
  local hal instance.
- The Tools pages support Create with AI / Ask AI (assist target kind `tool`) and a test runner with an args form.

## Scope

Sandboxes back agent fs/shell tools and custom tools only (not command nodes or sensors, for now).
