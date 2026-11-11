// Instant client-side validation mirroring the structural rules in
// REQUIREMENTS §6. The server stays authoritative (it validates on save); this
// exists so the canvas can reject a cycle-forming connection while dragging and
// show obvious mistakes without a round trip.

import {
  allPorts,
  edgePort,
  type Diagnostic,
  type Flow,
  type FlowEdge,
  type FlowNode,
  ERROR_PORT,
  DENIED_PORT,
} from "./model";
import { agentPortDiagnostics, inboxFedBySchemalessPorts } from "./agentPorts";
import { validateSteps } from "./transform/validateSteps";

type Pair = Pick<FlowEdge, "from" | "to">;

/**
 * Whether adding `from → to` to `edges` would create a cycle (including a
 * self loop): true when `from` is already reachable from `to`.
 */
export function wouldCreateCycle(
  edges: readonly Pair[],
  from: string,
  to: string,
): boolean {
  if (from === to) return true;
  const next = new Map<string, string[]>();
  for (const e of edges) {
    const list = next.get(e.from);
    if (list) list.push(e.to);
    else next.set(e.from, [e.to]);
  }
  const seen = new Set<string>([to]);
  const stack = [to];
  while (stack.length > 0) {
    const cur = stack.pop() as string;
    if (cur === from) return true;
    for (const n of next.get(cur) ?? []) {
      if (!seen.has(n)) {
        seen.add(n);
        stack.push(n);
      }
    }
  }
  return false;
}

/**
 * Node ids that sit on or are downstream of a cycle, i.e. every node left
 * over when repeatedly removing nodes with no remaining incoming edges
 * (Kahn's algorithm). Empty for a DAG.
 */
export function cycleNodeIds(
  nodeIds: readonly string[],
  edges: readonly Pair[],
): string[] {
  const known = new Set(nodeIds);
  const indegree = new Map<string, number>(nodeIds.map((id) => [id, 0]));
  const next = new Map<string, string[]>();
  for (const e of edges) {
    if (!known.has(e.from) || !known.has(e.to)) continue;
    indegree.set(e.to, (indegree.get(e.to) ?? 0) + 1);
    const list = next.get(e.from);
    if (list) list.push(e.to);
    else next.set(e.from, [e.to]);
  }
  const queue = nodeIds.filter((id) => indegree.get(id) === 0);
  const removed = new Set<string>();
  while (queue.length > 0) {
    const id = queue.shift() as string;
    removed.add(id);
    for (const n of next.get(id) ?? []) {
      const d = (indegree.get(n) ?? 0) - 1;
      indegree.set(n, d);
      if (d === 0) queue.push(n);
    }
  }
  return nodeIds.filter((id) => !removed.has(id));
}

/**
 * Edges that lie on a cycle. Nodes merely downstream of a cycle are excluded:
 * after Kahn's pass, repeatedly dropping nodes with no outgoing edge to the
 * remaining set leaves only nodes that can reach themselves.
 */
export function cycleEdgeKeys(flow: Flow): Set<string> {
  const core = new Set(
    cycleNodeIds(
      flow.nodes.map((n) => n.id),
      flow.edges,
    ),
  );
  for (let changed = true; changed;) {
    changed = false;
    for (const id of [...core]) {
      if (!flow.edges.some((e) => e.from === id && core.has(e.to))) {
        core.delete(id);
        changed = true;
      }
    }
  }
  const keys = new Set<string>();
  for (const e of flow.edges) {
    if (core.has(e.from) && core.has(e.to))
      keys.add(`${e.from}|${edgePort(e)}|${e.to}`);
  }
  return keys;
}

const BAD_ID = /[\s|]/;

function nodeDiagnostics(
  node: FlowNode,
  index: number,
  flow: Flow,
): Diagnostic[] {
  const out: Diagnostic[] = [];
  const at = (
    field: string,
    message: string,
    severity: Diagnostic["severity"] = "error",
  ) =>
    out.push({
      path: `nodes[${index}].${field}`,
      node_id: node.id,
      severity,
      message,
    });

  switch (node.type) {
    case "command": {
      if (node.lang === "exec") {
        if (
          !node.command ||
          node.command.filter((a) => a.trim() !== "").length === 0
        ) {
          at("command", "exec nodes need a command (argv list)");
        }
      } else if (!node.script || node.script.trim() === "") {
        at("script", `${node.lang} nodes need a script`);
      }
      const seen = new Set<string>();
      for (const p of node.ports ?? []) {
        if (p.trim() === "") at("ports", "port names cannot be empty");
        else if (p === ERROR_PORT || p === DENIED_PORT) {
          at("ports", `"${p}" is an implicit port and cannot be declared`);
        } else if (seen.has(p)) at("ports", `port "${p}" is declared twice`);
        seen.add(p);
      }
      break;
    }
    case "agent": {
      if (node.runtime.trim() === "") at("runtime", "an agent needs a runtime");
      const hasPrompt = (node.prompt ?? "").trim() !== "";
      const hasInstructions = (node.instructions ?? "").trim() !== "";
      if (hasPrompt && hasInstructions)
        at("instructions", "set either a saved prompt or inline instructions");
      else if (!hasPrompt && !hasInstructions)
        at("instructions", "write instructions or choose a saved prompt");
      if (
        node.cwd?.mode === "worktree" &&
        (!node.cwd.repo || node.cwd.repo.trim() === "")
      ) {
        at("cwd", "worktree cwd needs a repo");
      }
      out.push(...agentPortDiagnostics(node, index, flow.edges));
      break;
    }
    case "inbox": {
      if (
        node.title.trim() === "" &&
        !inboxFedBySchemalessPorts(flow, node.id)
      ) {
        at("title", "an inbox note needs a title");
      }
      break;
    }
    case "approval": {
      if (node.title.trim() === "") at("title", "an approval needs a title");
      if (node.on_timeout && !node.timeout) {
        at("on_timeout", "on_timeout only applies with a timeout");
      }
      break;
    }
    case "transform": {
      for (const d of validateSteps(node.steps)) {
        out.push({
          ...d,
          path: `nodes[${index}].${d.path}`,
          node_id: node.id,
        });
      }
      break;
    }
  }
  return out;
}

