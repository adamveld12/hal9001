// Mirror of Go spec.EmitTools / spec.LeadsTo / spec.OutputsSection: the
// "## Outputs" section hal appends to the prompt of an agent node with ports.
import { edgePort, type AgentNode, type Flow, type FlowNode } from "./model";

export interface EmitToolPreview {
  port: string;
  name: string;
  description: string;
  leadsTo: string;
}

const TEMPLATE_ACTIONS = /(\{\{.*?\}\})+/g;
const MAX_LEADS_TO_TITLE = 60;

function quoted(kind: string, title: string): string {
  const t = title
    .replace(TEMPLATE_ACTIONS, "...")
    .split(/\s+/)
    .filter((w) => w !== "")
    .join(" ");
  if (t === "") return kind;
  const chars = [...t];
  const shown =
    chars.length > MAX_LEADS_TO_TITLE
      ? chars.slice(0, MAX_LEADS_TO_TITLE).join("") + "..."
      : t;
  return `${kind} "${shown}"`;
}

/** Describe a downstream node for the agent: its type, and the title of inbox and approval nodes. */
export function leadsTo(n: FlowNode): string {
  switch (n.type) {
    case "inbox":
      return quoted("inbox note", n.title);
    case "approval":
      return quoted("approval", n.title);
  }
  return `${n.type} node`;
}

/** The emit tools of the declared ports that have an outgoing edge, in declaration order. */
export function emitTools(flow: Flow, node: AgentNode): EmitToolPreview[] {
  const out: EmitToolPreview[] = [];
  for (const [port, decl] of Object.entries(node.ports ?? {})) {
    const edge = flow.edges.find(
      (e) => e.from === node.id && edgePort(e) === port,
    );
    const to = edge && flow.nodes.find((n) => n.id === edge.to);
    if (!to) continue;
    out.push({
      port,
      name: `emit_${port}`,
      description:
        (decl.description ?? "").trim() || `Finish through the port ${port}.`,
      leadsTo: leadsTo(to),
    });
  }
  return out;
}

/** The "## Outputs" section, or undefined when no declared port has an edge. */
export function outputsSection(
  flow: Flow,
  node: AgentNode,
): string | undefined {
  const tools = emitTools(flow, node);
  if (tools.length === 0) return undefined;
  const lines = [
    "## Outputs",
    node.emit === "many"
      ? "Finish by calling one or more of these tools (each call is a separate output):"
      : "Finish by calling exactly one of these tools:",
  ];
  for (const t of tools) {
    lines.push(`- ${t.name}: ${t.description}`, `  Leads to: ${t.leadsTo}`);
  }
  lines.push("Do not put the output in your final message; it is ignored.");
  return lines.join("\n");
}
