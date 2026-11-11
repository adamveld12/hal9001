// What the chat transcript lists: the messages, with the conversation's
// sandbox log entries between them by time. Pure, so the shaping is tested.

import type { AssistMessage } from "../../api/assist";
import type { SandboxLogEntry } from "../runs/sandboxEntries";

export type ThreadItem =
  | { type: "message"; message: AssistMessage }
  /** Consecutive sandbox entries with no message between them. */
  | { type: "sandbox"; key: string; entries: SandboxLogEntry[] };

function time(ts: string): number {
  const t = Date.parse(ts);
  return Number.isNaN(t) ? 0 : t;
}

/**
 * Messages in order with the sandbox entries placed by their time: an entry
 * goes after the last message created at or before it. Entries that follow
 * each other form one group. Entries before the first message come first.
 * Both inputs are oldest first.
 */
export function threadItems(
  messages: readonly AssistMessage[],
  entries: readonly SandboxLogEntry[],
): ThreadItem[] {
  const out: ThreadItem[] = [];
  let group: SandboxLogEntry[] = [];
  const flush = () => {
    if (group.length === 0) return;
    out.push({
      type: "sandbox",
      key: `sandbox:${group[0]!.id}`,
      entries: group,
    });
    group = [];
  };
  let next = 0;
  const sorted = [...entries].sort(
    (a, b) => time(a.ts) - time(b.ts) || a.id - b.id,
  );
  for (const m of messages) {
    const at = time(m.created_at);
    // Entries older than this message go before it.
    while (next < sorted.length && time(sorted[next]!.ts) < at) {
      group.push(sorted[next]!);
      next++;
    }
    flush();
    out.push({ type: "message", message: m });
  }
  while (next < sorted.length) group.push(sorted[next++]!);
  flush();
  return out;
}

/** A one-line summary of a sandbox group: how many entries and the first title. */
export function sandboxGroupLabel(entries: readonly SandboxLogEntry[]): string {
  const first = entries[0]?.title ?? "";
  if (entries.length === 1) return first;
  return `${entries.length} entries: ${first}`;
}

/** Whether any entry of the group failed or was refused, so the row is marked. */
export function sandboxGroupHasProblem(
  entries: readonly SandboxLogEntry[],
): boolean {
  return entries.some(
    (e) =>
      e.level === "error" ||
      e.kind === "denied" ||
      (e.exit_code !== undefined && e.exit_code !== 0) ||
      e.timed_out === true,
  );
}

const RUN_REF = /\b(run )([0-9A-HJKMNP-TV-Z]{26})\b/g;

export type TextPart =
  { type: "text"; text: string } | { type: "run"; id: string };

/**
 * A divider's or notice's text split at the agent run ids that follow "run ",
 * so each id can link to its agent run page. The text is kept as it is.
 */
export function splitRunRefs(text: string): TextPart[] {
  const parts: TextPart[] = [];
  let last = 0;
  for (const m of text.matchAll(RUN_REF)) {
    const start = m.index + m[1]!.length;
    if (start > last)
      parts.push({ type: "text", text: text.slice(last, start) });
    parts.push({ type: "run", id: m[2]! });
    last = start + m[2]!.length;
  }
  if (last < text.length) parts.push({ type: "text", text: text.slice(last) });
  return parts;
}

/**
 * How a `system` message without a kind looks. In a chat it is an error of the
 * turn. In a flow run's transcript it is a note (the run finished, a proposal
 * was decided, hal sent a follow-up) unless the run did not succeed.
 */
export function systemTone(
  source: "chat" | "flow",
  text: string | null,
): "error" | "note" {
  if (source === "chat") return "error";
  return /^Run (failed|killed|denied|interrupted|timeout)\b/.test(text ?? "")
    ? "error"
    : "note";
}
