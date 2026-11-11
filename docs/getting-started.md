# hal9001: what's new and how to install

## Install the Mac app

Build:

```sh
mise run setup     # first time only
mise run release   # -> bin/HAL9001.app and bin/HAL9001-<version>.dmg
```

Install:

1. Stop old setups first (only if you had them): any `bin/hal9001 serve` running by hand, a headless service
   (`hal9001 service uninstall`), and the old menubar app. If the app still finds legacy launch agents, it offers
   **Remove older launch agents…** and starts no service until you accept.
2. Open the DMG and drag HAL9001 onto Applications. It must live in Applications, or macOS won't let it send
   notifications. A downloaded copy is ad-hoc signed, so Gatekeeper blocks the first launch (macOS 15+: right-click > Open
   no longer works). Try to open it once, then System Settings > Privacy & Security > **Open Anyway**.
3. Launch HAL9001. The eye appears in the menubar; there's no Dock icon.
4. Click **Allow** when macOS asks about notifications. Don't quit while the prompt is open, or macOS records
   "denied". If that happens, the menu shows **Notifications are off — Open Settings**.
5. From the menu: turn on **Launch at login**; optionally **Install command-line tool…** (links
   `~/.local/bin/hal9001`); optionally switch HAL9001 to the Alerts style in System Settings → Notifications so urgent
   items stay on screen.
6. Open <http://localhost:9001> (opens on the Inbox).

What runs where: the daemon is a background service owned by the app; it keeps running if you quit the menubar and
restarts if it crashes. Data stays in `~/.hal9001`.

Headless machines (no menubar, e.g. a Mac mini): `hal9001 service install | uninstall | status | logs`.

Development: `mise run dev` (hot-reload UI on :5173, daemon auto-restart), `mise run build:all`,
`mise run sandbox` (throwaway instance on a free port).

Manual checklist for what tests can't cover: [macos-app.md](macos-app.md). Authentication: [auth.md](auth.md).

## The Mac app

- Menubar: HAL eye with a count of unread items plus pending approvals; throbs slowly while flows run. Menu: Inbox (N),
  Approvals (M); Flows and Sensors submenus ("All flows" / "All sensors" at top, active run counts per flow, paused
  sensors marked); Settings; a Service submenu (Start / Stop / Restart service), Open logs, Launch at login,
  Install command-line tool…, Install the MCP… (opens Settings → MCP), hal9001 on GitHub, Quit.
- Native notifications with the HAL icon: title = flow, subtitle = item/approval/request/tool call, body = first
  lines; grouped per flow, dry runs labelled, high/urgent play a sound. Buttons: notes Open / Mark read / Archive;
  approvals Approve / Deny / Deny… (opens a text box for additional steering); editable requests Approve as-is / Deny /
  Open to edit. A notification disappears once handled elsewhere. Without the app, terminal-notifier takes over. Log
  at `GET /api/notifications`; `notify_via` (auto/app/terminal-notifier/none).
- DMG installer: drag-to-Applications window with eye-and-arrow background, built without Finder;
  `mise run dmg:check` verifies it.

### How to update

