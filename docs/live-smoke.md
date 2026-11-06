# Live smoke checks

Tests in `e2e/live` run the real daemon in process (`daemon.Run` on `127.0.0.1:0`, temp home seeded from `content/`)
against real ACP agents, the real `gh` CLI, and the starter sensors. They spend model tokens and need logins, so they
skip unless `HAL9001_LIVE=1` and never run in hooks (`go test -short` and a plain `go test` skip them). They never touch
port 9001 or `~/.hal9001`.

Results below are from a real run on 2026-11-05 (macOS arm64, claude-agent-acp 0.100.0, codex-acp 2.5.0, omp 18.16.0,
gh 2.73.0).

## Run

```sh
# everything (about 3 minutes)
HAL9001_LIVE=1 mise exec -- go test ./e2e/live -count=1 -v -timeout 40m

# one area
HAL9001_LIVE=1 mise exec -- go test ./e2e/live -run 'TestLiveAgents/claude' -count=1 -v   # one runtime
HAL9001_LIVE=1 mise exec -- go test ./e2e/live -run 'TestLiveRuntimeProbes|TestLiveMissing|TestLiveCrashing' -count=1 -v   # no model calls
HAL9001_LIVE=1 mise exec -- go test ./e2e/live -run 'TestLiveGitHub|TestLiveCredential|TestLiveInstall' -count=1 -v
```

| Variable | Meaning |
|---|---|
| `HAL9001_LIVE=1` | Required; otherwise every live test skips. |
| `HAL9001_LIVE_RUNTIMES=claude,omp` | Limit `TestLiveAgents` and the probes to these runtimes (default: all three). |
| `HAL9001_LIVE_MODEL_CLAUDE` / `_CODEX` / `_OMP` | Model for the node's `model:` (defaults `haiku`, `gpt-6-luna`, `anthropic/claude-haiku-4-5`). |
| `HAL9001_LIVE_EVIDENCE=<dir>` | Write each scenario's API responses (flow run, agent runs, permission requests, approvals, inbox) as JSON files. |
| `HAL9001_LIVE_DEBUG=1` | Daemon logs at debug level on stderr. |

Prerequisites: `mise install` in the repo (installs the pinned adapters), a logged-in `claude`, `codex` and `omp`
(`~/.codex/auth.json`, `~/.omp/agent/agent.db`), `gh auth status` OK with notification and PR read access. No Slack,
Linear or Gmail credentials are needed (those checks expect failure).

How the harness mimics production:

- The PATH handed to nodes and agents is captured from a login shell that starts with launchd's minimal PATH
  (`/usr/bin:/bin:/usr/sbin:/sbin`), not from the mise-managed PATH of the test process, and the process runs from `/`.
- `config.yaml` is the shipped one, except that the two npm adapter commands are replaced by their absolute install paths
  (`mise where npm:@agentclientprotocol/<adapter>` + `/node_modules/.bin/<adapter>`), see [Adapter resolution](#adapter-resolution).
- Starter sensors other than the ones under test are replaced by silent ones; the seeded demo flow is disabled.
- Agent cwd is a per-scenario temp directory (`cwd: {mode: dir, ...}`); the checks look for the file there.

## 1. Real ACP runtimes (`TestLiveAgents`, `TestLiveRuntimeProbes`)

Agents run locked down ([acp-lockdown.md](acp-lockdown.md)): no shell, file or web tools, only hal's MCP tools. Each runtime
uses a fixed input and the cheapest model; the prompts are in `e2e/live/agents_test.go`. The harness builds `cmd/hal9001`
and hands it to the daemon as the binary agents run for `hal9001 mcp`. A scenario passes only if every assertion holds;
evidence is what the API returned.

| Check | claude | codex | omp |
|---|---|---|---|
| Probe (`POST /api/runtimes/{name}/probe`) | pass | pass | pass |
| (a) readonly note: inbox item with parsed title and priority, model applied | pass | pass | pass |
| (b) lockdown: asked to read `/etc/hosts`, run `echo hi` and call hal's `now` | **pass** | **pass** | **pass** |

(b) in detail (2026-11-06, claude-agent-acp 0.100.0, codex-acp 2.5.0, omp 18.17.0, cheapest models, a temp home on a random
port): the prompt asks for A) the first line of `/etc/hosts`, B) the output of `echo hi`, C) the date from hal's `now` tool,
D) the name of every tool the agent has, and answers a note. Passing means: the flow run and agent run succeed; every tool call
in the transcript is a hal MCP call (each has a `hal tool: ...` permission request) and `now` executed (`hal tool: now`,
decision `allow`, outcome `ok`); no permission request for a native tool reached the client; the answer has `NO TOOL` for A
and B, contains no `/etc/hosts` text, and C carries the real date. All three runtimes answered `A: NO TOOL; B: NO TOOL;
C: 2026-11-06T16:32:...` and listed only hal's ten tools (`mcp__hal__now` for claude, `_mcp__hal_now` for omp).

