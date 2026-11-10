# hal9001 as an MCP server

hal9001 is an MCP server for other agents and editors on your Mac (Claude Code, Codex, omp, pi, ...). They can read your
inbox, approvals, flows, runs and sensors, and ask hal9001 to run a flow or poll a sensor. Anything with side effects
waits for your approval in the Inbox.

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
| `approvals_list`, `approval_get` | The approval queue (default `pending`) and history. Read only. |
| `flows_list`, `flow_get` | Flows with run statistics; one flow's definition and diagnostics. |
| `flow_runs_list`, `flow_run_get` | Flow runs; one run with its node runs (`wait` long-polls until it finishes or needs an approval). |
| `sensors_list`, `sensor_get`, `events_list` | Sensors and their last poll; events with the runs they started. |

These call the same REST endpoints as `curl` or the CLI ([api.md](api.md)) and return their JSON.

Side effects wait for your approval:

| Tool | What it does |
|---|---|
| `flow_run` | Run a flow now: polls its sensors and starts runs of that flow only (`POST /api/flows/{id}/run`). |
| `sensor_poll` | Poll a sensor outside its schedule; new items start runs of the flows subscribed to it (`POST /api/sensors/{id}/poll`). |

A call to one of these creates an approval in the Inbox ("MCP client: flow_run", with the tool and its arguments) and
holds the client's request open. Approve and it runs; deny (optionally with a comment) and the client gets a tool error and
nothing runs. After **4 minutes** without an answer it expires and the client gets an error. That is under the 5-minute
limit Claude Code applies to a silent HTTP call, but over the default tool timeout of some clients (Codex 60 s, omp
30 s, pi 60 s), so the commands below raise it. Arguments are validated before anyone is asked: an invalid call fails
without an approval.

There is no tool to approve or deny, to change a flow, sensor or setting, or to run anything else. Operator approvals
are ordinary approvals (`type: request`, no `flow_id` or `node_run_id`) and show in the Inbox, the menubar count, native
notifications and `GET /api/approvals`. They expire when the daemon restarts, like per-tool approvals of agent runs, and
decided ones are pruned with the retention setting.

## Security

- **Loopback only.** The daemon listens on `127.0.0.1`. `/mcp` additionally requires a loopback TCP peer, a loopback
  `Host` (`localhost`, `127.0.0.1`, `[::1]`) and, when present, a loopback `Origin`, and refuses requests with proxy
  headers (`Forwarded`, `X-Forwarded-*`, `Tailscale-*`). This holds even for a hostname in `allowed_hosts`: it is not
  reachable through `tailscale serve`. This is the DNS-rebinding protection the MCP specification asks for.
- **Same auth as the API.** `/mcp` needs the `api` service ([auth.md](auth.md)). Each tool then calls its REST endpoint
  with the same credentials, so `flow_run` and `sensor_poll` also need the `admin` service, exactly as `POST
  /api/flows/{id}/run` does. With the default stack, `localhost-trust` admits both for any process on this Mac and no
  credentials are needed.
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
