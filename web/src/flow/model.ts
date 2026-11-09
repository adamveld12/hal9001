import type { NodeToolRef } from "./toolSelection";
// Flow model: the JSON form of the YAML in REQUIREMENTS §6 (snake_case keys,
// identical to the file on disk). The editor edits this model directly; the
// canvas graph is derived from it (see graph.ts).

import { DEFAULT_INSTRUCTIONS } from "./agentPrompt";
import { transformPorts } from "./transform/ports";
import type { TransformNode } from "./transform/types";

export type Lang = "bash" | "python" | "node" | "jq" | "exec";
export const LANGS: readonly Lang[] = ["bash", "python", "node", "jq", "exec"];

export type AgentMode = "auto" | "approve" | "readonly";
export const AGENT_MODES: readonly AgentMode[] = [
  "auto",
  "approve",
  "readonly",
];

export type ApproveStyle = "proposal" | "per_tool";
export const APPROVE_STYLES: readonly ApproveStyle[] = ["proposal", "per_tool"];

export type Priority = "low" | "normal" | "high" | "urgent";
export const PRIORITIES: readonly Priority[] = [
  "low",
  "normal",
  "high",
  "urgent",
];

export interface UIPos {
  x: number;
  y: number;
}

export type DirCwd = { mode: "dir"; path?: string };
export type AgentCwd =
  DirCwd | { mode: "worktree"; repo?: string; ref?: string };

export interface McpServer {
  name: string;
  type?: "stdio" | "http" | "sse";
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  url?: string;
  headers?: Record<string, string>;
}

export interface AllowRules {
  tools?: string[];
}

export interface CommandNode {
  id: string;
  type: "command";
  lang: Lang;
  script?: string;
  command?: string[];
  ports?: string[];
  timeout?: string;
  side_effect?: boolean;
  /** Command nodes accept `dir` only. */
  cwd?: DirCwd;
  ui?: UIPos;
}

export interface AgentNode {
  id: string;
  type: "agent";
  runtime: string;
  mode: AgentMode;
  approve?: ApproveStyle;
  /** Library prompt name; exactly one of `prompt` and `instructions` is set. */
  prompt?: string;
  /** Inline prompt template. */
  instructions?: string;
  /** Inline instructions only: `note` (default) or `none`. */
  output?: "note" | "none";
  extra_prompt?: string;
  model?: string;
  cwd?: AgentCwd;
  allow?: AllowRules;
  mcp_servers?: McpServer[];
  /** Sandbox backend id (adds fs and shell tools). */
  sandbox?: string;
  image?: string;
  repo?: string;
  ref?: string;
  /** Custom tools: an id, or an id with an approval override. */
  tools?: NodeToolRef[];
  timeout?: string;
  approval_timeout?: string;
  ui?: UIPos;
}

export interface InboxNode {
  id: string;
  type: "inbox";
  title: string;
  priority?: Priority;
  summary_md?: string;
  suggested_reply?: string;
  links?: { label: string; url: string }[];
  ui?: UIPos;
}

/** "Request approval": parks the run until a person approves or denies. */
export interface ApprovalNode {
  id: string;
  type: "approval";
  /** Template, required. */
  title: string;
  /** Markdown template. */
  body?: string;
  /** Dot-paths into the node input, shown as a key/value table. */
  fields?: string[];
  /** Dot-paths into the node input the approver may edit before approving. */
  editable?: string[];
  priority?: Priority;
  /** Duration; without it the node waits indefinitely. */
  timeout?: string;
  /** What an expired approval does; only valid with `timeout`. Default denied. */
  on_timeout?: OnTimeout;
  ui?: UIPos;
}

export type OnTimeout = "denied" | "approved" | "error";
export const ON_TIMEOUTS: readonly OnTimeout[] = [
  "denied",
  "approved",
  "error",
];

export type { TransformNode };