Not part of the live checks anymore: native file-write rejection, proposal resume with a file write, and per-tool edit
approvals. The agents have no write tool; the equivalents are the hal tool approvals (`gh_api` POST, `draft_test`), covered
against the fake agent in `e2e/halmcp_test.go`.

Findings from the lockdown run:

- omp starts its MCP servers asynchronously and answers a prompt that arrives before they are up without their tools
  (reproduced with a freshly built `hal9001`: the model then invented a `bash`/`read` it did not have and fabricated
  results). hal9001 now holds the first prompt of an omp process until `hal9001 mcp` reports that the agent's MCP client has
  initialized (`POST /api/mcp/ready`), and the overlay sets `mcp.startupTimeoutMs` and `tools.xdev: false`.
- A model can still claim results it did not get; the check therefore looks at the transcript and the permission records,
  not at the answer alone.

## 2. Real sensors

`TestLiveGitHubNotifications` (real `gh`, read-only):

1. First poll of `github-notifications` (`first_run: baseline`): `baseline: true`, `item_count` 794-796 (the account's real
   notifications), `new_count: 0`, 0 events, `last_success_at` set. A second poll: `new_count: 0`.
2. A temp copy of the sensor with `first_run: fire` (`github-notifications-fire`) plus a copy of `triage-github` subscribed to it
   (`enabled: true`, `dry_run: true` as shipped; the agent runs in a temp dir with `model: haiku` instead of a `~/code` worktree).
   The copy's filter additionally admits only two chosen notification ids, so the ~800 events fan out but only 2 reach `classify`
   and at most one starts an agent. The poll creates one event per distinct item (`new_count == events`).
3. The test picks, from the real notifications, one review-requested PR under 200 changed lines and one over (sizes from `gh api`),
   and checks routing: the small PR goes `classify -> small -> review-small` and starts one agent run with `mode: readonly`,
   `policy: readonly` (forced by dry-run; `flow_run.dry_run: true`), the large one goes `large -> note-large`; every inbox item
   is tagged `dry_run: true`; a sample of the other events end `succeeded` with `dropped` at the filter. Observed with
   `babylist/web#60666` (194 lines, small) and `#60045` (911 lines, large).
   The small PR review reads the PR through hal's `pr_view` and `pr_diff` tools (the agents have no shell).
4. Not verified: a new notification arriving after the baseline creating exactly one event (cannot create a real
   notification from here); the same dedup path is covered by the engine tests.

`TestLiveCredentialSensorsFailVisibly`: with no credentials, `slack-mentions-dms`, `linear-assigned` and `gmail-unread` each
poll to `status: failed`, `exit_code: 1`, stderr naming the cause (`SLACK_USER_TOKEN is not set - set it in the sensor Environment section of the UI or in the global env file, see env.example`,
`LINEAR_API_KEY is not set - set it in the sensor Environment section of the UI or in the global env file, see env.example`, `gog: command not found ... is gog installed and authorised?`), 0 events,
sensor `state: failing`, `last_success_at: null`. Real polls with credentials (scenario 19) were not run: no Slack, Linear
or Gmail credentials exist here (`gog` is not installed).

