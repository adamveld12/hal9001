import type { AssistSession, AssistTargetKind } from "../../api/assist";

export interface SessionBinding {
  /** The session the open editor shows and feeds; null when it belongs to the editor of another target. */
  sessionId: string | null;
  /** Id of the other target when the session edits one. */
  otherTarget: string | null;
}

/**
 * Whether `session` belongs to the editor of the target `targetId` (null: the
 * page for a new target) of `kind`. A session started for a new target stays
 * attached after its first Save, when the page moves to the saved target. A
 * sensor session is matched on the target alone; a flow draft names its own
 * id, so a new-flow session belongs to the flow page of that id only. Closed
 * sessions and sessions of another kind leave the editor on its own.
 */
export function bindSession(
  session:
    Pick<AssistSession, "id" | "status" | "target" | "draft"> | undefined,
  kind: AssistTargetKind,
  targetId: string | null,
): SessionBinding {
  const none: SessionBinding = { sessionId: null, otherTarget: null };
  if (!session || session.status === "closed" || session.target.kind !== kind) {
    return none;
  }
  const t = session.target.id;
  const bound: SessionBinding = { sessionId: session.id, otherTarget: null };
  if (kind === "sensor" || kind === "tool") {
    return t === null || t === targetId
      ? bound
      : { sessionId: null, otherTarget: t };
  }
  if (t === targetId) return bound;
  if (t === null && targetId !== null && session.draft.flow_id === targetId) {
    return bound;
  }
  return { sessionId: null, otherTarget: t ?? session.draft.flow_id };
}

export const DEFAULT_DRAWER_WIDTH = 420;
export const MIN_DRAWER_WIDTH = 320;
/** The drawer may take up to this share of the viewport. */
const MAX_DRAWER_SHARE = 0.5;

/** Drawer width between its minimum and half the viewport (the minimum wins in a viewport too narrow for both). */
export function clampDrawerWidth(width: number, viewport: number): number {
  const max = Math.max(
    MIN_DRAWER_WIDTH,
    Math.floor(viewport * MAX_DRAWER_SHARE),
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
