import type { AssistSession } from "../../api/assist";

/**
 * Sessions of the same target that can be picked up again, newest first.
 * `targetId` null is a new sensor that has no file yet.
 */
export function selectResumable(
  sessions: readonly AssistSession[],
  targetId: string | null,
): AssistSession[] {
  return sessions
    .filter(
      (s) =>
        s.status !== "closed" &&
        s.target.kind === "sensor" &&
        s.target.id === targetId,
    )
    .toSorted((a, b) =>
      a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0,
    );
}

const NEW_SENSOR_PROMPTS = [
  "Poll my GitHub review requests",
  "Watch a Linear project for new issues",
  "Check an RSS feed for new posts",
  "Poll a JSON API I describe",
];

const EXISTING_SENSOR_PROMPTS = [
  "Explain what this sensor does",
  "Make the dedup key more stable",
  "Add error handling to the script",
  "Reduce the polling frequency",
];

/** Suggested first messages for a session on a new sensor (`targetId` null) or an existing one. */
export function starterPrompts(targetId: string | null): string[] {
  return targetId === null ? NEW_SENSOR_PROMPTS : EXISTING_SENSOR_PROMPTS;
}

export const DEFAULT_DRAWER_WIDTH = 420;
export const MIN_DRAWER_WIDTH = 320;
export const MAX_DRAWER_WIDTH = 720;
/** Room that always stays for the editor beside the drawer. */
const EDITOR_MIN = 400;

/** Drawer width within its own limits and leaving the editor room in a viewport of `viewport` px. */
export function clampDrawerWidth(width: number, viewport: number): number {
  const max = Math.max(
    MIN_DRAWER_WIDTH,
    Math.min(MAX_DRAWER_WIDTH, viewport - EDITOR_MIN),
  );
  return Math.round(Math.min(max, Math.max(MIN_DRAWER_WIDTH, width)));
}

/** The saved width, or the default when nothing usable is stored. */
export function parseStoredWidth(raw: string | null): number {
  const n = raw === null ? Number.NaN : Number(raw);
  return Number.isFinite(n) && n > 0 ? n : DEFAULT_DRAWER_WIDTH;
}

/** Whether the agent is working on a turn, so input and editing of the draft by the agent are in flight. */
export function isBusy(status: AssistSession["status"]): boolean {
  return status === "running" || status === "awaiting_approval";
}
