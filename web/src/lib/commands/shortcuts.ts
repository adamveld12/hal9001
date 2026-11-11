// Keyboard shortcuts of commands: parsing, matching and display. Pure; the
// listeners that call these live in layout/commands/CommandProvider.tsx.

import type { Command } from "./types";

export interface KeyLike {
  key: string;
  metaKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}

/** Cmd (macOS) or Ctrl plus `key`, nothing else held. */
export function isModKey(e: KeyLike, key: string): boolean {
  return (
    e.key.toLowerCase() === key &&
    (e.metaKey || e.ctrlKey) &&
    !e.altKey &&
    !e.shiftKey
  );
}

/** The key of a `mod+<key>` shortcut, or null for a plain one. */
export function modKeyOf(shortcut: string): string | null {
  return shortcut.startsWith("mod+") ? shortcut.slice(4).toLowerCase() : null;
}

/** The keys of a plain shortcut ("g i" is ["g", "i"]). */
export function sequenceOf(shortcut: string): string[] {
  return shortcut.split(" ").filter((k) => k !== "");
}

export type KeyResolution =
  /** A command to run; no sequence is pending after it. */
  | { kind: "run"; command: Command }
  /** The key starts a sequence; wait for the next one. */
  | { kind: "pending"; first: string }
  | { kind: "none" };

/**
 * What a plain key does: `pending` is the first key of a sequence that was
 * started. The first command registered for a shortcut wins.
 */
export function resolveKey(
  commands: readonly Command[],
  pending: string | null,
  key: string,
): KeyResolution {
  const plain = commands.filter(
    (c) => c.shortcut !== undefined && modKeyOf(c.shortcut) === null,
  );
  if (pending !== null) {
    const hit = plain.find((c) => {
      const keys = sequenceOf(c.shortcut ?? "");
      return keys.length === 2 && keys[0] === pending && keys[1] === key;
    });
    return hit ? { kind: "run", command: hit } : { kind: "none" };
  }
  const single = plain.find((c) => c.shortcut === key);
  if (single) return { kind: "run", command: single };
  if (
    plain.some(
      (c) => sequenceOf(c.shortcut ?? "")[0] === key && c.shortcut !== key,
    )
  ) {
    return { kind: "pending", first: key };
  }
  return { kind: "none" };
}

/** The key caps to show for a shortcut: `["⌘", "K"]`, `["g", "i"]`, `["["]`. */
export function shortcutKeys(shortcut: string, mac: boolean): string[] {
  const mod = modKeyOf(shortcut);
  if (mod !== null) return [mac ? "⌘" : "Ctrl", mod.toUpperCase()];
  return sequenceOf(shortcut);
}

/** The same as one string for titles and docs: "⌘K", "g i". */
export function shortcutLabel(shortcut: string, mac: boolean): string {
  const mod = modKeyOf(shortcut);
  if (mod !== null) return `${mac ? "⌘" : "Ctrl+"}${mod.toUpperCase()}`;
  return sequenceOf(shortcut).join(" ");
}
