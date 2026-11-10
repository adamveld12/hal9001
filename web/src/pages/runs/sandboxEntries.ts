// Pure logic for the sandbox run log: merging incremental fetches and deriving
// the labels the log view shows.

import type { Schemas } from "../../api/client";

export type SandboxSession = Schemas["SandboxSession"];
export type SandboxLogEntry = Schemas["SandboxLogEntry"];

export interface SandboxLogData {
  sessions: SandboxSession[];
  /** Ascending by id. */
  entries: SandboxLogEntry[];
  /** Older entries exist than the ones loaded. */
  hasMore: boolean;
}

/** `id` of the newest entry, or 0 when there is none (the `after_id` cursor). */
export function lastEntryId(entries: readonly SandboxLogEntry[]): number {
  return entries.length === 0 ? 0 : entries[entries.length - 1].id;
}

/**
 * Add entries fetched with `after_id=<last id>` to the loaded ones. The newest
 * response's sessions replace the old ones (counts and status change); entries
 * are deduplicated by id, and a re-sent entry replaces the earlier copy.
 */
export function mergeSandboxLog(
  prev: SandboxLogData | undefined,
  fresh: { sessions: SandboxSession[]; entries: SandboxLogEntry[] },
): SandboxLogData {
  if (!prev) {
    return { sessions: fresh.sessions, entries: fresh.entries, hasMore: false };
  }
  const byId = new Map(prev.entries.map((e) => [e.id, e]));
  for (const e of fresh.entries) byId.set(e.id, e);
  const entries = [...byId.values()].sort((a, b) => a.id - b.id);
  return { sessions: fresh.sessions, entries, hasMore: prev.hasMore };
}

/** The sandbox is still starting or in use, so new entries can arrive. */
export function isLiveSession(s: Pick<SandboxSession, "status">): boolean {
  return s.status === "provisioning" || s.status === "ready";
}

/** Milliseconds from start to ready, or null when it never became ready. */
export function provisioningMs(s: SandboxSession): number | null {
  if (!s.ready_at) return null;
  const ms = Date.parse(s.ready_at) - Date.parse(s.started_at);
  return Number.isNaN(ms) || ms < 0 ? null : ms;
}

export type Denial = "policy" | "user" | "expired" | "other";

/** Why a `denied` entry was not run, from its `approval` text. */
export function denialKind(entry: Pick<SandboxLogEntry, "approval">): Denial {
  const a = (entry.approval ?? "").toLowerCase();
  if (a.startsWith("denied by policy")) return "policy";
  if (a.startsWith("denied by user")) return "user";
  if (a.startsWith("expired")) return "expired";
  return "other";
}

export const DENIAL_LABEL: Record<Denial, string> = {
  policy: "denied by policy",
  user: "denied by user",
  expired: "expired",
  other: "denied",
};

export type ExitTone = "ok" | "bad" | "warn";

/** Badge text and tone for an exec entry's outcome; null when it has no exit code and did not time out. */
export function exitBadge(
  entry: Pick<SandboxLogEntry, "exit_code" | "timed_out">,
): { label: string; tone: ExitTone } | null {
  if (entry.timed_out) return { label: "timed out", tone: "warn" };
  if (entry.exit_code === undefined) return null;
  return {
    label: `exit ${entry.exit_code}`,
    tone: entry.exit_code === 0 ? "ok" : "bad",
  };
}

/**
 * Whether an output block starts expanded: always for a failed command, and
 * when it is the only output the command produced.
 */
export function outputOpen(
  entry: Pick<SandboxLogEntry, "exit_code" | "timed_out" | "stdout" | "stderr">,
  which: "stdout" | "stderr",
): boolean {
  if (
    entry.timed_out ||
    (entry.exit_code !== undefined && entry.exit_code !== 0)
  )
    return true;
  const other = which === "stdout" ? entry.stderr : entry.stdout;
  return !other;
}
