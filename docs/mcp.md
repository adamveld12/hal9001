# hal9001 as an MCP server

hal9001 is an MCP server for other agents and editors on your Mac (Claude Code, Codex, omp, pi, ...). They can read your
inbox, approvals, flows, runs and sensors, ask hal9001 to run a flow or poll a sensor, propose new flows and sensors, ask
you for a decision, and leave you notes. Anything with side effects waits for your approval in the Inbox.

This is separate from the MCP server hal9001 injects into the agents *it* runs ([design/agent-access.md](design/agent-access.md)).
That one is scoped to one run and exposes `gh_api`, `now`, `run_context` and the other run tools. The one on this page is
for clients **you** start, and exposes the operator tools below.

Settings → MCP in the web UI shows the exact URL, binary path and copy-paste install commands for your running daemon.
This page explains the model and lists the commands.

## Two transports, one tool set

| Transport | Endpoint | Use when |
|---|---|---|
| Streamable HTTP | `POST http://localhost:9001/mcp` (the daemon's real port) | The client supports remote MCP servers. Nothing to spawn. |
| stdio | `<hal9001 binary> mcp --url http://localhost:9001` | The client only starts local commands. The command is a bridge to the HTTP endpoint, so both expose the same tools and the same approvals. |

The stdio command needs the daemon to be running when the client starts it (it lists the tools once at start-up). It
exits with an error telling you so when it cannot reach `/mcp`.

The binary is `HAL9001.app/Contents/MacOS/hal9001` inside the app. If you ran **Install command-line tool**, `hal9001`
on your `PATH` is a symlink to it (`~/.local/bin/hal9001`) that follows app updates; the absolute path works too but
breaks if you move the app.

`hal9001 mcp --token <run token>` is what hal9001 itself hands to the agents it starts; it is unchanged. Without
`--token`, the same command is the operator bridge above.

## Tools

Reads run directly:

| Tool | What it does |
|---|---|
| `status` | Header counters: pending approvals, unread inbox items, queue, kill switch. |
| `inbox_list`, `inbox_get` | Inbox notes: search, filter by priority/flow/read/starred/archived; read one in full. |
| `approvals_list`, `approval_get` | The approval queue (default `pending`) and history. Read only. For a `request_approval` approval, `approval_get` adds `result` (below). |
| `flows_list`, `flow_get` | Flows with run statistics; one flow's definition and diagnostics. |
| `flow_runs_list`, `flow_run_get` | Flow runs (filters `flow_id`, `event_id`, `status`, `kind`, `since`, `until`); one run with its node runs (`wait` long-polls until it finishes or needs an approval). |
| `sensors_list`, `sensor_get`, `events_list` | Sensors and their last poll; events with the runs they started (filters `sensor_id`, `flow_id`, `status`, `decision`, `kind`, `q`, `since`, `until`). |
| `flow_validate`, `sensor_validate` | Check a spec without creating anything and without asking anybody (below). |

These call the same REST endpoints as `curl` or the CLI ([api.md](api.md)) and return their JSON.

Side effects wait for your approval:

| Tool | What it does |
|---|---|
| `flow_run` | Run a flow now: polls its sensors and starts runs of that flow only (`POST /api/flows/{id}/run`). |
| `sensor_poll` | Poll a sensor outside its schedule; new items start runs of the flows subscribed to it (`POST /api/sensors/{id}/poll`). |
| `flow_create` | Create a flow from a spec (`POST /api/flows`), after you approve it. |
| `sensor_create` | Create a sensor from a spec (`POST /api/sensors`), after you approve it. |

A call to one of these creates an approval in the Inbox ("MCP client: flow_run", with the tool and its arguments; for the
create tools "Create flow: <id>" with the spec) and holds the client's request open. Approve and it runs; deny (optionally
with a comment) and the client gets a tool error and nothing runs. After **4 minutes** without an answer it expires and the
client gets an error. That is under the 5-minute limit Claude Code applies to a silent HTTP call, but over the default tool
timeout of some clients (Codex 60 s, omp 30 s, pi 60 s), so the commands below raise it. Arguments are validated before
anyone is asked: an invalid call fails without an approval.

And two tools that talk to you:

| Tool | What it does |
|---|---|
| `request_approval` | Ask you for a decision, with a required description and optional editable values. Opens an approval in the Inbox. |
| `send_note` | Post a note to your Inbox and raise a notification. No approval. |