The release app updates itself from GitHub releases. With "Install updates automatically when idle" on (Settings >
Updates) it installs once no flow is running. Otherwise click **Install & Restart** in the web UI banner or the menu, or
run `hal9001 update install`; `hal9001 update` shows the state. Without the app (headless), download the new DMG from the
[Releases page](https://github.com/adamveld12/hal9001/releases). Details: [docs/macos-app.md](macos-app.md#updates).

## Agents: all access goes through hal9001

Details: [design/agent-access.md](design/agent-access.md), [runtimes.md](runtimes.md). hal9001 detects the `claude`, `codex` and `omp` CLIs and your sign-in (Settings > Runtimes); for Claude and Codex click **Install adapter** once (needs Node.js 22+ and npm).

- Lockdown: built-in file/shell/web tools off on claude, codex and omp; every action is a hal9001 tool call decided
  by policy or by you in the Inbox, recorded in the trace.
- Every agent has: GitHub via gh (reads automatic, writes need approval), now, run_context, node_input, event.
- Every prompt starts with run context (flow, time, trigger, node input).
- Sandboxes (Configure → Sandboxes: Local, Docker, Depot, Kubernetes, SSH): give an agent node fs_* and shell tools
  inside the sandbox, with per-sandbox approval rules and env vars. Local is always on with no setup; Depot needs its
  token. A run's sandbox log (provisioning, every command with output and exit code, denied calls, teardown) shows
  under "Sandbox" on the agent run and node run pages. After a run ends its sandbox stays for the backend's Keep alive
  setting (default 1m, 0 removes it at once) so a chat can continue in it.
- Custom tools (Configure → Tools): YAML bash/python/node scripts with params, approval rules and a "runs in sandbox"
  option; attach to agent nodes; test runner and Create with AI.
- Agent node: Sandbox dropdown with status, Image, Repo and Ref fields, Tools picker, inline prompts (no prompt file
  needed).

## Flows

Details: [flows.md](flows.md).

- Transform node: filter, route, reshape and summarize with plain-language steps instead of jq; live preview on real
  samples.
- Request approval node: waits for your decision, continues down `approved` or `denied` with your comment; optional
  timeout; dry runs still ask and are marked; editable fields let you change e.g. a drafted reply before approving.
- Run now on sensors and flows (option to re-run items already seen); manual-only sensors; Reset dedup state.
- Queue view (Flows → Queue): remove one, several or all; removed runs are `cancelled` and stay in history.
- Runs tab on Flows, per-flow Runs tab, Agent runs page in the sidebar.

## MCP server for your other agents

Details: [mcp.md](mcp.md).

- hal9001 is an MCP server on `http://localhost:9001/mcp` (Streamable HTTP) and as a stdio command (`hal9001 mcp`), for
  Claude Code, Codex, omp (Oh My Pi) and pi. Settings → MCP shows the URL, the binary path of the running app and
  copy-paste install commands for each client.
- Tools: inbox, approvals, flows, runs, sensors and events (read); `flow_run`, `sensor_poll`, `flow_create` and
  `sensor_create` wait for your approval in the Inbox; `request_approval` asks you for a decision and `send_note` leaves a
  note. No tool can approve or deny. Loopback only, same auth as the API.

## Chat assistant

Details: [assist.md](assist.md).

- **Ask AI** in the header of every page (<kbd>⌘</kbd>/<kbd>Ctrl</kbd>+<kbd>K</kbd>): one drawer that stays open while you
  move between pages, knows which page you are on, can open pages for you and start sensor, flow and tool drafts. `New chat`
  (or `/new`) starts a fresh chat; `History` goes back to an earlier one. Sensors, Flows and Tools keep "Create with AI" as a
  shortcut. On Flows it can also draft/edit sensors and prompts and save everything in one step.
- Knows your sandboxes, runtimes, tools and settings, so it doesn't guess ids.
- Knows what you put in Settings → Context → System Prompt (your name, preferences, facts). That text is added to every agent call, flows included.
- Fixes: kill switch doesn't block it; errors are shown; typing works immediately; Save disabled until the
  placeholder sensor is changed.

## Inbox and UI

- Inbox is the home page; pending approvals at the top under "Needs action"; notifications link to the exact item.
- Bulk clear: archive by preset (read, dry-run, low priority, older than 7 days, everything shown) with undo; starred
  items kept.
- Collapsible icon sidebar (`[`) grouped Notify / Automate / Observe / Configure; chat drawer up to 50% width; sensor
  output as readable tables.
- Rename sensors/flows/tools/prompts by clicking the name (references update); file name is the id; sensors and flows
  have descriptions.

## Other changes

- Starters only seeded into a brand-new home; `hal9001 starters add` restores one.
- IMAP sensor for Gmail with an app password.
- Env vars can be marked plain (not masked).
- Faster dev loop: `mise run check` ~30s; pre-commit formats files itself; pre-push runs e2e + web check; race
  detector opt-in (`mise run race`).

## Known issues

- Notification permission is only requested once; if denied, re-enable it in System Settings.
- Urgent notifications can't break through Focus without an Apple Developer ID; use the Alerts style.
- After rebuilding the app, the daemon may refuse to start for ~2 minutes; use Service > Stop service, then
  Start service.
- Occasionally flaky e2e tests make a push need a retry.
- Depot and Kubernetes sandboxes haven't run against real systems, so their provisioning logs are untested there.
- Notification buttons haven't been clicked by a person yet (on the checklist).
