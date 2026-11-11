# macOS app: manual checklist

Things the automated tests cannot cover (GUI, system prompts, Finder, Settings). Build first: `mise run release`
(outputs `bin/HAL9001.app` and `bin/HAL9001-<version>.dmg`); `mise run dmg:check` verifies the DMG contents and layout.
The window layout is written by `dmgbuild` (pinned, via `uv`) straight into `.DS_Store`; no Finder automation or GUI session is needed.
These builds are for the user's own machine and CI; `mise run app` installs nothing unless `HAL9001_APP_INSTALL_DIR` is set (to install the dev copy: `HAL9001_APP_INSTALL_DIR=~/Applications mise run app`).
Use a Mac where no hal9001 agent runs: stop the headless service and remove older launch agents first.

## Updates

HAL9001.app updates itself from signed GitHub releases (CI publishes every main build; see [releases.md](releases.md)).
The daemon decides, the menubar app executes; the daemon never replaces the app bundle.

- **Who updates.** Only the release app (`dev.hal9001`) with a CI build number above 0 and a signing key embedded. A local
  build (build 0), the dev app, a daemon outside an app bundle (`hal9001 serve`, a headless service) and a build without a
  key report `enabled: false` and a `disabled_reason` in `GET /api/update`.
- **Channels.** Setting `update_channel`: `stable` (default) or `beta`, each a signed manifest at
  `https://adamveld12.github.io/hal9001/<channel>.json` (GitHub Pages, `gh-pages` branch; override with `HAL9001_UPDATE_FEED_URL`
  for tests). Every main build goes to `beta`; `stable` gets builds promoted with a `stable/v0.1.N` tag (see [releases.md](releases.md)). A channel without a manifest yet (404) counts as up to date. The daemon checks shortly after start, every 3 hours, on
  `POST /api/update/check` (menu: Check for updates) and when the channel changes. An update is offered only when its
  build number is greater than the running build and the Mac's macOS meets `min_macos`; a downgrade is never offered.
- **Installing.** The menu and the web UI offer **Update available — Install & Restart**. The request goes to the daemon
  (`POST /api/update/install`), which sends the order over `/api/stream` to the app (connected with the header
  `X-Hal9001-Updater: app`). The app downloads the `.app.zip`, verifies size, sha256 and the Ed25519 signature against the
  public key embedded in the app, unpacks it next to the installed app (`.HAL9001-update-<build>/`, same volume), checks
  bundle id, build and code signature, swaps the bundles atomically (`renamex_np`), keeps the old one as
  `.HAL9001-previous.app`, and relaunches. Progress and failures come back through `POST /api/update/report`.
- **Running flows.** A normal request while flow runs are running is **pending** ("installs when flows finish") and the
  order goes out when none runs. **Force install now** exists only in the web UI (behind a confirmation): it orders the
  install at once, and the daemon restart that follows interrupts running runs. The CLI and the menu never force.
- **Automatic.** Setting `update_auto_install` (default off, "Install updates automatically when idle") orders an
  available update once no flow runs, once per build: a failed install is not retried by itself.
- **After the swap** launchd refuses to start a daemon binary whose bundle was just replaced (exit 78) for up to about
  two minutes, see Known quirk. The new app registers the service again and retries for about 4 minutes; the menu shows
  **Updating service… (up to 2 min)** and the web UI is unreachable meanwhile.
- **How to update.** Automatically when idle, if `update_auto_install` is on. Otherwise click **Install & Restart** in the
  web banner or the menu, run `hal9001 update` (state) and `hal9001 update install`, or change both settings under
  Settings > Updates. Headless or without the app: download the DMG from the GitHub Releases page.
- **Headless / no app.** Without the app connected as installer a request is refused with `409` (`no_installer`) and a
  link to download the build by hand; auto-install does not fire. A daemon with no signing key or outside a bundle
  updates by installing a new app manually.
- **Trust.** Manifests are fetched unauthenticated from the public repository; integrity comes from the signature, so a
  tampered feed or download is refused. The app is signed ad hoc, so macOS may still ask for approval where it protects
  app bundles (App Management): a failed swap is reported and the install dir stays as it was.

## 1. Install from the DMG

- [ ] Open the DMG. The Finder window shows the red-eye background, the HAL9001 icon on the left and the
      Applications folder on the right, with the arrow between them, no toolbar or sidebar, no stray files.
- [ ] Drag HAL9001 onto Applications. Eject the DMG.
- [ ] First launch from a downloaded copy: Gatekeeper blocks it (ad-hoc signature). On macOS 15+ right-click > Open no longer bypasses it; open it once, then System Settings > Privacy & Security > "Open Anyway" works.
- [ ] The eye appears in the menubar; there is no Dock icon.

## 2. First launch

- [ ] The system asks to allow notifications. Click **Allow**. (Do not quit the app while the prompt is up: that
      is recorded as a denial.)
- [ ] Within a few seconds the menu shows `Running · <version>` and `Home: ~/.hal9001`; <http://localhost:9001> loads.
- [ ] If you clicked Don't Allow: the menu shows **Notifications are off — Open Settings**; it opens the HAL9001
      pane of System Settings > Notifications (if it only lands on the list, that is the macOS deep link limit).
      Enable notifications there; the item disappears within ~10 s.
- [ ] If an older install exists: the menu offers **Remove older launch agents…** and starts no service until you
      accept.

## 3. Menu

- [ ] Service > Stop service: status changes to Stopped, the daemon stops. Start service: Running again.
- [ ] Service > Restart service: the daemon restarts (new pid in `hal9001 service status` is not applicable; check
      the log or `launchctl print gui/$UID/dev.hal9001.daemon`).
