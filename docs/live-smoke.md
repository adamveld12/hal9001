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

Each runtime uses a one-line fixed input and the cheapest model; the prompts are in `e2e/live/agents_test.go`. A scenario
passes only if every assertion holds; evidence is what the API returned.

| Check | claude | codex | omp |
|---|---|---|---|
| Probe (`POST /api/runtimes/{name}/probe`) | pass, 0.5 s | pass, 2.7 s | pass, 1.5 s |
| (a) readonly note: inbox item with parsed title and priority, model applied | **pass** (urgent) | **pass** (urgent) | **pass** (urgent) |
| (b) readonly asked to write a file: rejected by policy, file absent | **pass** | **pass** | **pass** |
| (c) approve/proposal: proposal queued, approve over API, file written in the dir cwd, `resumed_via` | **pass**, `load` | **pass**, `load` | **pass**, `load` |
| (d) approve/per_tool: edit becomes a `tool_call` approval, allow applies it | **pass** | not run (see below) | **pass** |

Evidence per runtime:

- (a) `agent_run.status: succeeded`, `stop_reason: end_turn`, `policy: readonly`, `result` parsed (`parse_error: false`),
  inbox item `priority: urgent`, `agent_run_id` set, the transcript has a `harness` event `set_model ... applied: true`.
- (b) one `permission_requests` row, `tool_kind: edit`, `decision: reject`, `decided_by: policy`,
  `matched_rule: policy:readonly`. claude's request is its `Write` tool, codex's is `Edit files`, omp's arrives through
  `elicitation/create` and is recorded the same way. The file does not exist; the agents report the refusal in their final message.
- (c) phase 1 (`propose`, readonly) ends `awaiting_approval` with a non-empty proposal and the file absent; after
  `POST /api/approvals/{id}/approve` phase 2 (`execute`, policy `auto`) has `resumed_via: load`, `resume_fallback: null`,
  and the file contains the requested text.
- (d) one pending approval of type `tool_call`, `tool_kind: edit` (claude `Write`; omp elicitation, title "Creating per-tool.txt ..."),
  file absent until `approve`; the decision is `allow`, `decided_by: user`, `matched_rule: policy:gated`; the read-class
  requests were allowed by policy without asking.

Codex per_tool is not part of this check: the gate (`docs/acp-gate.md`) found its only reject option ends the turn, and the
allow path is covered by the other runtimes. Run it by hand if needed.

Cost: each scenario is one to three short turns on the cheapest model; wall time for the agent scenarios was claude 20 s,
codex 38 s, omp 30 s. Token cost was not measured.

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
   The small PR review used `gh pr view` and `gh pr diff`, allowed by `allow.commands[0]`; a `gh api ...` call was rejected
   by `policy:readonly`.
4. Not verified: a new notification arriving after the baseline creating exactly one event (cannot create a real
   notification from here); the same dedup path is covered by the engine tests.

`TestLiveCredentialSensorsFailVisibly`: with no credentials, `slack-mentions-dms`, `linear-assigned` and `gmail-unread` each
poll to `status: failed`, `exit_code: 1`, stderr naming the cause (`SLACK_USER_TOKEN is not set (see env.example)`,
`LINEAR_API_KEY is not set (see env.example)`, `gog: command not found ... is gog installed and authorised?`), 0 events,
sensor `state: failing`, `last_success_at: null`. Real polls with credentials (scenario 19) were not run: no Slack, Linear
or Gmail credentials exist here (`gog` is not installed).

## 3. `hal9001 install --dry-run` (`TestLiveInstallDryRunPATH`)

Builds the binary and runs it from a bare environment (`PATH=/usr/bin:/bin:/usr/sbin:/sbin`). The plist has
`RunAtLoad` and `KeepAlive`, `ProgramArguments: [<binary>, serve, --home, ~/.hal9001]`, `WorkingDirectory: ~/.hal9001`, logs under
`~/Library/Logs/hal9001/`, and a `PATH` that starts with the mise tool dirs and `~/.local/share/mise/shims`, followed by
`/opt/homebrew/bin` and the system dirs. With that PATH, from a non-repo directory, `gh`, `jq`, `git`, `node` and `mise` resolve
and run. Run it by hand: `hal9001 install --dry-run`.

## Adapter resolution

The adapters `claude-agent-acp` and `codex-acp` are pinned only in the repo's `mise.toml`. A mise shim picks its version from
the config of the working directory, so from outside the repo (where the daemon and every agent run) the shim fails:

```
mise ERROR No version is set for shim: claude-agent-acp
```

With the shipped `command: [claude-agent-acp]` and the login-shell PATH (which contains the shims dir), the runtime probe
and every agent run fail until the adapter is made resolvable. `hal9001` shows this clearly: the probe error and the agent run
error both end with `stderr: No version is set for shim: claude-agent-acp` (before this change the run error omitted stderr).
What the live harness needed: absolute paths to the installed scripts. They start with `#!/usr/bin/env node`, and `node`
came from the login-shell PATH (mise global `node`). Other fixes that work outside the repo: `mise use -g` of both adapters
at the pinned versions, or `command: [mise, exec, "npm:@agentclientprotocol/claude-agent-acp@0.100.0", --, claude-agent-acp]`
(verified for the claude probe and its full scenario set, and for the codex probe). The test process's own PATH hides the problem because Go's `exec` resolves the
command against the daemon's PATH; that is why the harness sets the process PATH from the login shell.

Also checked: a runtime whose command does not exist fails the run in milliseconds with
`spawn_failed: exec: "no-such-acp-agent": executable file not found in $PATH` (`TestLiveMissingRuntimeCommand`).

## Findings fixed along the way

- Codex cannot continue after a rejection (its only reject option cancels the turn), so a readonly or propose-phase run
  failed with `policy_rejected` as soon as the agent tried to edit, and no proposal was produced. A turn ended this way by a
  policy rejection is now continued once or twice with a follow-up prompt in the same session (the transcript has a
  `harness` event `recovery_prompt`); a user's deny still ends the run as `denied`.
- A readonly agent that pipes or redirects (`gh pr view ... 2>&1 | head`) is refused by `allow.commands` on purpose; the
  `pr-review` prompt now tells the agent to run each command bare.
- Agent run errors for a process that died on its own now include the stderr tail.
