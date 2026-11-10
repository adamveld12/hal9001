# macOS app: manual checklist

Things the automated tests cannot cover (GUI, system prompts, Finder, Settings). Build first: `mise run release`
(outputs `bin/HAL9001.app` and `bin/HAL9001-<version>.dmg`); `mise run dmg:check` verifies the DMG contents and layout.
The window layout is written by `dmgbuild` (pinned, via `uv`) straight into `.DS_Store`; no Finder automation or GUI session is needed.
Use a Mac where no hal9001 agent runs: stop the headless service and remove older launch agents first.

## 1. Install from the DMG

- [ ] Open the DMG. The Finder window shows the red-eye background, the HAL9001 icon on the left and the
      Applications folder on the right, with the arrow between them, no toolbar or sidebar, no stray files.
- [ ] Drag HAL9001 onto Applications. Eject the DMG.
- [ ] First launch from a downloaded copy: Gatekeeper blocks it (ad-hoc signature); right-click > Open > Open works.
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

## 4. Notifications

Use a flow that raises inbox notes and approvals (the seeded `demo` flow does; the approval node tests do too).

- [ ] A note shows title (flow), subtitle and body, grouped per flow. Hover shows **Open**, **Mark read**, **Archive**.
      Clicking the notification opens its page; Mark read / Archive change the item in the UI.
- [ ] A proposal or tool-call approval shows **Approve**, **Deny**, **Approve with comment…**, **Deny with comment…**.
      Each decision shows in the UI, with the comment when you typed one.
- [ ] A request approval with editable fields shows **Approve as-is**, **Deny**, **Open to edit**.
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

## Known quirk

After replacing an app bundle that has a registered service (a rebuild), launchd can refuse to start the daemon
(exit 78) for about two minutes. Wait, then Service > Stop service and Start service, or relaunch the app.
