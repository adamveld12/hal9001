# Writing sensors, flows and prompts

Everything here lives in files under the home directory (`~/.hal9001` by default) and is hot-reloaded:

| File | Defines |
|---|---|
| `sensors/<id>.yaml` | A scheduled command that prints items. |
| `flows/<id>.yaml` | A DAG of nodes subscribed to sensors. |
| `prompts/<name>.md` | A prompt template for agent nodes. |
| `env` | `KEY=VALUE` secrets injected into every sensor and command node. |
| `env.d/sensors/<id>.env` | `KEY=VALUE` secrets of one sensor, same format as `env`. Not in git. |

Check your work offline with `hal9001 validate` (add `--json` for machine-readable diagnostics) or against the running
daemon with `hal9001 flows validate flow.yaml`. Ids are letters, digits, `-` and `_`, at most 64 characters, starting
with a letter or digit, and must match the file name.

## The node execution contract

It applies to sensors and `command` nodes.

**Process.** `lang` is one of:

| `lang` | Runs |
|---|---|
| `bash` | `script` written to a temp file, run with `bash` |
| `python` | `script` run with `python3` |
| `node` | `script` run with `node` |
| `jq` | `script` run as `jq -c -f <file>` |
| `exec` | `command: [argv...]` run directly (no shell, no `script`) |

The interpreter is looked up on the `PATH` the daemon was given (under launchd: the login-shell `PATH` that
`hal9001 serve` captures when `HAL9001_LAUNCHD=1`). The process starts in its own process group. On timeout or kill-switch the group gets SIGTERM and,
after 10 seconds, SIGKILL.

**Streams.** stdin is the node's input payload (JSON): the sensor item for an entry node, or the upstream node's output.
stdout is the result. stderr is captured as logs and shown in the trace. Stdout and stderr are each capped at 16 MiB;
exceeding the cap on stdout is an error.

**Working directory.** The node's `$HAL_STATE_DIR` unless `cwd` is set (command nodes: `cwd: "~/some/dir"` or
`cwd: {mode: dir, path: "..."}`; a template over the data model below; `~` is expanded). A sensor's `cwd` is a plain
path.

**Result.**

| stdout | exit | Meaning |
|---|---|---|
| empty | 0 | **Drop**: the branch ends, recorded as decision `dropped`. So `jq 'select(...)'` is a filter. |
| one JSON value | 0 | Emit on port `out`, or on `_port` if the value is an object that has one. |
| JSONL, N values | 0 | Emit N payloads (split / fan-out), each routed by its own `_port`. |
| any | non-zero | Error. Goes to the `error` port if an edge uses it, otherwise the branch fails. |
| any | killed by timeout | Error `timeout`. |

Invalid JSON, an undeclared `_port`, and truncated stdout are errors too. An undeclared `_port` fails the whole node run
rather than emitting a partial fan-out. A value that is not an object (a string, number, array) is emitted on `out`
unchanged.

**Reserved keys.** In an output object, `_port` (string) selects the output port and `_reason` (string) is shown as the
decision reason in the trace. Both are stripped before the payload reaches downstream nodes.

**Environment.** A node does not inherit the daemon's environment. It gets `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`,
`LC_ALL`, `LC_CTYPE`, `TMPDIR`, `TZ`, `SSH_AUTH_SOCK`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and `PATH`,
then everything in the `env` file, then:

| Variable | Value |
|---|---|
| `HAL_EVENT_FILE` | Path to the original event JSON (not set during a sensor poll). |
| `HAL_RUN_ID`, `HAL_EVENT_ID`, `HAL_FLOW`, `HAL_NODE` | Ids of the current run, event, flow, node. |
| `HAL_STATE_DIR` | Persistent scratch directory for this node. |
| `HAL_DRY_RUN` | `1` in a dry run. |
| `HAL_LAST_SUCCESS_AT` | Sensors only: RFC 3339 time of the last successful poll, for incremental fetches. Unset before the first success. |

`HAL_*` values from the `env` file are ignored. Values from `env` are replaced with `«KEY»` in all stored stdout,
stderr, scripts and argv.

Error classes recorded on a failed node run: `timeout`, `exit_nonzero`, `undeclared_port`, `invalid_output`, `killed`,
`interrupted`, `template`, `config`, `start_failed`. Defaults: command timeout 60s, agent timeout 30m.

### Examples

jq: a filter that also annotates (empty output drops the item):

```yaml
- id: only-review-requests
  type: command
  lang: jq
  script: 'select(.reason == "review_requested") | . + {_reason: "review requested"}'
```

jq: one output object per element of an array (fan-out), because `jq -c` prints one JSON value per line:

```yaml
- id: split-labels
  type: command
  lang: jq
  script: '.labels[] | {label: ., issue: .id}'
```

bash: use `HAL_STATE_DIR` for state and `HAL_DRY_RUN` to be careful with side effects:

```yaml
- id: notify-pager
  type: command
  lang: bash
  side_effect: true        # skipped in dry-run, recorded as skipped:dry_run
  script: |
    set -euo pipefail
    title=$(jq -r .title)
    curl -fsS -X POST "$PAGER_URL" -d "title=$title"
    echo '{"paged": true}'
```

