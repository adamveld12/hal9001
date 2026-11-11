// View-model helpers for the run detail page. Pure.

import type { Schemas } from "../../api/client";
import { durationBetween, formatDuration } from "../../lib/format";

type NodeRun = Schemas["NodeRun"];

export interface RunLabel {
  /** 1-based position among the runs of the same node. */
  index: number;
  total: number;
  /** The upstream run that fed this one, when it is in the list. */
  parent: { nodeId: string; index: number; total: number } | null;
}

/** "#2/3" for fan-out nodes, "#1" otherwise. */
export function indexText(l: { index: number; total: number }): string {
  return l.total > 1 ? `#${l.index}/${l.total}` : `#${l.index}`;
}

/** Position of each node run among its node's runs, and of its parent run. */
export function runLabels(
  nodeRuns: readonly Pick<NodeRun, "id" | "node_id" | "parent_node_run_id">[],
): Map<string, RunLabel> {
  const perNode = new Map<string, string[]>();
  for (const nr of nodeRuns) {
    const ids = perNode.get(nr.node_id) ?? [];
    ids.push(nr.id);
    perNode.set(nr.node_id, ids);
  }
  const byId = new Map(nodeRuns.map((nr) => [nr.id, nr]));
  const position = (nr: Pick<NodeRun, "id" | "node_id">) => {
    const ids = perNode.get(nr.node_id) ?? [];
    return { index: ids.indexOf(nr.id) + 1, total: ids.length };
  };

  const out = new Map<string, RunLabel>();
  for (const nr of nodeRuns) {
    const parent =
      nr.parent_node_run_id === null
        ? undefined
        : byId.get(nr.parent_node_run_id);
    out.set(nr.id, {
      ...position(nr),
      parent: parent ? { nodeId: parent.node_id, ...position(parent) } : null,
    });
  }
  return out;
}

/** Recorded duration, else elapsed time for a started run, else a dash. */
export function runDuration(
  nr: Pick<NodeRun, "duration_ms" | "started_at" | "ended_at">,
  now: number = Date.now(),
): string {
  if (nr.duration_ms !== null) return formatDuration(nr.duration_ms);
  return durationBetween(nr.started_at, nr.ended_at, now);
}

/**
 * Id of the approval a node run is parked on: the engine records it as the
 * reason `awaiting approval <id>`. Null for any other run.
 */
export function awaitedApprovalId(
  nr: Pick<NodeRun, "status" | "reason">,
): string | null {
  if (nr.status !== "awaiting_approval" || nr.reason === null) return null;
  const m = /^awaiting approval (\S+)$/.exec(nr.reason);
  return m ? m[1] : null;
}

export interface Snapshot {
  /** CodeMirror language key input for `langForCommand`. */
  lang: string;
  code: string;
}

/** The script or command a command node ran; null for other node types. */
export function commandSnapshot(node: Schemas["Node"]): Snapshot | null {
  if (node.type !== "command") return null;
  if (node.script !== undefined && node.script !== "") {
    return { lang: node.lang, code: node.script };
  }
  if (node.command && node.command.length > 0) {
    return { lang: "exec", code: node.command.join(" ") };
  }
  return null;
}