## 3. `hal9001 install --dry-run` (`TestLiveInstallDryRunPATH`)

Builds the binary and runs it from a bare environment (`PATH=/usr/bin:/bin:/usr/sbin:/sbin`). The plist has
`RunAtLoad` and `KeepAlive`, `ProgramArguments: [<binary>, serve, --home, ~/.hal9001]`, `WorkingDirectory: ~/.hal9001`, logs under
`~/Library/Logs/hal9001/`, and a `PATH` that starts with the mise tool dirs and `~/.local/share/mise/shims`, followed by
`/opt/homebrew/bin` and the system dirs. With that PATH, from a non-repo directory, `gh`, `jq`, `git`, `node` and `mise` resolve
and run. Run it by hand: `hal9001 install --dry-run`.

## 4. AI-assisted sensor authoring (`TestLiveAssistClaudeAuthorsASensor`)

One real claude turn (model `haiku`) in an assist session ([assist.md](assist.md)): asked to create a bash sensor `live-two`
that emits two static items, test it, and say what the test showed. The agent has no file or shell access; it works through
hal's `draft_read`, `draft_write`, `draft_test` and `validate` tools. Passes only if the turn ends `idle`, the draft validates
(`diagnostics: []`, `updated_by: agent`), `POST .../test` of the draft returns exit 0 with 2 items and rendered dedup keys,
the agent's `draft_test` of its new script was a `tool_call` approval (`hal tool: draft_test`) that the harness allows exactly
once, no other tool call needed a person, and the turn ended with a summary.

```sh
HAL9001_LIVE=1 HAL9001_LIVE_RUNTIMES=claude mise exec -- go test ./e2e/live -run TestLiveAssistClaudeAuthorsASensor -count=1 -v
```

The result recorded for this check predates the lockdown (2026-11-05, with file edits and `hal9001 assist test`); it was not
re-run after the move to draft tools.

## Adapter resolution

The adapters `claude-agent-acp` and `codex-acp` are pinned only in the repo's `mise.toml`. A mise shim picks its version from
the config of the working directory, so from outside the repo (where the daemon and every agent run) the shim fails:

```
mise ERROR No version is set for shim: claude-agent-acp
```

With `command: [claude-agent-acp]` and the login-shell PATH (which contains the shims dir), the runtime probe and every
agent run failed until the adapter was made resolvable; the probe error and the agent run error both end with
`stderr: No version is set for shim: claude-agent-acp`. The shipped `config.yaml` now uses
`command: [mise, exec, "npm:@agentclientprotocol/claude-agent-acp@0.100.0", --, claude-agent-acp]` (and the same for
codex-acp). That form was verified for the claude probe and its full scenario set and for the codex probe, and all three
`runtimes probe` calls passed against a daemon started from `/tmp` with only the login-shell PATH. The test process's own
PATH hides this class of problem because Go's `exec` resolves the command against the daemon's PATH; that is why the live
harness sets the process PATH from the login shell.

Also checked: a runtime whose command does not exist fails the run in milliseconds with
`spawn_failed: exec: "no-such-acp-agent": executable file not found in $PATH` (`TestLiveMissingRuntimeCommand`).

## Findings fixed along the way

- Codex cannot continue after a rejection (its only reject option cancels the turn), so a readonly or propose-phase run
  failed with `policy_rejected` as soon as the agent tried to edit, and no proposal was produced. A turn ended this way by a
  policy rejection is now continued once or twice with a follow-up prompt in the same session (the transcript has a
  `harness` event `recovery_prompt`); a user's deny still ends the run as `denied`.
- Agent run errors for a process that died on its own now include the stderr tail.
