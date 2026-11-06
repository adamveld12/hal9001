// Pure logic of "Run now" and "Reset dedup state" (sensor and flow pages).

export interface RunCounts {
  new_events: number;
  rerun_events: number;
  flow_run_ids: readonly string[];
}

/** "3 new, 2 re-run → 5 flow runs"; "0 new, 0 re-run → no flow runs". */
export function runSummary(r: RunCounts): string {
  const n = r.flow_run_ids.length;
  const runs =
    n === 0 ? "no flow runs" : n === 1 ? "1 flow run" : `${n} flow runs`;
  return `${r.new_events} new, ${r.rerun_events} re-run → ${runs}`;
}

export interface FlowRunNowInput {
  /** The editor has unsaved changes. */
  dirty: boolean;
  subscribeCount: number;
  enabled: boolean;
  killSwitch: boolean;
}

export interface FlowRunNowState {
  disabled: boolean;
  /** Tooltip explaining why the button is disabled. */
  reason?: string;
  /** Shown next to an enabled button. */
  note?: string;
}

export function flowRunNowState(i: FlowRunNowInput): FlowRunNowState {
  if (i.killSwitch) {
    return { disabled: true, reason: "The kill switch is engaged" };
  }
  if (i.dirty) {
    return {
      disabled: true,
      reason: "Save the flow first: Run now runs the saved version",
    };
  }
  if (i.subscribeCount === 0) {
    return { disabled: true, reason: "This flow subscribes to no sensors" };
  }
  if (!i.enabled) {
    return {
      disabled: false,
      note: "This flow is disabled; Run now still runs it once.",
    };
  }
  return { disabled: false };
}

const KEY_PREFIX = "hal9001.runNow.rerunSeen.";

/** Remembered key of a sensor's Run now option; flows use `flow:<id>`. */
export const flowRerunKey = (flowId: string) => `flow:${flowId}`;

type KV = Pick<Storage, "getItem" | "setItem">;

function defaultStorage(): KV | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

/** The remembered "Re-run items already seen" choice; false when unset or storage is unavailable. */
export function loadRerunSeen(
  id: string,
  storage: KV | null = defaultStorage(),
): boolean {
  try {
    return storage?.getItem(KEY_PREFIX + id) === "1";
  } catch {
    return false;
  }
}

export function saveRerunSeen(
  id: string,
  value: boolean,
  storage: KV | null = defaultStorage(),
): void {
  try {
    storage?.setItem(KEY_PREFIX + id, value ? "1" : "0");
  } catch {
    // Storage blocked or full: the choice just is not remembered.
  }
}

export type ResetMode = "reset" | "reset-and-run";

export interface ResetDialogAction {
  label: string;
  body: { run: boolean };
}

/** Buttons of the reset dialog and the request body each one sends. */
export const RESET_ACTIONS: Record<ResetMode, ResetDialogAction> = {
  reset: { label: "Reset", body: { run: false } },
  "reset-and-run": { label: "Reset and run now", body: { run: true } },
};

export const RESET_EXPLANATION =
  "Every item this sensor returns next time counts as new and will trigger its flows again. Events, runs and inbox items already recorded are kept.";

/** "Reset: 12 dedup keys cleared". */
export function resetMessage(keysCleared: number): string {
  return `Reset: ${keysCleared} dedup ${keysCleared === 1 ? "key" : "keys"} cleared`;
}
