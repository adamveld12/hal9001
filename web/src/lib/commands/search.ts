// What the palette shows for a query: fuzzy-ranked commands grouped into
// sections, recently used first. With nothing typed it shows the recent
// commands and then every group. "ask ai: hello" runs a text-taking command
// with the text after the colon. Pure.

import { fuzzyMatch } from "./fuzzy";
import {
  GROUP_ORDER,
  ITEM_GROUPS,
  type Command,
  type CommandGroup,
} from "./types";

export interface Match {
  command: Command;
  score: number;
  /** Matched positions in the title, for highlighting. Empty when only a keyword matched. */
  indices: number[];
}

export interface Section {
  title: string;
  recent: boolean;
  items: Match[];
}

export interface SearchResult {
  sections: Section[];
  /** The items in display order: what the arrow keys walk. */
  flat: Match[];
  /** Text after the colon for the command that runs on Enter; undefined when there is none. */
  arg: string | undefined;
}

const KEYWORD_WEIGHT = 0.6;
const ITEM_LIMIT = 6;
const RECENT_SHOWN = 5;

/** `head: rest` split at the first colon; null when there is no colon or no text before it. */
export function parseArgument(
  query: string,
): { head: string; arg: string } | null {
  const i = query.indexOf(":");
  if (i <= 0) return null;
  const head = query.slice(0, i).trim();
  if (head === "") return null;
  return { head, arg: query.slice(i + 1).trim() };
}

function scoreCommand(
  query: string,
  c: Command,
): { score: number; indices: number[] } | null {
  const t = fuzzyMatch(query, c.title);
  let best = t && t.score > 0 ? { score: t.score, indices: t.indices } : null;
  for (const k of c.keywords ?? []) {
    const m = fuzzyMatch(query, k);
    if (!m) continue;
    const s = m.score * KEYWORD_WEIGHT;
    if (s > 0 && (best === null || s > best.score)) {
      best = { score: s, indices: [] };
    }
  }
  return best;
}

function recencyBoost(recent: readonly string[], id: string): number {
  const r = recent.indexOf(id);
  return r < 0 ? 0 : Math.max(0, 4 - r * 0.5);
}

function bySections(items: Match[], order: readonly CommandGroup[]): Section[] {
  const sections: Section[] = [];
  for (const group of order) {
    const inGroup = items.filter((m) => m.command.group === group);
    if (inGroup.length > 0) {
      sections.push({ title: group, recent: false, items: inGroup });
    }
  }
  return sections;
}

export function search(
  query: string,
  commands: readonly Command[],
  recent: readonly string[],
): SearchResult {
  let q = query.trim();
  let arg: string | undefined;
  let pool = commands;
  const parsed = parseArgument(q);
  if (parsed) {
    const textual = commands.filter((c) => c.acceptsText === true);
    if (textual.some((c) => scoreCommand(parsed.head, c) !== null)) {
      pool = textual;
      q = parsed.head;
      arg = parsed.arg === "" ? undefined : parsed.arg;
    }
  }

  const finish = (sections: Section[]): SearchResult => ({
    sections,
    flat: sections.flatMap((s) => s.items),
    arg,
  });

  if (q === "") {
    const sections: Section[] = [];
    const shown = new Set<string>();
    const recents: Match[] = [];
    for (const id of recent) {
      const command = pool.find((c) => c.id === id);
      if (command && recents.length < RECENT_SHOWN) {
        recents.push({ command, score: 0, indices: [] });
        shown.add(id);
      }
    }
    if (recents.length > 0) {
      sections.push({ title: "Recent", recent: true, items: recents });
    }
    const rest = pool
      .filter((c) => c.queryOnly !== true && !shown.has(c.id))
      .map((command): Match => ({ command, score: 0, indices: [] }));
    sections.push(...bySections(rest, GROUP_ORDER));
    return finish(sections);
  }

  const ranked: (Match & { order: number })[] = [];
  pool.forEach((command, order) => {
    const m = scoreCommand(q, command);
    if (m) {
      ranked.push({
        command,
        score: m.score + recencyBoost(recent, command.id),
        indices: m.indices,
        order,
      });
    }
  });
  ranked.sort((a, b) => b.score - a.score || a.order - b.order);

  // Sections in the order of their best match; item groups are capped.
  const groups: CommandGroup[] = [];
  for (const m of ranked) {
    if (!groups.includes(m.command.group)) groups.push(m.command.group);
  }
  const capped: Match[] = [];
  const taken = new Map<CommandGroup, number>();
  for (const m of ranked) {
    const n = taken.get(m.command.group) ?? 0;
    if (ITEM_GROUPS.includes(m.command.group) && n >= ITEM_LIMIT) continue;
    taken.set(m.command.group, n + 1);
    capped.push({ command: m.command, score: m.score, indices: m.indices });
  }
  return finish(bySections(capped, groups));
}
