// Builds the read-only canvas overlay of a flow run from the flow version that
// ran and its node_runs (REQUIREMENTS §11, Run detail). Pure.

import type { Schemas } from "../api/client";
import { sensorNodeId } from "./graph";
import { edgeKey, edgePort, entryNodeIds, type Flow } from "./model";
import {
  nodeVisual,
  VISUAL_LABEL,
  worseVisual,
  type NodeOverlay,
  type NodeVisual,
  type RunOverlay,
} from "./overlay";

/** The node_run fields the overlay reads. */
export type OverlayNodeRun = Pick<
  Schemas["NodeRun"],
  | "id"
  | "node_id"
  | "parent_node_run_id"
  | "status"
  | "decision"
  | "reason"
  | "error"
  | "ports"
  | "emitted_count"
>;

/** node_runs grouped by node id, keeping the given (creation) order. */
export function groupByNode<T extends { node_id: string }>(
  nodeRuns: readonly T[],
): Map<string, T[]> {
  const out = new Map<string, T[]>();
  for (const nr of nodeRuns) {
    const list = out.get(nr.node_id);
    if (list) list.push(nr);
    else out.set(nr.node_id, [nr]);
  }
  return out;
}

/** Visual of one node run. */
export function runVisual(nr: OverlayNodeRun): NodeVisual {
  return nodeVisual(nr.status, nr.decision, nr);
}

function singleDetail(
  nr: OverlayNodeRun,
  visual: NodeVisual,
): string | undefined {
  if (nr.reason) return nr.reason;
  if (visual === "error" && nr.error) return nr.error;
  if (visual === "emitted" && nr.emitted_count > 1) {
    return `${nr.emitted_count} emitted`;
  }
  return undefined;
}

const BREAKDOWN_ORDER: readonly NodeVisual[] = [
  "error",
  "killed",
  "interrupted",
  "awaiting",
  "running",
  "queued",
  "denied",
  "emitted",
  "dropped",
  "skipped",
];

/** "3 runs" or, when outcomes differ, "3 runs: 2 emitted, 1 error". */
function manyDetail(visuals: readonly NodeVisual[]): string {
  const head = `${visuals.length} runs`;
  const counts = new Map<NodeVisual, number>();
  for (const v of visuals) counts.set(v, (counts.get(v) ?? 0) + 1);
  if (counts.size === 1) return head;
  const parts = BREAKDOWN_ORDER.filter((v) => counts.has(v)).map(
    (v) => `${counts.get(v)} ${VISUAL_LABEL[v]}`,
  );
  return `${head}: ${parts.join(", ")}`;
}

/** Overlay entry for a node from all its node runs; undefined when none. */
export function overlayForRuns(
  runs: readonly OverlayNodeRun[],
): NodeOverlay | undefined {
  if (runs.length === 0) return undefined;
  const visuals = runs.map(runVisual);
  const visual = visuals.reduce(worseVisual);
  if (runs.length === 1) {
    const detail = singleDetail(runs[0], visual);
    return detail === undefined ? { visual } : { visual, detail };
  }
  return { visual, detail: manyDetail(visuals) };
}

/** Id of the derived sensor -> entry node edge (see deriveSensorEdges). */
export function sensorEdgeId(sensor: string, nodeId: string): string {
  return `${sensorNodeId(sensor)}->${nodeId}`;
}

/**
 * Nodes without a node run are left out of `nodes` (the canvas dims them).
 * An edge is taken when some run of its source node emitted on its port.
 * `sensorId`, when known, marks the derived sensor edges into entry nodes
 * that started.
 */
export function buildRunOverlay(
  flow: Flow,
  nodeRuns: readonly OverlayNodeRun[],
  opts: { sensorId?: string | null } = {},
): RunOverlay {
  const byNode = groupByNode(nodeRuns);

  const nodes: Record<string, NodeOverlay> = {};
  for (const [id, runs] of byNode) {
    const ov = overlayForRuns(runs);
    if (ov) nodes[id] = ov;
  }

  const emittedPorts = new Map<string, Set<string>>();
  for (const nr of nodeRuns) {
    if (nr.ports.length === 0) continue;
    const set = emittedPorts.get(nr.node_id) ?? new Set<string>();
    for (const p of nr.ports) set.add(p);
    emittedPorts.set(nr.node_id, set);
  }

  const taken = new Set<string>();
  for (const e of flow.edges) {
    if (emittedPorts.get(e.from)?.has(edgePort(e))) taken.add(edgeKey(e));
  }

  if (opts.sensorId) {
    for (const id of entryNodeIds(flow)) {
      const started = byNode
        .get(id)
        ?.some((nr) => nr.parent_node_run_id === null);
      if (started) taken.add(sensorEdgeId(opts.sensorId, id));
    }
  }

  return { nodes, takenEdges: taken };
}