python: route to a declared port:

```yaml
- id: classify
  type: command
  lang: python
  ports: [small, large]
  script: |
    import json, sys
    ev = json.load(sys.stdin)
    n = ev.get("lines_changed", 0)
    ev["_port"] = "small" if n < 200 else "large"
    ev["_reason"] = f"{n} lines changed"
    print(json.dumps(ev))
```

node:

```yaml
- id: shorten
  type: command
  lang: node
  script: |
    const ev = JSON.parse(require("fs").readFileSync(0, "utf8"));
    console.log(JSON.stringify({...ev, title: ev.title.slice(0, 80)}));
```

exec:

```yaml
- id: lookup
  type: command
  lang: exec
  command: [/usr/local/bin/my-lookup, --json]
  timeout: 10s
```

## Sensors

```yaml
# sensors/github-notifications.yaml
interval: 2m              # or: cron: "*/5 9-18 * * 1-5"; leave both out for a manual-only sensor
lang: bash
script: |
  gh api notifications --paginate -q '.[]'
key: "{{.id}}:{{.updated_at}}"
first_run: baseline
timeout: 60s
```

| Field | Meaning |
|---|---|
| `interval` / `cron` | At most one. Interval is a Go duration (`30s`, `2m`, `1h`; minimum `1s`). Cron is 5 fields (minute hour day-of-month month day-of-week) with lists, ranges, steps and names, in local time. With neither, the sensor is **manual-only**: the scheduler never polls it (no next poll; the API reports `status.manual: true` and the UI shows "manual") and only Run now does. |
| `lang`, `script` / `command`, `cwd`, `timeout` | As in the node contract. `timeout` defaults to 60s. |
| `key` | Required. A template over one item that yields its dedup key. Items whose key was already seen are dropped; a changed key (for example one that includes `updated_at`) re-triggers. |
| `first_run` | `baseline` (default): the scheduler's first poll marks everything seen without firing. `fire`: emit everything. A manual poll (Run now) never baselines. |

The sensor prints a JSON array, or JSONL (one item per line). Every poll is recorded (duration, item count, new count,
stdout, stderr, exit code); a failed poll does not advance `HAL_LAST_SUCCESS_AT`.

Only sensors subscribed to by at least one **enabled** flow are scheduled, and never a manual-only one.
`hal9001 events inject <sensor-id> < item.json` pushes one item through dedup and fan-out without running the script.
Starter sensors and their credentials are listed in the [README](../README.md#prerequisites).

**Run now.** Kick off a sensor and its flows whenever you want instead of waiting for the interval:
`hal9001 sensors poll <id> [--rerun-seen] [--flow ID ...] [--wait]` (UI: Run now on the sensor page;
`POST /api/sensors/{id}/poll` with an optional body `{rerun_seen, flows}`). It polls any valid sensor now, subscribed or
not, paused or not, and waits for the poll to finish. Items with a new dedup key become events and fan out to the
enabled flows subscribed to the sensor, as for a scheduled poll. The response is the sensor run plus `new_events`,
`rerun_events` and `flow_run_ids`, so you can follow the runs (`hal9001 runs get <id> --wait`; `--wait` on the poll
waits for all of them and prints their final status as `flow_runs`).

- **Re-run seen items.** Items whose key was already seen create no event, so by default nothing happens for them. With
  `--rerun-seen` (UI: "Re-run items already seen"; remembered per sensor in the browser) each such item is replayed
  as a `kind: replay` run into every target flow, on the flow's *current* version, as `events replay` does. The event
  that is replayed is the most recent stored event of that key. If that event was already pruned by retention (the
  key is remembered longer than events), the item is stored again as a new event belonging to this manual poll (its
  sensor run has trigger `manual`) and run. The dedup state does not change, and replay runs show in the timeline by
  default.
- **Limit the flows.** `flows` (`--flow ID`, repeatable) sends new events and re-runs only to those flows. Each must
  subscribe to the sensor (422 otherwise) and may be disabled. Note that an event created this way is not run by the
  other subscribed flows later: its key is now seen (replay it into them with `events replay`).
- **No baseline.** A manual poll never applies `first_run: baseline`: on a sensor's very first poll its items become
  events normally. Only the scheduler's first poll baselines. A manual first poll still counts as the first
  success, so the scheduler does not baseline afterwards.
- The kill switch rejects Run now with 423. A poll that is already running gives 409.

**Run a flow now.** `hal9001 flows run <flow-id> [--rerun-seen] [--wait]` (UI: Run now in the flow editor and on the
Flows list; `POST /api/flows/{id}/run`) polls every sensor the flow subscribes to, in parallel, each as a normal
manual sensor run, with this flow as the only target: other flows subscribed to those sensors get nothing. The saved
version of the flow runs (the editor disables Run now while it has unsaved changes). The result lists each sensor's run
with its counts and `flow_run_ids`, plus all created `flow_run_ids`; a sensor that cannot be polled (missing, invalid,
already running) is reported with an `error` and the others still run. A flow that subscribes to nothing gives 422. A
flow with `enabled: false` can still be run this way: running it is an explicit user action, so "This flow is
disabled; Run now still runs it once."

**Resetting a sensor.** `hal9001 sensors reset <id> [--run [--flow ID ...]] [--yes]` (UI: "Reset dedup state…" in the
menu of the sensor page; `POST /api/sensors/{id}/reset` with `{run, rerun_flows}`) makes the sensor forget what it has
seen: its dedup keys are cleared, and so is its incremental state (`last_success_at`, so `HAL_LAST_SUCCESS_AT` is
unset on the next poll). Who did it and when is stored (`status.reset_at`, `status.reset_by`; the sensor page header
shows it). Every item the sensor returns next time counts as new and triggers its flows again. Events, sensor runs, flow
runs and inbox items stay as history (the new events sit next to the old ones; a later re-run replays the newest event
of a key), and files the script keeps in `$HAL_STATE_DIR` are not touched. Without `--run` the next *scheduled* poll is
that of a brand-new sensor, so `first_run: baseline` baselines again and `fire` emits everything. With `--run` the
reset is followed at once by a manual poll, which never baselines. A reset works while the sensor is paused (it stays
paused), is refused with 409 while a poll of the sensor is running, and with `--run` is rejected with 423 under the
kill switch (a plain reset is not a poll and still works). Without `--yes` the CLI asks for confirmation when it runs on
a terminal.

**Pausing one sensor.** `hal9001 sensors pause <id>` (UI: the pause toggle on the Sensors list and the sensor page)
stops the scheduler polling that sensor until `sensors resume <id>`. The state is operational, not part of the YAML: it
lives in the database, survives restarts and records who paused the sensor and when. A paused sensor has state `paused`
and no next poll. Explicit actions still work: Run now, reset and `events inject` are user requests, not
scheduled polls. Resuming starts the schedule over (an overdue interval sensor polls at once). The kill switch is
separate: it pauses every sensor and also rejects Run now.

**Per-sensor environment.** Credentials a single sensor needs can be set on the sensor instead of the global `env`
file: the Environment section of the sensor page, or `hal9001 sensors env set <id> KEY`. They are stored in
`env.d/sensors/<id>.env` (mode 0600, git-ignored; sensor YAML stays safe to commit), not in the sensor YAML. A poll
gets the global `env` values with the sensor's own values on top (the sensor's value wins; the UI marks keys that
override a global one). Secret values are write-only: the API and UI list key names only. They are redacted from
stored output like the global values. A change applies on the next poll without a restart, and deleting the sensor
deletes its env file.

