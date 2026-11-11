// State of the global Ask AI drawer: whether it is open, which session it shows
// and what that session edits. A pure reducer plus its sessionStorage form, so
// a reload restores the drawer. The React side is AssistProvider.tsx.

import type { AssistTargetKind } from "../../api/assist";

export interface DrawerTarget {
  kind: AssistTargetKind;
  /** Sensor, flow or tool id; null for a general chat or a target that has no file yet. */
  id: string | null;
}

/** Text for the composer, set by a starter prompt or an entry point. `n` makes each one distinct. */
export interface Seed {
  text: string;
  n: number;
  /** Send the text as a message once the composer can, instead of leaving it to edit. */
  send?: boolean;
}

export interface DrawerState {
  open: boolean;
  /** The session shown; null shows the start view for `target`. */
  sessionId: string | null;
  target: DrawerTarget;
  seed: Seed | null;
}

export const GENERAL_TARGET: DrawerTarget = { kind: "general", id: null };

export const CLOSED_DRAWER: DrawerState = {
  open: false,
  sessionId: null,
  target: GENERAL_TARGET,
  seed: null,
};

export type DrawerAction =
  | { type: "open" }
  | { type: "close" }
  /** A session was created or picked up: show it. `target` defaults to the current one. */
  | { type: "bind"; sessionId: string; target?: DrawerTarget }
  /** The target moved on (a new sensor was saved) while the session stays. */
  | { type: "retarget"; target: DrawerTarget }
  /** Start view for a target (or its session when one is already shown), optionally with composer text. */
  | { type: "openTarget"; target: DrawerTarget; seed?: Seed }
  /** A fresh general chat. The session that was shown is left as it is. */
  | { type: "newChat" }
  | { type: "select"; sessionId: string; target: DrawerTarget }
  /** Set the composer text, or null once the composer took it. */
  | { type: "seed"; seed: Seed | null };

export function sameTarget(
  a: DrawerTarget | null,
  b: DrawerTarget | null,
): boolean {
  return a !== null && b !== null && a.kind === b.kind && a.id === b.id;
}

export function drawerReducer(
  state: DrawerState,
  action: DrawerAction,
): DrawerState {
  switch (action.type) {
    case "open":
      return state.open ? state : { ...state, open: true };
    case "close":
      return state.open ? { ...state, open: false } : state;
    case "bind":
      return {
        open: true,
        sessionId: action.sessionId,
        target: action.target ?? state.target,
        seed: null,
      };
    case "retarget":
      return sameTarget(state.target, action.target)
        ? state
        : { ...state, target: action.target };
    case "openTarget":
      if (state.sessionId !== null && sameTarget(state.target, action.target)) {
        return { ...state, open: true, seed: action.seed ?? null };
      }
      return {
        open: true,
        sessionId: null,
        target: action.target,
        seed: action.seed ?? null,
      };
    case "newChat":
      return {
        open: state.open,
        sessionId: null,
        target: GENERAL_TARGET,
        seed: null,
      };
    case "select":
      return {
        open: true,
        sessionId: action.sessionId,
        target: action.target,
        seed: null,
      };
    case "seed":
      return { ...state, seed: action.seed };
  }
}

export const DRAWER_STORAGE_ITEM = "hal9001.assist.drawer";

const KINDS: readonly AssistTargetKind[] = [
  "general",
  "sensor",
  "flow",
  "tool",
];

/** The stored form: open flag, session and target. Not the seed. */
export function serializeDrawer(state: DrawerState): string {
  return JSON.stringify({
    open: state.open,
    sessionId: state.sessionId,
    target: state.target,
  });
}

/** The stored drawer, or the closed default when nothing usable is stored. */
export function parseDrawer(raw: string | null): DrawerState {
  if (raw === null) return CLOSED_DRAWER;
  let v: unknown;
  try {
    v = JSON.parse(raw);
  } catch {
    return CLOSED_DRAWER;
  }
  if (typeof v !== "object" || v === null) return CLOSED_DRAWER;
  const o = v as Record<string, unknown>;
  const t = o.target;
  if (typeof o.open !== "boolean" || typeof t !== "object" || t === null) {
    return CLOSED_DRAWER;
  }
  const tt = t as Record<string, unknown>;
  const kind = KINDS.find((k) => k === tt.kind);
  const sessionOk =
    o.sessionId === null ||
    (typeof o.sessionId === "string" && o.sessionId !== "");
  const idOk = tt.id === null || (typeof tt.id === "string" && tt.id !== "");
  if (kind === undefined || !sessionOk || !idOk) return CLOSED_DRAWER;
  if (kind === "general" && tt.id !== null) return CLOSED_DRAWER;
  return {
    open: o.open,
    sessionId: o.sessionId as string | null,
    target: { kind, id: tt.id as string | null },
    seed: null,
  };
}

export function loadDrawer(): DrawerState {
  try {
    return parseDrawer(sessionStorage.getItem(DRAWER_STORAGE_ITEM));
  } catch {
    return CLOSED_DRAWER;
  }
}

export function saveDrawer(state: DrawerState): void {
  try {
    sessionStorage.setItem(DRAWER_STORAGE_ITEM, serializeDrawer(state));
  } catch {
    // not persisted
  }
}
