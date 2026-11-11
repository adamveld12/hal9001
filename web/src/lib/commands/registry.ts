// The command registry: pages, the sidebar and the shell register their
// commands here under an owner id; the palette lists the result and the
// shortcut handler runs from it. A plain subscribable store (no React) so it
// can be tested and read from event handlers.

import type { Command } from "./types";

type Listener = () => void;

/** The parts of a command that show; `run` is left out so new closures alone do not notify. */
function signature(commands: readonly Command[]): string {
  return JSON.stringify(
    commands.map((c) => [
      c.id,
      c.title,
      c.group,
      c.icon,
      c.subtitle,
      c.keywords,
      c.shortcut,
      c.disabled,
      c.confirm,
      c.acceptsText,
      c.queryOnly,
    ]),
  );
}

export class CommandRegistry {
  private owners = new Map<string, readonly Command[]>();
  private shown: readonly Command[] = [];
  private newest: readonly Command[] = [];
  private sig = "[]";
  private listeners = new Set<Listener>();

  /**
   * Replace the commands of `owner`. Listeners are told only when something
   * visible changed; `run` always takes the newest closure.
   */
  set(owner: string, commands: readonly Command[]): void {
    this.owners.set(owner, commands);
    this.rebuild();
  }

  remove(owner: string): void {
    if (this.owners.delete(owner)) this.rebuild();
  }

  /**
   * What to render: every registered command, owners in registration order.
   * The same array is returned until something visible changes, so its `run`
   * functions may be stale; run commands through `find` or `commands`.
   */
  getSnapshot = (): readonly Command[] => this.shown;

  /** Every command with its newest `run`; for event handlers. */
  commands(): readonly Command[] {
    return this.newest;
  }

  find(id: string): Command | undefined {
    return this.newest.find((c) => c.id === id);
  }

  subscribe = (l: Listener): (() => void) => {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  };

  private rebuild(): void {
    // A shortcut already claimed by an earlier command is dropped from a later
    // one, so the palette only shows the hint that works.
    const claimed = new Set<string>();
    const ids = new Set<string>();
    const out: Command[] = [];
    for (const list of this.owners.values()) {
      for (const c of list) {
        if (ids.has(c.id)) continue;
        ids.add(c.id);
        if (c.shortcut !== undefined && claimed.has(c.shortcut)) {
          out.push({ ...c, shortcut: undefined });
          continue;
        }
        if (c.shortcut !== undefined) claimed.add(c.shortcut);
        out.push(c);
      }
    }
    this.newest = out;
    const next = signature(out);
    if (next === this.sig) return;
    this.sig = next;
    this.shown = out;
    this.listeners.forEach((l) => l());
  }
}
