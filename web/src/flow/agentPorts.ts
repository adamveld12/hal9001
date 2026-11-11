// Output ports of an agent node: names, edits and the edge bookkeeping that goes with them.
import {
  edgePort,
  type AgentNode,
  type AgentPort,
  type Diagnostic,
  type Flow,
  type FlowEdge,
} from "./model";

export const PORT_NAME_RE = /^[a-z0-9_]+$/;
const RESERVED_PORTS = ["out", "error", "denied"];

/** Why `name` cannot be a new port name, given the other names on the node; undefined when it is fine. */
export function portNameError(
  name: string,
  others: readonly string[],
): string | undefined {
  if (name === "") return "a port needs a name";
  if (RESERVED_PORTS.includes(name)) {
    return `"${name}" is a reserved port name`;
  }
  if (!PORT_NAME_RE.test(name)) {
    return "port names use lowercase letters, digits and _";
  }
  if (others.includes(name)) return `port "${name}" is declared twice`;
  return undefined;
}

/** The first free port name of the form `port`, `port_2`, ... */
export function freshPortName(existing: readonly string[]): string {
  if (!existing.includes("port")) return "port";
  for (let i = 2; ; i++) {
    if (!existing.includes(`port_${i}`)) return `port_${i}`;
  }
}

function withPorts(node: AgentNode, ports: Record<string, AgentPort>) {
  const { ports: _p, ...rest } = node;
  return Object.keys(ports).length === 0
    ? rest
    : ({ ...rest, ports } as AgentNode);
}

/** Add a port. The first port clears `output`: the result then comes back through the emit tools. */
export function addPort(node: AgentNode, name: string): AgentNode {
  const { output: _o, ...rest } = node;
  const base = Object.keys(node.ports ?? {}).length === 0 ? rest : node;
  return { ...base, ports: { ...node.ports, [name]: {} } };
}

/** Rename a port, keeping its position. A no-op when `to` is taken. */
export function renamePort(
  node: AgentNode,
  from: string,
  to: string,
): AgentNode {
  const ports = node.ports ?? {};
  if (!(from in ports) || from === to || to in ports) return node;
  const next: Record<string, AgentPort> = {};
  for (const [k, v] of Object.entries(ports)) next[k === from ? to : k] = v;
  return { ...node, ports: next };
}

/** Remove a port; the last one also removes `emit`. */
export function removePort(node: AgentNode, name: string): AgentNode {
  const { [name]: _gone, ...rest } = node.ports ?? {};
  const next = withPorts(node, rest);
  if (Object.keys(rest).length > 0) return next;
  const { emit: _e, ...bare } = next;
  return bare;
}

/** Set a port's description and schema; empty values are dropped. */
export function setPort(
  node: AgentNode,
  name: string,
  patch: Partial<AgentPort>,
): AgentNode {
  const cur = { ...node.ports?.[name], ...patch };
  const port: AgentPort = {};
  if (cur.description && cur.description !== "")
    port.description = cur.description;
  if (cur.schema !== undefined) port.schema = cur.schema;
  return { ...node, ports: { ...node.ports, [name]: port } };
}

/** Edges after renaming port `from` of node `id` (an omitted port means `out`). */
export function renamePortEdges(
  edges: readonly FlowEdge[],
  id: string,
  from: string,
  to: string,
): FlowEdge[] {
  return edges.map((e) =>
    e.from === id && edgePort(e) === from ? { ...e, port: to } : e,
  );
}

/** Edges without those leaving port `port` of node `id`. */
export function removePortEdges(
  edges: readonly FlowEdge[],
  id: string,
  port: string,
): FlowEdge[] {
  return edges.filter((e) => !(e.from === id && edgePort(e) === port));
}

/** Declared ports of the agent node that no edge leaves. */
export function unusedPorts(
  node: AgentNode,
  edges: readonly FlowEdge[],
): string[] {
  return Object.keys(node.ports ?? {}).filter(
    (p) => !edges.some((e) => e.from === node.id && edgePort(e) === p),
  );
}

export function unusedPortMessage(port: string): string {
  return `port "${port}" has no outgoing edge, so the agent gets no emit_${port} tool for it; connect it or remove it`;
}

/** Client-side checks of the agent ports of node `index`: names, and ports without an edge. */
export function agentPortDiagnostics(
  node: AgentNode,
  index: number,
  edges: readonly FlowEdge[],
): Diagnostic[] {
  const out: Diagnostic[] = [];
  const names = Object.keys(node.ports ?? {});
  names.forEach((name, i) => {
    const err = portNameError(
      name,
      names.filter((_, j) => j !== i),
    );
    if (err !== undefined) {
      out.push({
        path: `nodes[${index}].ports.${name}`,
        node_id: node.id,
        severity: "error",
        message: err,
      });
    }
  });
  if (names.length > 0 && node.output !== undefined) {
    out.push({
      path: `nodes[${index}].ports`,
      node_id: node.id,
      severity: "error",
      message: `ports cannot be combined with output: ${node.output}`,
    });
  }
  for (const p of unusedPorts(node, edges)) {
    out.push({
      code: "unused_port",
      path: `nodes[${index}].ports.${p}`,
      node_id: node.id,
      severity: "warning",
      message: unusedPortMessage(p),
    });
  }
  return out;
}

/**
 * Whether the inbox node may leave its title empty: it is fed by at least one
 * edge and every edge comes from an agent port without a schema (the emission
 * fills the title).
 */
export function inboxFedBySchemalessPorts(
  flow: Flow,
  inboxId: string,
): boolean {
  const incoming = flow.edges.filter((e) => e.to === inboxId);
  if (incoming.length === 0) return false;
  return incoming.every((e) => {
    const src = flow.nodes.find((n) => n.id === e.from);
    if (!src || src.type !== "agent") return false;
    const port = src.ports?.[edgePort(e)];
    return port !== undefined && port.schema === undefined;
  });
}