There is no tool to approve or deny, to change or delete a flow, sensor or setting, or to run anything else. Operator
approvals are ordinary approvals (`type: request`, no `flow_id` or `node_run_id`) and show in the Inbox, the menubar count,
native notifications (with Approve and Deny) and `GET /api/approvals`. The ones a tool call waits on (`flow_run`,
`sensor_poll`, the create tools) expire when the daemon restarts, like per-tool approvals of agent runs; a
`request_approval` one does not (below). Decided ones are pruned with the retention setting.
For those gated calls hal writes the approval's description from the call's facts (for example `Run the flow "triage" now: poll
its sensors and start runs of it.`); only `request_approval` carries a description the agent wrote. Agents that run inside hal
(flow agent nodes, the chat assistant) have a `request_approval` tool with the same arguments among their hal tools, see
[design/agent-access.md](design/agent-access.md).

### Arguments

**`flow_validate`, `sensor_validate`**: `spec` (string, required): the flow or sensor as YAML or JSON text, the same schema as
a file under `flows/` or `sensors/` ([flows.md](flows.md)); `id` (string, optional): the id (file
name) it would be stored under, otherwise the spec's top-level `id:`. Returns `{valid, id, id_available, errors, warnings}`;
each finding has `severity`, `code`, `message` and, where known, `path`, `node_id` and `line`. It runs the validator behind
`POST /api/flows/validate` (graph, fields, templates, references to sensors and prompts), so a spec that is valid here is
accepted by `flow_create`, unless something changed in between.

**`flow_create`, `sensor_create`**: the same `spec` and `id`. The spec is validated first. **Errors come back as a tool
error that lists the findings and nobody is asked**, so the agent can fix the spec and call again. A valid spec raises the
approval, which shows the id, the description, the spec as YAML and a summary: for a flow whether it is **enabled**, whether
it is a dry run, the sensors it subscribes to, the prompts and custom tools its nodes use, and which nodes run code or
start agents; for a sensor its schedule and language. The file is written only if you approve. **An existing id is refused**
(before the approval and again when the file is written): these tools never overwrite; there is no tool that edits or
deletes. A JSON spec is stored as block-style YAML.

- **Enabled state.** A flow is created as the spec says. `enabled` defaults to `false` when the spec omits it, so a flow
  without `enabled: true` is created **disabled** (and runs only when run by hand); the approval says which, so you see
  `enabled: true` before you approve it. Dry run is likewise the spec's own `dry_run` (default false).
- **Sensors** do nothing on a schedule until an enabled flow subscribes to them; with neither `interval` nor `cron` they run
  only when polled. So a sensor created over MCP is inert until a flow you enabled subscribes to it.
- **References.** Sensors and prompts a flow names must exist (the validator reports them), so create the sensor first.