export type FlowNode =
  CommandNode | AgentNode | InboxNode | ApprovalNode | TransformNode;
export type NodeType = FlowNode["type"];

export interface FlowEdge {
  from: string;
  /** Output port on `from`; omitted means `out`. */
  port?: string;
  to: string;
}

export interface Flow {
  id: string;
  enabled: boolean;
  dry_run?: boolean;
  subscribe: string[];
  max_concurrent?: number;
  nodes: FlowNode[];
  edges: FlowEdge[];
}

export type Severity = "error" | "warning" | "info";

/** API diagnostic shape; client-side validation produces the same shape. */
export interface Diagnostic {
  code?: string;
  line?: number;
  file?: string;
  path?: string;
  node_id?: string;
  severity: Severity;
  message: string;
}

export const DEFAULT_PORT = "out";
export const ERROR_PORT = "error";
export const DENIED_PORT = "denied";
export const APPROVED_PORT = "approved";

export function edgePort(e: FlowEdge): string {
  return e.port && e.port !== "" ? e.port : DEFAULT_PORT;
}

export function edgeKey(e: FlowEdge): string {
  return `${e.from}|${edgePort(e)}|${e.to}`;
}

/** Ports a node author declares (excluding implicit ones). */
export function declaredPorts(node: FlowNode): string[] {
  switch (node.type) {
    case "command":
      return node.ports && node.ports.length > 0 ? node.ports : [DEFAULT_PORT];
    case "agent":
    case "inbox":
      return [DEFAULT_PORT];
    case "approval":
      return [APPROVED_PORT, DENIED_PORT];
    case "transform":
      return transformPorts(node.steps);
  }
}

/** Ports implicit on every node of this shape (REQUIREMENTS §6). */
export function implicitPorts(node: FlowNode): string[] {
  if (node.type === "agent" && node.mode === "approve") {
    return [ERROR_PORT, DENIED_PORT];
  }
  return [ERROR_PORT];
}

/** All valid output ports: declared first, then implicit. */
export function allPorts(node: FlowNode): string[] {
  return [...declaredPorts(node), ...implicitPorts(node)];
}

export function isImplicitPort(node: FlowNode, port: string): boolean {
  return implicitPorts(node).includes(port);
}

export function nodeById(flow: Flow, id: string): FlowNode | undefined {
  return flow.nodes.find((n) => n.id === id);
}

/** Nodes with no incoming edges: each receives the event (§6). */
export function entryNodeIds(flow: Flow): string[] {
  const targets = new Set(flow.edges.map((e) => e.to));
  return flow.nodes.filter((n) => !targets.has(n.id)).map((n) => n.id);
}

export function emptyFlow(id: string): Flow {
  return {
    id,
    enabled: true,
    dry_run: false,
    subscribe: [],
    nodes: [],
    edges: [],
  };
}

/** Sensible defaults for a node freshly dropped from the palette. */
export function newNode(type: NodeType, id: string, ui: UIPos): FlowNode {
  switch (type) {
    case "command":
      return { id, type, lang: "bash", script: "cat\n", ui };
    case "agent":
      return {
        id,
        type,
        runtime: "claude",
        mode: "readonly",
        instructions: DEFAULT_INSTRUCTIONS,
        output: "note",
        ui,
      };
    case "inbox":
      return { id, type, title: "{{.input.title}}", priority: "normal", ui };
    case "approval":
      return {
        id,
        type,
        title: "Approve {{.input.title}}?",
        priority: "normal",
        ui,
      };
    case "transform":
      return { id, type, steps: [], ui };
  }
}

/** First free id of the form `<base>`, `<base>-2`, ... not in `taken`. */
export function uniqueId(base: string, taken: Iterable<string>): string {
  const used = new Set(taken);
  if (!used.has(base)) return base;
  for (let i = 2; ; i++) {
    const candidate = `${base}-${i}`;
    if (!used.has(candidate)) return candidate;
  }
}