export function validateFlow(flow: Flow): Diagnostic[] {
  const out: Diagnostic[] = [];
  const ids = new Map<string, number>();

  flow.nodes.forEach((n, i) => {
    if (n.id.trim() === "") {
      out.push({
        path: `nodes[${i}].id`,
        severity: "error",
        message: "node id cannot be empty",
      });
    } else if (BAD_ID.test(n.id)) {
      out.push({
        path: `nodes[${i}].id`,
        node_id: n.id,
        severity: "error",
        message: "node ids cannot contain whitespace or |",
      });
    }
    if (ids.has(n.id)) {
      out.push({
        path: `nodes[${i}].id`,
        node_id: n.id,
        severity: "error",
        message: `duplicate node id "${n.id}"`,
      });
    }
    ids.set(n.id, i);
    out.push(...nodeDiagnostics(n, i, flow));
  });

  const byId = new Map(flow.nodes.map((n) => [n.id, n]));
  const seenEdges = new Set<string>();
  flow.edges.forEach((e, i) => {
    const port = edgePort(e);
    const src = byId.get(e.from);
    if (!src) {
      out.push({
        path: `edges[${i}].from`,
        severity: "error",
        message: `edge starts at unknown node "${e.from}"`,
      });
    } else if (!allPorts(src).includes(port)) {
      out.push({
        path: `edges[${i}].port`,
        node_id: src.id,
        severity: "error",
        message: `node "${src.id}" has no port "${port}"`,
      });
    }
    if (!byId.has(e.to)) {
      out.push({
        path: `edges[${i}].to`,
        severity: "error",
        message: `edge ends at unknown node "${e.to}"`,
      });
    }
    if (e.from === e.to) {
      out.push({
        path: `edges[${i}]`,
        node_id: e.from,
        severity: "error",
        message: `node "${e.from}" is connected to itself`,
      });
    }
    const key = `${e.from}|${port}|${e.to}`;
    if (seenEdges.has(key)) {
      out.push({
        path: `edges[${i}]`,
        node_id: e.from,
        severity: "warning",
        message: `duplicate edge ${e.from}.${port} → ${e.to}`,
      });
    }
    seenEdges.add(key);
  });

  const cyc = cycleNodeIds(
    flow.nodes.map((n) => n.id),
    flow.edges.filter((e) => e.from !== e.to),
  );
  if (cyc.length > 0) {
    for (const id of cyc) {
      out.push({
        path: "edges",
        node_id: id,
        severity: "error",
        message: `"${id}" is part of or downstream of a cycle; the graph must be a DAG`,
      });
    }
  }

  if (flow.subscribe.length === 0) {
    out.push({
      path: "subscribe",
      severity: "warning",
      message: "no sensors subscribed; this flow will never run on live events",
    });
  }
  return out;
}

/**
 * Field a diagnostic refers to: the last non-index segment of its path, with
 * the leading `nodes[i]` / `nodes.<id>` selector removed (so a bare node path
 * has no field).
 */
export function diagnosticField(d: Diagnostic): string | undefined {
  if (!d.path) return undefined;
  const rest = d.path
    .replace(/^nodes(\[\d+\]|\.[^.[]+)/, "")
    .replace(/^\./, "");
  if (rest === "") return undefined;
  const segments = rest.split(".");
  for (let i = segments.length - 1; i >= 0; i--) {
    const name = segments[i].replace(/\[\d+\]/g, "");
    if (name !== "") return name;
  }
  return undefined;
}

export interface DiagnosticIndex {
  byNode: Map<string, Diagnostic[]>;
  /** Diagnostics with no node (flow level). */
  flow: Diagnostic[];
}

/**
 * Group diagnostics by node. A diagnostic carrying `path: nodes[i]...` but no
 * `node_id` is attributed through the flow's node order.
 */
export function indexDiagnostics(
  diags: readonly Diagnostic[],
  flow: Flow,
): DiagnosticIndex {
  const byNode = new Map<string, Diagnostic[]>();
  const flowLevel: Diagnostic[] = [];
  for (const d of diags) {
    let id = d.node_id;
    if (!id && d.path) {
      const m =
        /^nodes\[(\d+)\]/.exec(d.path) ?? /^nodes\.([^.[\]]+)/.exec(d.path);
      if (m) id = /^\d+$/.test(m[1]) ? flow.nodes[Number(m[1])]?.id : m[1];
    }
    if (id) {
      const list = byNode.get(id);
      if (list) list.push(d);
      else byNode.set(id, [d]);
    } else {
      flowLevel.push(d);
    }
  }
  return { byNode, flow: flowLevel };
}

export function worstSeverity(
  diags: readonly Diagnostic[] | undefined,
): Diagnostic["severity"] | null {
  if (!diags || diags.length === 0) return null;
  return diags.some((d) => d.severity === "error") ? "error" : "warning";
}

export function hasErrors(diags: readonly Diagnostic[]): boolean {
  return diags.some((d) => d.severity === "error");
}
