// Run overlay: how a node_run's status/decision maps to what the read-only
// canvas shows (REQUIREMENTS §11, Run detail).

import type { Tone } from "../lib/status";

export type NodeVisual =
  | "emitted"
  | "dropped"
  | "error"
  | "skipped"
  | "running"
  | "awaiting"
  | "denied"
  | "queued"
  | "killed"
  | "interrupted";

export interface NodeOverlay {
  visual: NodeVisual;
  /** Short text under the node id, e.g. the decision reason or "3 runs". */
  detail?: string;
}

export interface RunOverlay {
  nodes: Record<string, NodeOverlay>;
  /** edgeKey()s that carried a payload in this run. */
  takenEdges: ReadonlySet<string>;
}

export const VISUAL_TONE: Record<NodeVisual, Tone> = {
  emitted: "ok",
  dropped: "muted",
  error: "bad",
  skipped: "muted",
  running: "run",
  awaiting: "warn",
  denied: "warn",
  queued: "info",
  killed: "bad",
  interrupted: "warn",
};

export const VISUAL_LABEL: Record<NodeVisual, string> = {
  emitted: "emitted",
  dropped: "dropped",
  error: "error",
  skipped: "skipped",
  running: "running",
  awaiting: "waiting for approval",
  denied: "denied",
  queued: "queued",
  killed: "killed",
  interrupted: "interrupted",
};

/** Higher wins when one node has several node_runs (fan-out). */
const SEVERITY: Record<NodeVisual, number> = {
  error: 9,
  killed: 8,
  interrupted: 7,
  awaiting: 6,
  running: 5,
  queued: 4,
  denied: 3,
  emitted: 2,
  dropped: 1,
  skipped: 0,
};

/** How an approval gate ended a node run that did not run its node. */
export type GateOutcome = "denied" | "timeout";

export const GATE_OUTCOME_TEXT: Record<GateOutcome, string> = {
  denied: "denied",
  timeout: "timed out, treated as denied",
};

/**
 * Set for a node run that finished on port `denied` with error `denied` or
 * `approval_timeout`: it is done, not failed, but its node did not run.
 */
export function gateOutcome(nr: {
  status: string;
  error?: string | null;
  ports?: readonly string[];
}): GateOutcome | null {
  if (nr.status !== "done" || !nr.ports?.includes("denied")) return null;
  if (nr.error === "denied") return "denied";
  if (nr.error === "approval_timeout") return "timeout";
  return null;
}

export function nodeVisual(
  status: string,
  decision?: string | null,
  outcome?: { error?: string | null; ports?: readonly string[] },
): NodeVisual {
  switch (status) {
    case "running":
      return "running";
    case "queued":
      return "queued";
    case "awaiting_approval":
      return "awaiting";
    case "killed":
      return "killed";
    case "interrupted":
      return "interrupted";
    case "failed":
      return "error";
    case "skipped":
    case "cancelled":
      return "skipped";
    default:
      break;
  }
  if (gateOutcome({ status, ...outcome }) !== null) return "denied";
  switch (decision) {
    case "dropped":
      return "dropped";
    case "error":
      return "error";
    case "skipped":
      return "skipped";
    default:
      return "emitted";
  }
}

export function worseVisual(a: NodeVisual, b: NodeVisual): NodeVisual {
  return SEVERITY[b] > SEVERITY[a] ? b : a;
}
