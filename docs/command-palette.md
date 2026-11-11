# Command palette and keyboard shortcuts

Press <kbd>⌘</kbd>/<kbd>Ctrl</kbd>+<kbd>P</kbd> anywhere in the web UI to open the command palette (the browser's print dialog is suppressed). <kbd>Esc</kbd> closes it and returns focus to where it was. It opens above everything, including the Ask AI drawer, and keeps focus inside while open. <kbd>⌘</kbd>/<kbd>Ctrl</kbd>+<kbd>K</kbd> still toggles Ask AI.

![Palette, empty](images/palette/palette-empty-light.png#gh-light-mode-only)
![Palette, empty](images/palette/palette-empty-dark.png#gh-dark-mode-only)

Type to filter (fuzzy: `gtp` finds "Go to Prompts"), <kbd>↑</kbd>/<kbd>↓</kbd> to move, <kbd>Enter</kbd> to run. Recently used commands come first. Each command shows its keyboard shortcut, or the reason it is unavailable.

## Commands

| Group | Commands |
| --- | --- |
| Go to | Every sidebar page and every Settings section. |
| Create | New flow (puts the cursor in the new-flow id field), New sensor (the blank sensor form), New flow with AI…, New sensor with AI… |
| Chat | Ask AI…, New chat, Open full chat page |
| Actions | Toggle sidebar, Toggle theme, Check for updates, Stop all running flows, Resume after kill switch, Pause all sensors, Resume all sensors |
| Flows, Sensors, Tools, Prompts, Conversations | Jump to an item by name (shown once you type; at most 6 per group) |

Commands marked `…` take text: type the command, a colon, then the text. `ask ai: why did my last run fail` opens the drawer and sends the text as a message. `new sensor with ai: poll my rss feed` starts a sensor draft with it as the first message. On the Chat page, Ask AI sends in the open chat (or a new one).

Dangerous commands always ask first and say what they affect: **Stop all running flows** ("Stops 3 running flows") engages the kill switch and kills running work; **Pause all sensors** names how many sensors it pauses. Commands that cannot run now stay listed, greyed out, with the reason ("No flows are running", "The kill switch is already engaged").

![Kill switch confirmation](images/palette/palette-confirm-light.png#gh-light-mode-only)
![Kill switch confirmation](images/palette/palette-confirm-dark.png#gh-dark-mode-only)

![Jump to items](images/palette/palette-items-light.png#gh-light-mode-only)
![Jump to items](images/palette/palette-items-dark.png#gh-dark-mode-only)

![Palette over the Ask AI drawer](images/palette/palette-drawer-light.png#gh-light-mode-only)
![Palette over the Ask AI drawer](images/palette/palette-drawer-dark.png#gh-dark-mode-only)

## Shortcuts

| Keys | Action |
| --- | --- |
| <kbd>⌘</kbd>/<kbd>Ctrl</kbd>+<kbd>P</kbd> | Command palette |
| <kbd>⌘</kbd>/<kbd>Ctrl</kbd>+<kbd>K</kbd> | Ask AI drawer (toggle) |
| `g` then `i a s f p h t r , c e m o b u n` | Go to Inbox, Approvals, Sensors, Flows, Prompts, Chat, Timeline, Agent runs, General, Context, Environment, Runtimes, Tools, Sandboxes, Auth, Tailscale. (`g c` is Context; MCP also declares `c` and is reached from the palette.) |
| `[` | Toggle the sidebar |
| `j`/`k` and action keys | Inbox and approvals |
| <kbd>⌘</kbd>+<kbd>S</kbd>, <kbd>⌘</kbd>+<kbd>Z</kbd> / <kbd>⇧⌘Z</kbd> | Save, undo and redo in the flow editor |

Plain-key shortcuts are ignored while you type in a field and while the Ask AI drawer or the palette is open.

## For developers

`web/src/lib/commands/` holds the pure parts (command type, registry, fuzzy matching, search, shortcuts). Pages and the shell register commands with `useRegisterCommands(owner, commands)` (`web/src/layout/commands/`). A command's `shortcut` is all the keyboard handler needs: the palette hint and the hotkey come from the same field. The sidebar's nav list (`web/src/layout/nav.ts`) produces the "Go to" commands and `g <key>` shortcuts, so a new page appears in both.