**Plain (non-secret) values.** Every env value is secret by default. A value that is not sensitive, such as a login
name or a host name, can be marked plain: the Secret toggle in the env editor (global or per sensor),
`hal9001 env set KEY --plain` / `hal9001 sensors env set <id> KEY --plain` (with no value, `--plain` and `--secret`
flip the flag of an existing key), or `secret: false` in `PUT /api/env/{key}` and `PUT /api/sensors/{id}/env/{key}`
(omit `value` to flip only the flag). The flag is stored in the same file as a comment line,
`# hal:plain KEY1 KEY2`, so the file stays plain `KEY=VALUE` text and the marker survives hand edits. A plain value is
still injected into the process, but it is not redacted from stored output, and the env listings return its `value`.
Secret values are never returned. A key written with `value` and without `secret` becomes secret again.

**Email: the `imap-mail` sensor.** The default email sensor reads any IMAP mailbox with the Python 3 standard library
(no packages, no OAuth). It is meant for Gmail with an app password and is configured entirely in its own Environment
section (`IMAP_USER`, `IMAP_PASSWORD`; optional `IMAP_HOST`, `IMAP_PORT`, `IMAP_TLS`, `IMAP_MAILBOX`, `IMAP_SEARCH`,
`IMAP_MAX`; defaults and meaning are in the comment at the top of `sensors/imap-mail.yaml` and in `env.example`).

- Read-only: it opens the mailbox with `EXAMINE` and fetches with `BODY.PEEK`, so nothing is marked read, moved or
  deleted. The default search is `UNSEEN`; after the first successful poll it adds `SINCE` the day before
  `HAL_LAST_SUCCESS_AT`, and dedup drops the overlap. Each poll takes the newest `IMAP_MAX` (default 50) matches.
- Gmail: when the server advertises `X-GM-EXT-1`, `id` is the `X-GM-MSGID` (the dedup key), `thread_id` is the
  `X-GM-THRID`, `labels` are the Gmail labels and `url` opens the message in Gmail. Other servers get `id` =
  `<UIDVALIDITY>:<UID>`, empty `labels` and a null `url`.
- Items: `source`, `id`, `thread_id`, `uid`, `mailbox`, `from` (display string), `from_address`, `to`, `cc`, `subject`,
  `date` (RFC 3339 UTC), `message_id`, `in_reply_to`, `labels`, `flags`, `snippet` (the first 2000 characters of the
  plain-text part, or the HTML part stripped to text), `has_attachments`, `url`, `updated_at` (= `date`). Headers and
  bodies are decoded with their declared charsets; undecodable bytes become U+FFFD.