**`request_approval`**: `title` (string, required, at most 200 characters); `description` (string, **required**, at most 600
characters: one or two sentences on what the agent wants to do and why; a call without it is a tool error,
`description is required: say in one or two sentences what you want to do and why`, and the agent retries). The Inbox card
shows it labelled as written by the agent. `body` (markdown, at most 16 KiB); `priority`
(`low`, `normal`, `high`, `urgent`; default `normal`); `editable` (up to 20 `{key, value}` entries the approver may change
before approving: `key` is a dot-path such as `reply` or `draft.subject`, `value` any JSON value shown as the starting value:
the Request approval node's editable fields); `timeout_seconds` (10 to 604800, default **86400** = 24 hours): how long the
approval stays open; `wait_seconds` (0 to 240, default **240**): how long this call waits for you.

It returns `{approval_id, status, decision, comment, decided_by, decided_at, values, edited, edits, message}`:

- `decision` is `approved`, `denied` or `expired`, or `pending` when `wait_seconds` ran out with no decision (`status` is the
  same word). `comment` is what you typed. When you deny with a comment, `message` is `Denied by the user: "<text>". Do not
  retry the same action unchanged; do what the user asked instead.` (steering the agent should follow); a held approval
  (steering limit reached) says to stop and wait. `values` (approved only) is the final value of every editable field, `edits`
  the ones you changed as `{before, after}`; a denial ignores edits.
- **Polling pattern.** An MCP call must not sit silent for more than about 4 to 5 minutes, so `wait_seconds` is capped at 240
  (the same limit as the other approvals). If you have not decided by then the call returns
  `{approval_id, status: "pending", ...}` and the approval **stays open**. The agent polls `approval_get` with the
  `approval_id` (for example every 30 to 60 s) and reads `result.decision`; `result` has the same shape as the call's own
  return value. `wait_seconds: 0` creates the approval and returns at once.
- It expires only after `timeout_seconds` (default 24 hours), then `decision` is `expired`. A daemon restart does not expire
  it. At most 10 `request_approval` approvals can be open at once.
- The agent cannot decide it: you approve or deny in the Inbox, the menubar or the notification, like any approval.

**`send_note`**: `title` (required, one line, at most 200 characters); `body` (markdown, at most 16 KiB); `priority`
(`low`, `normal`, `high`, `urgent`; default `normal`; `high` and `urgent` play the notification sound); `url` (an absolute
http or https link, at most 2048 characters) with `link_label` (at most 100); `thread` (at most 64 characters: a new
notification with the same thread replaces the previous native one). It creates an Inbox note, raises a notification through
the normal path (the app, or terminal-notifier, per `notify_via` in [getting-started.md](getting-started.md)) and returns `{item_id, url}`.
**Limits:** 5 notes per minute and 60 per hour per client; over that the call fails with a rate-limit error that says when to
retry.

### Attribution

Everything created through MCP says where it came from: approvals carry `request.source: "mcp"` and `request.client`, notes
have `source: "mcp"`, `client` and `thread_key` (`flow_id` `mcp`, `node_id` `send_note`), notifications use the flow id `mcp`
and are titled with the client, and the daemon log records each raised approval, decision and note with the principal. The
client name is the `clientInfo` the client sent in its MCP `initialize` request (`hal9001 mcp`, the stdio bridge, forwards
its own client's), or its `User-Agent` when there was none. It is a label the client chooses, not an identity: the caller's
identity is the authenticated principal, shown as "requested by" on the approval.

## Security

- **Loopback only.** The daemon listens on `127.0.0.1`. `/mcp` additionally requires a loopback TCP peer, a loopback
  `Host` (`localhost`, `127.0.0.1`, `[::1]`) and, when present, a loopback `Origin`, and refuses requests with proxy
  headers (`Forwarded`, `X-Forwarded-*`, `Tailscale-*`). This holds even for a hostname in `allowed_hosts`: it is not
  reachable through `tailscale serve`. This is the DNS-rebinding protection the MCP specification asks for.
- **Same auth as the API.** `/mcp` needs the `api` service ([auth.md](auth.md)). Each tool then calls its REST endpoint
  with the same credentials, so `flow_run`, `sensor_poll` and the create tools also need the `admin` service, exactly as
  `POST /api/flows/{id}/run` and `POST /api/flows` do; so do `request_approval` and `send_note`, which have no REST
  endpoint of their own but change state the same way. The `admin` check happens before anything is asked or created, so a
  client that is not admitted gets the 401 at once instead of after your approval. With the default stack,
  `localhost-trust` admits both services for any process on this Mac and no credentials are needed.
- **Credentials when your stack asks for a login.** There is no static MCP token: hal9001 authenticates with session
  cookies. Log in as in [auth.md](auth.md#logging-in-from-scripts) and pass the cookie: the `Cookie:
  hal9001_session=<id>` header for HTTP, or `HAL9001_SESSION_COOKIE=<id>` in the environment of the stdio command (the
  same variable the CLI reads; the bridge also takes `--session-cookie`). The snippets in Settings → MCP show these forms
  when your `api` stack does not contain `localhost-trust`.
- **What approvals do and do not protect.** The approval is a decision point you see, for tools that an agent calls
  through MCP. It is not a boundary against a process that can reach `127.0.0.1:9001` itself: with `localhost-trust`, such a
  process can call the REST API directly, including `POST /api/approvals/{id}/approve` ([auth.md](auth.md#threat-notes)).
  Remove `localhost-trust` from the `approve` and `admin` stacks to make approvals meaningful against local processes.
- No CORS headers, JSON-only bodies and the `Host`/`Origin` allow-list apply as for every request.

The endpoint is stateless: no `Mcp-Session-Id`, JSON responses, `GET` and `DELETE` answer 405 (it opens no
server-to-client stream, and sends no `tools/list_changed`). Restart the client to pick up tool changes after a daemon
update.

## Install in your client

Replace `<HAL>` with the binary path, and the port if you changed it. Settings → MCP fills these in. If your `api` stack
needs a login, add the cookie as shown in each block. Syntax below was read from each tool's own help and documentation;
the commands were not run against a real configuration.

Timeouts: a call that needs approval can take up to 4 minutes, and most clients give a tool call less by default, so each
block sets 300 s (the unit differs per client).

### Claude Code

Checked against `claude mcp add --help` (2.1.311) and the [Claude Code MCP docs](https://code.claude.com/docs/en/mcp).
Streamable HTTP needs Claude Code 1.0.27 or later.

```sh
# HTTP
claude mcp add --transport http hal9001 http://localhost:9001/mcp
# HTTP with a login cookie (put the name and URL before --header)
claude mcp add --transport http hal9001 http://localhost:9001/mcp --header "Cookie: hal9001_session=<id>"

# stdio
claude mcp add --transport stdio hal9001 -- <HAL> mcp --url http://localhost:9001
# stdio with a login cookie (--env is variadic: keep another option between it and the name)
claude mcp add --env HAL9001_SESSION_COOKIE=<id> --transport stdio hal9001 -- <HAL> mcp --url http://localhost:9001
```

Scopes (`--scope`): `local` (default: this project only, private, in `~/.claude.json`), `user` (all your projects),
`project` (writes `.mcp.json` in the repository; Claude asks before using it). Verify with `claude mcp list`, then `/mcp`
in a session. Claude Code has no short default tool timeout (`MCP_TOOL_TIMEOUT`, or `timeout` in milliseconds through
`claude mcp add-json`).

### Codex

Checked against `codex mcp add --help` (0.162.1) and the [config reference](https://developers.openai.com/codex/config-reference).
Streamable HTTP needs no experimental flag from Codex 0.45.0, and `codex mcp add --url` exists from 0.46.0. The default
tool timeout is 60 s and `codex mcp add` has no timeout or header option, so write the table in `~/.codex/config.toml`:

```toml
# HTTP
[mcp_servers.hal9001]
url = "http://localhost:9001/mcp"
# http_headers = { Cookie = "hal9001_session=<id>" }   # only when a login is required
tool_timeout_sec = 300

# stdio (instead of the above)
[mcp_servers.hal9001]
command = "<HAL>"
args = ["mcp", "--url", "http://localhost:9001"]
# env = { HAL9001_SESSION_COOKIE = "<id>" }             # only when a login is required
tool_timeout_sec = 300
```

Without a timeout or header you can use `codex mcp add hal9001 --url http://localhost:9001/mcp` or `codex mcp add hal9001
-- <HAL> mcp --url http://localhost:9001`. Verify with `codex mcp list` or `/mcp` in the TUI.

### OMP (Oh My Pi)

Checked against `omp --help` (18.18.5) and the [oh-my-pi docs](https://github.com/can1357/oh-my-pi/blob/main/docs/mcp-config.md).
There is no `omp mcp` shell command: MCP servers live in `~/.omp/agent/mcp.json` (user) or `.omp/mcp.json` (project).
`timeout` is in milliseconds (omp's default is 30 s, and progress notifications do not extend it):

```json
{
  "mcpServers": {
    "hal9001": {
      "type": "http",
      "url": "http://localhost:9001/mcp",
      "timeout": 300000
    }
  }
}
```

For stdio use `"type": "stdio", "command": "<HAL>", "args": ["mcp", "--url", "http://localhost:9001"]` instead of `url`.
With a login, add `"headers": { "Cookie": "hal9001_session=<id>" }` (HTTP) or `"env": { "HAL9001_SESSION_COOKIE": "<id>" }`
(stdio). Then run `/mcp reload`, `/mcp list` and `/mcp test hal9001` in omp.

Inside omp, `/mcp add hal9001 --scope user --url http://localhost:9001/mcp --transport http` (or `/mcp add hal9001 --scope user
-- <HAL> mcp --url http://localhost:9001`) also works; those flags come from the installed binary's usage text and were not
run, and they cannot set a header, env or timeout.

### Pi agent

Checked against `pi mcp add --help` (1.5.0) and the installed `docs/mcp.md`. Pi has MCP support built in (since 0.99.0), no
extension needed; a third-party MCP adapter that registers `/mcp` replaces it and ignores `mcp.json`. The default
exposure is `codemode`, which hides the tools from the model, so pass `--exposure direct`:

```sh
# HTTP
pi mcp add hal9001 --url http://localhost:9001/mcp --exposure direct
# HTTP with a login cookie (KEY=VALUE, not "Name: value")
pi mcp add hal9001 --url http://localhost:9001/mcp --exposure direct --header 'Cookie=hal9001_session=<id>'

# stdio
pi mcp add hal9001 --exposure direct -- <HAL> mcp --url http://localhost:9001
# stdio with a login cookie
pi mcp add hal9001 --exposure direct --env HAL9001_SESSION_COOKIE=<id> -- <HAL> mcp --url http://localhost:9001
```

This writes `~/.pi/agent/mcp.json` (`-l` for the project's `.pi/mcp.json`, trusted projects only). `pi mcp add` has no timeout
option; pi's per-server `timeout` is in **seconds** (default 60), so add `"timeout": 300` to the server's entry in that file.
Verify with `pi mcp list` (connects and lists the tools) or `/mcp` in a session; tools are named `mcp__hal9001__<tool>`.

## Other clients

Any MCP client that supports Streamable HTTP can use `http://localhost:9001/mcp` (add the `Cookie` header only when a login
is required). Any that starts local commands can use `<HAL> mcp --url http://localhost:9001`. To try it by hand:

```sh
curl -sS http://localhost:9001/mcp -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```
