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
  awaiting: "awaiting approval",
  queued: "queued",
  killed: "killed",
  interrupted: "interrupted",
};

/** Higher wins when one node has several node_runs (fan-out). */
const SEVERITY: Record<NodeVisual, number> = {
  error: 8,
  killed: 7,
  interrupted: 6,
  awaiting: 5,
  running: 4,
  queued: 3,
  emitted: 2,
  dropped: 1,
  skipped: 0,
};

export function nodeVisual(
  status: string,
  decision?: string | null,
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