- Gmail setup: turn on IMAP (Gmail Settings > Forwarding and POP/IMAP), then create an app password (Google account >
  Security > 2-Step Verification > App passwords) and set it as `IMAP_PASSWORD`. A Workspace admin can disable IMAP or
  app passwords; the poll then fails with `AUTHENTICATIONFAILED` in its stderr and only the admin can fix that.
- `IMAP_TLS=none` sends the password in clear and exists for tests and local bridges only. Failures (login, TLS,
  connection, unknown mailbox) exit non-zero with a message that never contains the password.
- Stored output is redacted like any secret env value, so while `IMAP_USER` is secret your own address shows as
  `«IMAP_USER»` in the `to`/`cc`/`from_address` fields of stored events. Mark `IMAP_USER`, `IMAP_HOST`, `IMAP_PORT`,
  `IMAP_TLS`, `IMAP_MAILBOX`, `IMAP_SEARCH` and `IMAP_MAX` plain (they are not sensitive) and the address appears as
  is. Keep `IMAP_PASSWORD` secret.
- `gmail-unread` (the `gog` CLI) remains as an alternative, but it needs Google OAuth, which a Workspace admin can
  block. To use it, change the `email-secretary` flow to subscribe to `gmail-unread` and the prompt's
  `{{.input.snippet}}` to `{{.input.text}}`.

## Flows

```yaml
# flows/triage-github.yaml (abridged; see content/flows/ for the full starters)
enabled: true
dry_run: false
subscribe: [github-notifications]
max_concurrent: 2          # default 2
nodes:
  - {id: only-review-requests, type: command, lang: jq, script: 'select(.reason == "review_requested")'}
  - {id: classify, type: command, lang: python, ports: [small, large], script: "..."}
  - {id: review-small, type: agent, runtime: claude, mode: readonly, prompt: pr-review}
  - {id: note-large, type: inbox, title: "Large PR needs you: {{.input.title}}", priority: high}
edges:
  - {from: only-review-requests, to: classify}
  - {from: classify, port: small, to: review-small}
  - {from: classify, port: large, to: note-large}
```

- The graph must be a DAG. Validation rejects cycles, unknown nodes or sensors, undeclared ports, unreachable nodes,
  duplicate ids and fields that do not belong to the node's type.
