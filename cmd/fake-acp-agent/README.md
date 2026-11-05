# fake-acp-agent

A scripted ACP agent over stdio (built on `github.com/coder/acp-go-sdk`, agent side). Tests and test setups of
`runtimes` use it instead of a real model, so modes, policy, approvals, resume, timeouts and cancellation are
deterministic. The implementation lives in `internal/agent/fakeagent`; Go tests build the script with
`fakeagent.Script` and write it with `Script.Write`.

```
fake-acp-agent --script script.json [--sessions-dir DIR] [--log FILE]
```

Environment fallbacks: `FAKE_ACP_SCRIPT`, `FAKE_ACP_SESSIONS`, `FAKE_ACP_LOG`. A flag/env value overrides the same
setting in the script. The script is validated at start; a malformed script exits with code 2 and a message on stderr.

## Script (JSON)

| Field | Meaning |
|---|---|
| `agent_info` | `{name, version}` returned by `initialize` (default `fake-acp-agent`/`0.0.0`). |
| `load_session` | Advertise and implement `session/load`. When false the agent answers method-not-found. |
| `modes` | `[{id, name}]` advertised on `session/new`; first is initial unless `initial_mode`. Empty: no modes advertised. `session/set_mode` rejects unknown ids. |
| `models` | Advertises a `model` select config option (category `model`); `session/set_config_option` with `configId: "model"` accepts these values. |
| `sessions_dir` | Persist sessions as JSONL (prompts, every update, mode changes) so `session/load` replays history. Without it sessions live in memory only and cannot be loaded. |
| `log_path` | Append one JSON line per ACP call received (see below). |
| `startup` | Steps run before `initialize` is answered (`exit`, `sleep`, `stderr`, `hang`, `ignore_sigterm`). |
| `turns` | One entry per `session/prompt`; the first whose `when` matches runs. |

Turn: `{ "when": {...}, "steps": [...], "stop_reason": "end_turn" }`. `when` (all set fields must match):
`mode` (current session mode id), `prompt_matches` (regexp over the prompt text), `session_turn` (0-based number of
prompts already answered in this session; persisted, survives `session/load`). A turn without `when` always matches. If
no turn matches, the prompt fails with a JSON-RPC error that names the mode/turn/prompt. If the client cancels the turn
the response is `stopReason: cancelled`.

Text fields accept `{{prompt}}`, `{{cwd}}`, `{{session_id}}`, `{{mode}}`, `{{turn}}` substitutions.

### Steps

Every step has `op`:

| op | Fields | Effect |
|---|---|---|
| `message` | `text` or `chunks` (+`delay_ms` between chunks) | `agent_message_chunk` updates. |
| `thought` | `text` or `chunks` | `agent_thought_chunk` updates. |
| `plan` | `entries: [{content, status, priority}]` | `plan` update. |
| `tool_call` | `id`, `title`, `kind`, `status` (default `pending`), `raw_input`, `content`, `locations` | `tool_call` update. `kind`: read, edit, delete, move, search, execute, think, fetch, switch_mode, other. |
| `tool_call_update` | `id`, `status` (default `completed`), `title`, `raw_output`, `content` | `tool_call_update`. |
| `permission` | `id`, `title`, `kind`, `raw_input`, `content`, `locations`, `options`, `on_allow`, `on_reject`, `on_cancel`, `no_tool_call`, `no_auto_update`, `cancel_on_reject`, `via`, `name`, `text` | Emits the `tool_call` (unless `no_tool_call` or already announced), sends `session/request_permission`, then runs `on_allow` / `on_reject` / `on_cancel` depending on the outcome. After `on_allow` / `on_reject` it emits a `tool_call_update` `completed` / `failed` unless `no_auto_update`. A `cancelled` outcome runs `on_cancel` and ends the turn with `stopReason: cancelled`. Default `options`: `allow` (`allow_once`) and `reject` (`reject_once`). `cancel_on_reject` ends the turn with `stopReason: cancelled` after a reject, as codex-acp does. `via: "elicitation"` asks through an `elicitation/create` form (`Allow tool: <name>` message, schema enum `Approve` / `Approve <name> for this session` / `Deny`) like omp does for non-execute tools; if the client did not advertise form elicitation the tool is refused without asking. |
| `sleep` | `ms`, `ignore_cancel` | Wait; ends the turn early on cancel unless `ignore_cancel`. |
| `hang` | `ignore_cancel` | Block until the turn is cancelled, then `stopReason: cancelled`. With `ignore_cancel` block forever (the client must kill the process). |
| `exit` | `code` | `os.Exit(code)` immediately (crash). |
| `ignore_sigterm` | | Ignore SIGTERM, so only SIGKILL stops the process. |
| `spawn_child` | `cmd`, `pid_file` | Start a child in the agent's process group; write its pid to `pid_file` (relative to the session cwd). |
| `write_file` | `path`, `data` | Write a file; `path` is relative to the session cwd. Put it in `on_allow` so the effect only happens when permitted. |
| `delete_file` | `path` | Remove a file. |
| `exec` | `cmd` | Run a command in the session cwd; a non-zero exit fails the turn. |
| `stderr` | `text` | Print to stderr. |
| `error` | `text` | Fail the prompt with a JSON-RPC error. |

`content` items: `{"type":"text","text":"..."}` or `{"type":"diff","path":"...","old_text":"...","new_text":"..."}`.

### Call log

With `log_path`, each received call appends a line with `method`: `initialize` (`client_capabilities`),
`session/new` (`cwd`, `mcp_servers`), `session/load` (`session_id`, `cwd`), `session/set_mode` (`mode`),
`session/set_config_option` (`config_id`, `value`), `session/prompt` (`session_id`, `mode`, `session_turn`, `text`),
`session/cancel`, and `permission_outcome` (`tool_call_id`, `outcome`, `option_id`, `allowed`).

## Example

```json
{
  "load_session": true,
  "modes": [{"id": "default"}, {"id": "bypass"}],
  "sessions_dir": "/tmp/fake-sessions",
  "turns": [
    {"when": {"mode": "default"}, "steps": [
      {"op": "message", "text": "Plan: create hello.txt"}
    ]},
    {"when": {"mode": "bypass"}, "steps": [
      {"op": "permission", "id": "c1", "kind": "edit", "title": "Write hello.txt",
       "raw_input": {"path": "hello.txt"},
       "on_allow": [{"op": "write_file", "path": "hello.txt", "data": "hi\n"}]},
      {"op": "message", "text": "{\"title\":\"done\",\"priority\":\"normal\",\"summary_md\":\"ok\"}"}
    ]}
  ]
}
```