- [ ] Open logs opens `~/Library/Logs/hal9001/hal9001.log`.
- [ ] Launch at login: tick it, check System Settings > General > Login Items lists HAL9001; log out and in; the
      app and daemon are back. Untick it again.
- [ ] Install command-line tool: `~/.local/bin/hal9001` links into the app; the alert says whether
      `~/.local/bin` is on your PATH. `hal9001 status --table` works in a new terminal.
- [ ] **Install the MCP…** (directly below Install command-line tool…) opens the web UI at Settings > MCP in the default
      browser.
- [ ] **hal9001 on GitHub** (above Quit) opens https://github.com/adamveld12/hal9001 in the default browser.

## 4. Notifications

Use a flow that raises inbox notes and approvals (the seeded `demo` flow does; the approval node tests do too).

- [ ] A note shows title (flow), subtitle and body, grouped per flow. Hover shows **Open**, **Mark read**, **Archive**.
      Clicking the notification opens its page; Mark read / Archive change the item in the UI.
- [ ] An approval (proposal, tool call, request) shows the approval's title and description, and **Approve**, **Deny**,
      **Deny…**. **Deny…** opens a text box (steering); the typed text is the denial comment the agent receives.
      Each decision shows in the UI, with the steering when you typed one.
- [ ] A request approval with editable fields shows **Approve as-is**, **Deny**, **Deny…**, **Open to edit**.
- [ ] Approving the same item twice shows a "Could not approve" notification (409); clicking it opens the page.
- [ ] Decide an approval (or read an item) in the web UI: its notification disappears from Notification Center.
- [ ] `high` and `urgent` play a sound; `low` and `normal` are silent. Notifications are not time-sensitive, so a
      Focus mode may hold them back.
- [ ] While the app is the notification sink, `terminal-notifier` does not fire; after Quit it does again
      (`hal9001` falls back when no sink is connected).

## 5. Alerts hint

- [ ] With the style set to **Banners**, the first `urgent` notification adds a menu item "Urgent notifications
      vanish as banners — switch to Alerts…" that opens the settings pane. It shows once, for that app session.

## 6. Quit, headless

- [ ] Quit from the menu: the eye disappears, the daemon keeps running (flows still fire, UI still loads).
- [ ] Reopen the app: it reconnects; Stop service was not undone by relaunching.
- [ ] Headless: with the app's service stopped and the app quit, `hal9001 service install` starts the daemon;
      `hal9001 service status` shows it loaded and answering; opening the app now shows "A headless service runs the
      daemon" and registers no service of its own. `hal9001 service uninstall` removes it; Start service works again.
- [ ] `hal9001 service logs -n 20` prints the daemon log.

## 7. Updates

Needs a release app of a CI build in `~/Applications` or `/Applications` (a local build has build number 0 and never
updates) and a newer build on the feed of its channel.

- [ ] While an update is available the menu shows **Update available: <version> — Install & Restart**, and **Check for
      updates** sits next to Open logs.
- [ ] Install & Restart with no flow running: the line becomes **Installing update… (downloading 40%)**, then
      **(verifying)**; the app quits and opens again by itself within a few seconds.
- [ ] After the relaunch the menu shows **Updating service… (up to 2 min)** while the daemon is down, then **Running ·
      <new version>** (up to about 4 minutes, see Known quirk). Launch at login is still ticked and notifications still
      arrive.
- [ ] With a flow run in progress, Install & Restart changes the line to **Update <version> installs when flows
      finish**; once the run ends the install starts by itself.
- [ ] Check for updates: nothing changes while up to date; a newer build published on the feed shows up as the update
      item.
- [ ] After an update `.HAL9001-previous.app` sits next to the app (hidden, see it with `ls -a`) and holds the old
      version; no `.HAL9001-update-*` folder is left behind. A second update replaces it.
- [ ] A failed update: run the app from the mounted DMG (or from a folder you cannot write to) and choose Install &
      Restart. An alert says the folder is not writable and to download the update manually from the releases page; the
      menu shows **Update failed: …** with Install & Restart again, and the app keeps running.
- [ ] The dev app (HAL9001 Dev) never shows update items.
- [ ] Web UI: while an update is available a banner above every page reads **Update available: HAL9001 <version>** with
      **Release notes** and **Install & Restart**; **Dismiss** hides it for that version (it returns for the next).
- [ ] Web UI with a flow run in progress: Install & Restart turns the banner into **Update <version> will install when N
      running flow(s) finish** with **Force install now**; Force asks "Running flows will be interrupted" and only
      installs after you confirm. While installing the banner reads **Installing update (…)… HAL9001 will restart.**
- [ ] Web UI with the app quit: the banner shows the hint to open the app or download the build; Install & Restart
      shows the refusal and a link to the release page.
- [ ] Settings > Updates shows version/build, channel select, the auto-install switch, state, last checked and the
      latest version with notes; **Check now** refreshes them. On a dev or local build it shows why updates are off.
- [ ] `hal9001 update` prints the same state; `hal9001 update install` prints "pending until flows finish" or
      "installing" and never interrupts running flows.

## Known quirk

After replacing an app bundle that has a registered service (a rebuild), launchd can refuse to start the daemon
(exit 78) for about two minutes. Wait, then Service > Stop service and Start service, or relaunch the app. After an
update the app itself retries registering the service every 30 seconds for about 4 minutes (the menu says **Updating
service…** meanwhile).