- **Entry nodes** (no incoming edge) each receive the event. An edge without `port` uses `out`.
- **Fan-out:** one port can feed several nodes; they run concurrently.
- **Ports:** a `command` node declares `ports` (default `[out]`). A `transform` node's ports come from its route step
  (see [Building flows without code](#building-flows-without-code-transform-nodes)). Every node also has an implicit
  `error` port; agent nodes in `approve` mode also have `denied`. `error` and `denied` cannot be declared. Inbox nodes
  and agent nodes emit on `out`.
- **Error port:** if an edge leaves a node's `error` port, a failed node run is routed there instead of failing the
  branch, with the payload `{"input": <node input>, "error": <class>, "reason": ..., "exit_code": ..., "stderr": ...}`
  (stderr cut to 4000 characters). Test runs never route.
- A flow run fans out once per enabled subscribed flow per event. `max_concurrent` (default 2) limits a flow's parallel
  runs; globally `max_agent_runs` (2) and `max_command_runs` (8) in `config.yaml`. Excess runs wait as `queued`.
- Every flow run records the content hash of the flow YAML; each distinct version is stored, so a trace shows exactly
  what ran.
- `ui: {x, y}` on a node is only its canvas position.

### Templates and the data model

Templates are Go `text/template`. Where they apply: sensor `key`; command `cwd`; agent `cwd.path`, `cwd.repo`,
`cwd.ref`, `extra_prompt` and the prompt file body; inbox `title`, `summary_md`, `suggested_reply` and link `label` /
`url`. Scripts are not templated: they read stdin.

| Name | Value |
|---|---|
| `.event` | The original event payload. |
| `.input` | The input payload of this node (what the upstream node emitted). For entry nodes it equals `.event`. |
| `.flow.id`, `.flow.hash`, `.flow.dry_run` | The flow being run. |
| `.run.id`, `.run.event_id`, `.run.kind` | The flow run; `kind` is `live`, `replay` or `test`. |

Helpers: `json VALUE` (compact JSON, no HTML escaping), `default DEFAULT VALUE`, `trunc N STRING` (first N runes).
`default` and `trunc` take the subject last so they work in pipelines: `{{.input.title | trunc 80}}`.

A missing key is an error (`missingkey=error`), which fails the node with class `template`. Because of that, `default`
only helps for present-but-empty values; for an optional key use `{{default "n/a" (index .input "maybe")}}`.

## Building flows without code (transform nodes)

A `transform` node reshapes, filters and routes items with a list of plain-language steps, so you do not have to write
jq. In the flow editor you add steps from a menu and fill in dropdowns; a preview on a real sample item shows the result
after every edit. The steps compile to a jq program (one `# step N: ...` comment and one line per step) that runs
exactly like a jq `command` node: same timeout, redaction and trace (the trace records the compiled jq as the script).

```yaml
- id: triage
  type: transform
  steps:
    - type: filter                       # drop anything that is not an open, non-bot item
      conditions:
        - {field: state, op: equals, value: open}
        - {field: author.login, op: not_equals, value: dependabot}
    - type: set
      field: summary
      value: {kind: template, template: "{title} by {author.login} ({labels[].name})"}
    - type: aggregate                    # count the comments into comment_count
      field: comments
      into: comment_count
      op: count
    - type: pick
      fields: [id, summary, comment_count, url]
    - type: route                        # the last step may route to different outputs
      default_port: normal
      rules:
        - port: urgent
          match: any
          conditions:
            - {field: 'labels[].name', op: in, value: [p0, p1]}
            - {field: comment_count, op: gte, value: 20}
```

The input is one JSON value (the event item, or what the upstream node emitted). Steps run in order and each one gets
what the previous one produced.

| Step | Does |
|---|---|
| `filter` `{match: all\|any, conditions, keep}` | Keeps items that meet the conditions (`keep: false` drops them instead). A dropped item is recorded as `dropped` with a reason in plain words, for example `Dropped by step 1: it did not meet the condition: state is "open"`. |
| `route` `{rules: [{port, match, conditions}], default_port}` | Must be the last step. The first matching rule picks the output port; items no rule matches go to `default_port` (default `out`). |
| `pick` `{fields, rename, flatten}` | Keeps only these fields. Nested fields stay nested (`author.login` gives `{"author": {"login": ...}}`); with `flatten` the key is `author_login`; `rename: {author.login: who}` makes a top-level `who`. Missing fields are left out. |
| `set` `{field, value}` | Writes `field` (a dot path; nested objects are created). `value.kind` is `constant` (`value`: text, number, true/false, list), `field` (copy another field), `template` (text with `{field.path}` placeholders; `{{` and `}}` are literal braces) or `function` (`function` of `field`: `lowercase`, `uppercase`, `trim`, `length`, `join` and `split` (separator in `arg`), `first`, `last`, `to_number`, `to_string`, `default` (`arg` is the value to use when the field is missing)). |
| `remove` `{fields}` | Deletes fields. |
| `split` `{field, as, keep_parent}` | Turns the list at `field` (`.` means the item itself) into one output item per element. `as` wraps each element as `{as: element}`; `keep_parent` copies the other fields into every item (replacing the list, or adding `as`). An empty or missing list drops the item. |
| `sort` `{field, by, order}`, `limit` `{field, count}`, `unique` `{field, by}` | Sort the list at `field` (by a sub-path, `asc` or `desc`), keep its first `count` elements, or remove duplicates (first occurrence wins, order kept). |
| `aggregate` `{field, into, op, of, group_by}` | Writes a summary of the list at `field` into `into`: `count`, `sum`/`avg`/`min`/`max` of the sub-path `of` (numbers; `min`/`max` accept text such as dates), `collect` the `of` values into a list, or `group_count` by `group_by` (a list of `{value, count}`). |
| `filter_list` `{field, match, conditions, keep}` | Keeps (or with `keep: false` removes) the elements of the list at `field` that meet the conditions, which are written relative to each element (`.` is the element itself). |

A **condition** is `{field, op, value, case_sensitive}`. Operators: `equals`, `not_equals`, `contains`, `not_contains`,
`starts_with`, `ends_with`, `matches` (a regular expression), `gt`, `gte`, `lt`, `lte` (numbers, or text such as ISO
dates), `exists`, `not_exists`, `is_empty`, `is_not_empty`, `is_true`, `is_false`, and `in` (a list of values). The
operators that need a value take one of the matching type; the others take none. Text operators ignore upper/lower case
unless `case_sensitive: true`; `contains` also finds an element in a list of text. A *missing* field is never an error:
`exists` is false, `is_empty` is true and the positive comparisons are false (so the `not_*` ones are true).

**Paths** are dot paths: `author.login`. `[]` means "any element": `labels[].name equals bug` is true when some label is
named bug. `not_*` operators and `is_not_empty` are the exact opposite, so `labels[].name not_equals bug` means *no*
label is named bug (and is true for an item without labels). `.` is the whole item. In `set`, a path with `[]` yields the
list of all matches, which a template or `join` turns into text.

**Ports.** A transform node has `out`, every port named by its route step (rules and `default_port`), and the implicit
`error`. Edges leaving the node use those names; the editor updates the canvas handles as you edit the route. Duplicate
rule ports are a validation error.

**Limits.** `lowercase` and `uppercase` change ASCII letters only. `matches` accepts the common regular-expression
syntax; patterns the Go `regexp` package rejects (look-around, back-references) are reported as invalid. Condition
values are matched as data, never as jq or as patterns (except `matches`).

**In the editor.** The preview takes a sample from recent events (or the upstream node's recent outputs) or pasted JSON,
and shows the item count after each step, the output per port, or why the item was dropped. "Show jq" displays the
compiled program read-only; "Convert to code node" turns the node into an editable jq `command` node with that program
(one-way). Existing jq nodes keep working and can sit in the same flow.

**API.** `POST /api/transform/preview` (`{steps, input}`: the compiled jq, a description per step, the outcome, per-step
counts and diagnostics; nothing is stored), `POST /api/transform/compile` (`{steps}`: the jq and descriptions) and
`GET /api/flows/{id}/samples?node_id=` (recent sample inputs for a node, with where each came from; a node that is not saved yet is sampled like an entry node). Validation errors in
a flow carry paths such as `nodes[2].steps[1].conditions[0].value`.

## Inbox nodes

```yaml
- id: note-large
  type: inbox
  title: "Large PR needs you: {{.input.title}}"
  priority: high            # low | normal | high | urgent; default normal
  summary_md: "{{.input.repo}}: {{.input.lines_changed}} lines changed\n\n{{.input.url}}"
  suggested_reply: ""
  links:
    - {label: Open, url: "{{.input.url}}"}
```

`title` is required and must not render empty. The node creates an inbox item and passes its input through on `out`. In
a dry-run flow the item is tagged `dry-run`. Items at or above `notify_min_priority` raise a macOS notification that
opens the item in the UI. Manage items with `hal9001 inbox list|get|read|star|archive`.

## Approval nodes ("Request approval")

```yaml
- id: confirm-reply
  type: approval
  title: "Send this reply to {{.input.author}}?"
  body: "{{.input.draft}}"            # markdown template, optional
  fields: [author, repo, lines]       # input dot-paths shown as a key/value table, optional
  editable: [draft]                   # input dot-paths the approver may edit before approving, optional
  priority: high                      # low | normal | high | urgent; default normal
  timeout: 24h                        # optional; without it the node waits indefinitely
  on_timeout: denied                  # denied (default) | approved | error; only valid with timeout
```

When a run reaches the node it creates a pending approval of type `request` and parks the node as `awaiting_approval`
(the flow run is `awaiting_approval`). The node holds no concurrency slot and survives daemon restarts. `title` is
required and must not render empty; `title` and `body` are templates over the same data as inbox nodes (`.input`,
`.event`, ...). Someone approves or denies it, with an optional comment, in the Inbox (Needs action) or with
`hal9001 approvals approve|deny <id>`; a macOS notification opens `/inbox/approval/<id>`.

**Ports** `approved`, `denied` and the implicit `error`. On `approved` and `denied` the payload is
`{"input": <node input>, "approval": {"decision": "approved|denied|timeout", "comment": "...", "decided_by": "...", "decided_at": "...", "edited": false}}`.
When the timeout passes, `on_timeout` decides: `denied` or `approved` route to that port with `decision: timeout`,
`error` fails the node (class `approval_timeout`, routed on `error` if wired).

**Edit before approve.** `editable` lists dot-paths into the input (validated: non-empty, no empty segments, no
duplicates). The approval shows each as an editable box prefilled with the current value: a textarea for strings, a JSON
editor for objects, arrays and other values. `POST /api/approvals/{id}/approve` takes optional
`edits: {"<path>": <new value>}`; it is only allowed for `request` approvals and only for the node's declared paths
(422 otherwise). Deny ignores edits. On approve, the payload's `input` has the new values set at those paths, and
`approval.edited` is `true` with `approval.edits: {"<path>": {"before": ..., "after": ...}}` (only paths whose value
changed). The same `edits` appear on the approval (`request.edits`) and so in the approval history. Example: an agent
drafts a Slack reply, the approver fixes the wording, and the command node sends the edited text:

```yaml
nodes:
  - id: draft
    type: agent
    prompt: draft-slack-reply            # outputs {channel, thread_ts, reply}
  - id: review
    type: approval
    title: "Reply in #{{.input.channel}}?"
    editable: [reply]
  - id: send
    type: command
    side_effect: true
    command: [hal-slack-post]            # reads {{.input.input.reply}}: the edited text
edges:
  - {from: draft, to: review}
  - {from: review, port: approved, to: send}
```

`hal9001 approvals approve <id>` approves without edits. The flow editor's "Editable fields" picker (from the sample)
shows the boxes in the preview. Node tests simulate the decision and never apply edits.

**Dry run.** The node still asks, but the approval is tagged `dry_run`, shows a DRY RUN badge and starts with "This is a
dry run: approving or denying only exercises the flow's branches."; the notification title starts with "[dry run]".

**Kill switch.** Approvals can be decided while paused; the downstream nodes queue until the engine resumes.

**Tests.** "Run node on sample" (`POST /api/flows/{id}/test-node`, `hal9001 flows test-node`) and the assist flow test
(`draft_test`, `hal9001 assist test`) never wait: they take the decision `simulate_approval` (`approved` default, or
`denied`; `--simulate-approval`) and create no approval.

## Agent nodes

```yaml
- id: review-small
  type: agent
  runtime: claude          # a key of `runtimes` in config.yaml
  mode: approve            # auto | approve | readonly (required)
  approve: proposal        # proposal | per_tool; only with mode approve; default proposal
  prompt: pr-review        # a library prompt (prompts/pr-review.md) ...
  # instructions: |        # ... or, instead of prompt, the prompt written inline (same template data)
  #   Review {{.input.url}}.
  # output: note           # inline only: note (default) | none
  extra_prompt: "Focus on tests. Title: {{.input.title}}"
  model: sonnet            # optional; set through the agent's `model` config option
  cwd: {mode: worktree, repo: "~/code/{{.input.repository.name}}", ref: "origin/main"}
  timeout: 20m             # default 30m
  approval_timeout: 30m    # how long a call that needs approval waits; default config.yaml approval_timeout (1h)
  allow:
    tools: ['^mcp__linear__']   # tools of the node's mcp_servers the agent may call (hal's own are always allowed)
  mcp_servers:
    - {name: docs, type: http, url: "https://example.com/mcp", headers: {Authorization: "Bearer ..."}}
    - {name: local, command: my-mcp, args: [--stdio], env: {TOKEN: abc}}
  sandbox: my-sandbox      # optional: sandbox backend id; adds fs/shell tools run there
  image: ubuntu:24.04      # optional sandbox options
  repo: "~/code/app"
  ref: main
  tools: [lookup-customer] # optional: custom tool ids exposed through hal's MCP server
```

Every agent session also gets hal's own MCP server (`hal`): `gh_api`, `pr_view`, `pr_diff`, `pr_list`, `issue_view`,
`search`, `now`, `run_context`, `node_input`, `event`. Reads run without a prompt; a call that writes (for example a
`gh_api` POST) becomes a tool-call Approval and the agent waits up to `approval_timeout`. The first prompt of the run starts
with a context block (ids, time, trigger, node input, available tools). See `docs/design/agent-access.md`.

### Modes

Agents have no tools of their own (see [runtimes.md](runtimes.md#lockdown)): they act through hal's MCP tools, and hal
decides each call inside its server. The modes differ in what a write may do.

| Mode | Behavior | Output |
|---|---|---|
| `readonly` | Calls that would change something (for example a `gh_api` POST) fail without asking. Produces a structured note. | Inbox item and notification; port `out`. |
| `auto` | Calls that need approval still ask: hal's approval rules are not bypassed by the mode. | Port `out`; an inbox item only if the prompt has `output: note`. |
| `approve` + `proposal` | Phase 1 is read-only with a suffix asking for a plan; the agent process then exits and the plan becomes an Approval plus a notification. Approving resumes the **same session** (`session/load`; falls back to a new session with the plan in the prompt) with "Approved. Execute the plan." plus your comment. Denying routes to port `denied`. | Port `out` after phase 2. |
| `approve` + `per_tool` | One session; every call that needs approval becomes a tool-call Approval showing the tool and its arguments. Allow runs the call; deny returns a tool error and the agent continues. After `approval_timeout` the call fails with a tool error. | Port `out`. |

A flow run waiting on an approval has status `awaiting_approval`; downstream nodes run after the agent finishes.
Proposal approvals never expire and survive daemon restarts. Tool-call approvals die with the agent process: a restart
marks them `expired` and the run `interrupted`. Decide with `hal9001 approvals approve|deny <id> [--comment ...]` or in
the UI.

### Permission policy and `allow` rules

hal hands the agent its own MCP server (`hal`); everything else the agent asks permission for is rejected and recorded.

| Request | Decision |
|---|---|
| A `hal` tool (`mcp__hal__...` and each runtime's spelling) | Allowed at the agent layer; hal approves inside its server (reads run, writes need approval, read-only runs refuse writes). |
| A tool matching `allow.tools` | Allowed. Use it for the tools of MCP servers you declared in `mcp_servers`. |
| Anything else (a built-in the lockdown missed, an undeclared MCP server) | Rejected, with the reason `lockdown`. |

`allow.tools` entries are regular expressions matched against the tool title or name. There is no `allow.commands`: the
agents have no shell.

### Working directory

| `cwd` | Meaning |
|---|---|
| omitted | `data/state/<flow>/<node>/` |
| `"<path>"` or `{mode: dir, path: "<path>"}` | An existing directory (template; `~` expanded). |
| `{mode: worktree, repo: "<path>", ref: "<ref>"}` | `git fetch` (when the repo has remotes), then `git worktree add --detach data/worktrees/<node-run-id> <ref>`. Removed when the run succeeds; kept when it fails or awaits approval; removed by retention pruning. |

Only agent nodes may use `mode: worktree`.

### MCP servers

`mcp_servers` are passed to the agent session. `name` is required, unique, and not `hal` (reserved for hal's own tools). `type` is `stdio` (default; fields
`command`, `args`, `env`), `http` or `sse` (fields `url`, `headers`).

### Prompts and notes

```markdown
---
name: pr-review          # should match the file name; agent nodes select a prompt by file name
description: Review a PR and leave comments
output: note             # note = the final message carries the inbox note JSON; none = free text
---
You are reviewing {{.input.url}}.
...
```

Give each agent node exactly one of `prompt` (a library prompt) or `instructions` (inline text on the node); both or
neither is a validation error. Inline `instructions` is rendered exactly like a prompt file body, and its `output`
(`note` by default, or `none`) replaces the prompt file's front matter `output`. Use a prompt file when several nodes share
the text; the flow editor's "Save as reusable prompt" turns inline instructions into one. `extra_prompt` works with either.

The prompt body is a template over the data model above. The node's `extra_prompt` (also a template) is appended. The
rendered prompt is stored on the agent run. With `output: note`, or in `readonly` mode, the agent ends its final message
with one JSON object, which becomes the inbox item:

```json
{
  "title": "string",
  "priority": "low | normal | high | urgent",
  "summary_md": "markdown",
  "suggested_reply": "string, optional",
  "links": [{"label": "string", "url": "string"}]
}
```

If parsing fails, an item is still created with the raw final message, priority `normal`, and a `parse_error` flag.
Starter prompts are in `content/prompts/` (`pr-review`, `triage-slack`, `linear-triage`, `email-secretary`).

### What an agent node emits

Downstream nodes receive `{"input": <the agent node's input>, "agent": {"run_id", "status", "final_message", "note",
"proposal", "comment"}}`, for example `{{.input.agent.note.title}}` or `{{.input.input.url}}`. The same shape goes out on
`denied`. Agent failures go to the `error` port (classes include `timeout`, `approval_timeout`, `spawn_failed`,
`agent_exit`, `agent_error`, `mode_unavailable`, `model_rejected`, `policy_rejected`, `empty_proposal`).

## Dry-run, replay and test-node

- **Dry-run.** A flow with `dry_run: true` (or a replay or test run started with dry-run) runs agent nodes forced to
  `readonly`, skips command nodes with `side_effect: true` (recorded as `skipped:dry_run`), sets `HAL_DRY_RUN=1`, and tags
  inbox items `dry-run`. All starter flows except `demo` ship with `dry_run: true`.
- **Replay.** Re-run a stored event through the **current** version of a flow; the new run records `replay_of` and the
  new flow hash:

  ```sh
  hal9001 events replay <event-id> --flow triage-github --dry-run=true
  ```

  A flow configured `dry_run: true` stays dry. Replay is accepted while the kill switch is engaged; the run stays
  `queued` until resume.
- **Test a node.** Run one node on a chosen input without creating timeline noise (the run has `kind: test`, is hidden
  from the default timeline, and its outputs are not routed downstream):

  ```sh
  hal9001 flows test-node triage-github classify --json '{"subject": {"url": "..."}}'
  hal9001 flows test-node triage-github classify --event <event-id>
  hal9001 flows test-node triage-github review-small --node-run <node-run-id>:0
  echo '{"x": 1}' | hal9001 flows test-node triage-github classify
  ```

  Input is exactly one of a stored event, an earlier node run's output (`<id>` or `<id>:<index>`), or JSON. The result is
  the node run with stdin, stdout, stderr, exit code and duration. Rejected with `423` while the kill switch is
  engaged. In the UI this is "Run node on sample" in the flow editor.
- **Follow a run.** `hal9001 runs get <id> --wait` blocks until the run finishes or awaits approval. `runs list --kind
  live|replay|test` filters by kind.
- **Queue.** A run waits in the queue when its flow is at `max_concurrent` (`waiting_for: flow_slot`), or when it is
  running but its only unfinished nodes wait for `max_agent_runs` or `max_command_runs` (`agent_slot`, `command_slot`);
  while the kill switch is engaged every item waits for it. Runs awaiting approval are not queued (see Approvals).
  `hal9001 queue list` shows them in claim order, `queue remove <run-id>` removes one, `queue clear [--flow ID] [--yes]`
  removes all (the CLI asks for confirmation on a terminal). Removing never deletes history: the run and its queued
  nodes end `cancelled` with the reason `removed from queue` and who removed it. A run that already started a node is
  refused with 409 (use the kill switch or wait). `cancelled` counts as finished, not failed, in flow health. In the UI
  this is the Queue tab on the Flows page; the "queued" count in the header links to it.
- **Kill switch.** `hal9001 killswitch pause` pauses sensors and stops starting nodes; `--kill` also terminates running
  nodes (SIGTERM, then SIGKILL after 10s; runs are marked `killed`). `killswitch resume` undoes it. The state survives a
  restart.

## Names, descriptions and renaming

The file name is the id: `sensors/<id>.yaml`, `flows/<id>.yaml`, `tools/<id>.yaml` and `prompts/<name>.md` have no
`id:` (or prompt `name:`) field. An old one is ignored with a warning and dropped on the next write. Sensors and flows
take an optional one-line `description:` that the UI shows under the name.

Rename in the UI (click the name), with `hal9001 sensors|flows|tools|prompts rename <old> <new>`, or
`POST /api/<kind>/{id}/rename` with `{new_id, expected_hash}`. It moves the file and its env file
(`env.d/sensors/<id>.env`, `env.d/tools/<id>.env`), rewrites flows' `subscribe`, agent nodes' `tools` and `prompt`
entries, and moves a sensor's dedup keys, last success, pause and reset state. Recorded events and runs keep the old
id, so a renamed flow's earlier runs stay under the old name.
