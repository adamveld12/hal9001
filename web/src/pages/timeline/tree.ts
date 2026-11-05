import type { EventDetail } from "../../api/events";

type Branch = EventDetail["fan_out"][number];
type TreeNode = Branch["node_runs"][number];

export interface PathStep {
  nodeRunId: string;
  nodeId: string;
  nodeType: string;
  status: string;
  /** Null until the node run has finished. */
  decision: string | null;
  reason: string | null;
  /** Nesting depth below the entry node runs. */
  depth: number;
  /** Ports the node emitted on, for routing nodes. */
  ports: string[];
  emittedCount: number;
}

/** Depth-first flattening of a branch's node-run trees, in execution-tree order. */
export function flattenBranch(nodeRuns: readonly TreeNode[]): PathStep[] {
  const out: PathStep[] = [];
  const walk = (t: TreeNode, depth: number) => {
    const n = t.node_run;
    out.push({
      nodeRunId: n.id,
      nodeId: n.node_id,
      nodeType: n.node_type,
      status: n.status,
      decision: n.decision,
      reason: n.reason,
      depth,
      ports: n.ports,
      emittedCount: n.emitted_count,
    });
    for (const c of t.children) walk(c, depth + 1);
  };
  for (const t of nodeRuns) walk(t, 0);
  return out;
}

/** Compact one-line decision path, e.g. `filter:emitted → triage:emitted → notify:skipped`. */
export function decisionPathText(steps: readonly PathStep[]): string {
  return steps.map((s) => `${s.nodeId}:${s.decision ?? s.status}`).join(" → ");
}

/** Counts of finished decisions in a branch, for a summary line. */
export function decisionCounts(
  steps: readonly PathStep[],
): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const s of steps) {
    const k = s.decision ?? s.status;
    counts[k] = (counts[k] ?? 0) + 1;
  }
  return counts;
}
