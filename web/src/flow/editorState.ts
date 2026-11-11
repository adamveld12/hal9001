// Pure reducer behind the flow editor. The canvas graph is the working copy;
// `serialize` turns it back into the model for validation, dirty checks, and
// save. History snapshots cover structural edits and whole drags (not every
// intermediate drag position).

import {
  applyEdgeChanges,
  applyNodeChanges,
  type Connection,
  type EdgeChange,
  type NodeChange,
} from "@xyflow/react";
import {
  deriveSensorEdges,
  flowToGraph,
  graphToFlow,
  isFlowNode,
  isSensorNode,
  IN_HANDLE,
  sensorNodeId,
  flowEdgeToGraphEdge,
  type FlowGraphNode,
  type FlowMeta,
  type Graph,
  type GraphEdge,
  type GraphNode,
} from "./graph";
import {
  allPorts,
  newNode,
  uniqueId,
  type Flow,
  type FlowNode,
  type NodeType,
  type UIPos,
} from "./model";
import { removePort, renamePort } from "./agentPorts";
import { wouldCreateCycle } from "./validate";

interface Snapshot {
  meta: FlowMeta;
  nodes: GraphNode[];
  edges: GraphEdge[];
}

export interface EditorState extends Snapshot {
  past: Snapshot[];
  future: Snapshot[];
  dragging: boolean;
  /** JSON of the last loaded/saved flow, for dirty detection. */
  baseline: string;
  /** Key of the field edited by the last `updateNode`, while that run continues. */
  coalesce?: string;
}

const HISTORY_LIMIT = 100;

export type EditorAction =
  | { type: "load"; flow: Flow }
  | { type: "nodeChanges"; changes: NodeChange<GraphNode>[] }
  | { type: "edgeChanges"; changes: EdgeChange<GraphEdge>[] }
  | { type: "connect"; connection: Connection }
  | { type: "addNode"; nodeType: NodeType; position: UIPos }
  | { type: "addSensor"; sensor: string; position: UIPos }
  | {
      type: "updateNode";
      id: string;
      patch: (n: FlowNode) => FlowNode;
      /** Edits with the same key in a row (typing in one field) form one undo step. */
      coalesce?: string;
    }
  | { type: "renameNode"; id: string; to: string }
  /** Rename an output port of an agent node; its edges follow. */
  | { type: "renameAgentPort"; id: string; from: string; to: string }
  /** Remove an output port of an agent node together with its edges. */
  | { type: "removeAgentPort"; id: string; port: string }
  | { type: "updateMeta"; patch: Partial<FlowMeta> & { subscribe?: never } }
  | { type: "markSaved"; flow: Flow }
  | { type: "undo" }
  | { type: "redo" };

export function splitFlow(flow: Flow): FlowMeta {
  const { nodes: _nodes, edges: _edges, subscribe: _subscribe, ...meta } = flow;
  return meta;
}

export function serialize(
  state: Pick<EditorState, "meta" | "nodes" | "edges">,
): Flow {
  return graphToFlow(state.meta, { nodes: state.nodes, edges: state.edges });
}

export function initState(flow: Flow): EditorState {
  const graph = flowToGraph(flow);
  return {
    meta: splitFlow(flow),
    nodes: graph.nodes,
    edges: graph.edges,
    past: [],
    future: [],
    dragging: false,
    baseline: JSON.stringify(serialize({ meta: splitFlow(flow), ...graph })),
  };
}

export function isDirty(state: EditorState): boolean {
  return JSON.stringify(serialize(state)) !== state.baseline;
}

function snapshot(s: EditorState): Snapshot {
  return { meta: s.meta, nodes: s.nodes, edges: s.edges };
}

function withHistory(s: EditorState, next: Partial<Snapshot>): EditorState {
  return {
    ...s,
    ...next,
    past: [...s.past, snapshot(s)].slice(-HISTORY_LIMIT),
    future: [],
  };
}

/** Port handle ids a connection may start from, for the given source node. */
function sourcePorts(nodes: GraphNode[], id: string): string[] | null {
  const n = nodes.find((x) => x.id === id);
  return n && isFlowNode(n) ? allPorts(n.data.node) : null;
}

/**
 * Whether a connection can be added: source port exists, both ends are flow
 * nodes, it is not a duplicate, and it would not close a cycle.
 */
export function canConnect(
  state: Pick<EditorState, "nodes" | "edges">,
  c: { source: string; sourceHandle?: string | null; target: string },
): boolean {
  const ports = sourcePorts(state.nodes, c.source);
  if (!ports) return false;
  const target = state.nodes.find((n) => n.id === c.target);
  if (!target || !isFlowNode(target)) return false;
  const port = c.sourceHandle ?? "out";
  if (!ports.includes(port)) return false;
  if (
    state.edges.some(
      (e) =>
        e.source === c.source &&
        e.target === c.target &&
        e.sourceHandle === port,
    )
  ) {
    return false;
  }
  return !wouldCreateCycle(
    state.edges.map((e) => ({ from: e.source, to: e.target })),
    c.source,
    c.target,
  );
}

function allIds(nodes: GraphNode[]): string[] {
  return nodes.filter(isFlowNode).map((n) => n.id);
}

function reduce(s: EditorState, a: EditorAction): EditorState {
  switch (a.type) {
    case "load":
      return initState(a.flow);

    case "markSaved":
      return { ...s, baseline: JSON.stringify(a.flow) };

    case "nodeChanges": {
      const nodes = applyNodeChanges(a.changes, s.nodes);
      let next: EditorState = { ...s, nodes };

      // A drag is one history entry, recorded when it starts.
      const positions = a.changes.filter((c) => c.type === "position");
      if (positions.some((c) => c.dragging === true) && !s.dragging) {
        next = {
          ...next,
          dragging: true,
          past: [...s.past, snapshot(s)].slice(-HISTORY_LIMIT),
          future: [],
        };
      } else if (positions.some((c) => c.dragging === false)) {
        next = { ...next, dragging: false };
      }

      const removed = new Set(
        a.changes.flatMap((c) => (c.type === "remove" ? [c.id] : [])),
      );
      if (removed.size > 0) {
        const edges = s.edges.filter(
          (e) => !removed.has(e.source) && !removed.has(e.target),
        );
        next = withHistory(s, { nodes, edges });
      }
      return next;
    }

    case "edgeChanges": {
      const edges = applyEdgeChanges(a.changes, s.edges);
      if (a.changes.some((c) => c.type === "remove"))
        return withHistory(s, { edges });
      return { ...s, edges };
    }

    case "connect": {
      const c = a.connection;
      if (!canConnect(s, c)) return s;
      const edge = flowEdgeToGraphEdge({
        from: c.source,
        to: c.target,
        port: c.sourceHandle ?? "out",
      });
      return withHistory(s, {
        edges: [...s.edges, { ...edge, targetHandle: IN_HANDLE }],
      });
    }

    case "addNode": {
      const id = uniqueId(a.nodeType, allIds(s.nodes));
      const node = newNode(a.nodeType, id, {
        x: Math.round(a.position.x),
        y: Math.round(a.position.y),
      });
      const gn: FlowGraphNode = {
        id,
        type: node.type,
        position: { x: node.ui?.x ?? 0, y: node.ui?.y ?? 0 },
        data: { node },
        selected: true,
      };
      const cleared = s.nodes.map((n) =>
        n.selected ? { ...n, selected: false } : n,
      );
      return withHistory(s, { nodes: [...cleared, gn] });
    }

    case "addSensor": {
      const id = sensorNodeId(a.sensor);
      if (s.nodes.some((n) => n.id === id)) return s;
      return withHistory(s, {
        nodes: [
          ...s.nodes,
          {
            id,
            type: "sensor",
            position: a.position,
            data: { sensor: a.sensor },
          },
        ],
      });
    }

    case "updateNode": {
      let changed = false;
      const nodes = s.nodes.map((n) => {
        if (n.id !== a.id || !isFlowNode(n)) return n;
        changed = true;
        const node = a.patch(n.data.node);
        return {
          ...n,
          type: node.type,
          data: { ...n.data, node },
        } as FlowGraphNode;
      });
      if (!changed) return s;
      // Keystrokes in one field share a history entry.
      const key =
        a.coalesce === undefined ? undefined : `${a.id}:${a.coalesce}`;
      if (key !== undefined && key === s.coalesce) return { ...s, nodes };
      return { ...withHistory(s, { nodes }), coalesce: key };
    }

    case "renameNode": {
      const to = a.to.trim();
      if (to === "" || to === a.id || allIds(s.nodes).includes(to)) return s;
      const nodes = s.nodes.map((n) =>
        n.id === a.id && isFlowNode(n)
          ? ({
              ...n,
              id: to,
              data: { ...n.data, node: { ...n.data.node, id: to } },
            } as FlowGraphNode)
          : n,
      );
      const edges = s.edges.map((e) => {
        const source = e.source === a.id ? to : e.source;
        const target = e.target === a.id ? to : e.target;
        return {
          ...e,
          id: `${source}|${e.sourceHandle ?? "out"}|${target}`,
          source,
          target,
        };
      });
      return withHistory(s, { nodes, edges });
    }

    case "renameAgentPort": {
      let changed = false;
      const nodes = s.nodes.map((n) => {
        if (n.id !== a.id || !isFlowNode(n) || n.data.node.type !== "agent")
          return n;
        const node = renamePort(n.data.node, a.from, a.to);
        if (node === n.data.node) return n;
        changed = true;
        return { ...n, data: { ...n.data, node } } as FlowGraphNode;
      });
      if (!changed) return s;
      const edges = s.edges.map((e) =>
        e.source === a.id && (e.sourceHandle ?? "out") === a.from
          ? {
              ...e,
              id: `${e.source}|${a.to}|${e.target}`,
              sourceHandle: a.to,
            }
          : e,
      );
      return withHistory(s, { nodes, edges });
    }

    case "removeAgentPort": {
      let changed = false;
      const nodes = s.nodes.map((n) => {
        if (n.id !== a.id || !isFlowNode(n) || n.data.node.type !== "agent")
          return n;
        changed = true;
        return {
          ...n,
          data: { ...n.data, node: removePort(n.data.node, a.port) },
        } as FlowGraphNode;
      });
      if (!changed) return s;
      const edges = s.edges.filter(
        (e) => !(e.source === a.id && (e.sourceHandle ?? "out") === a.port),
      );
      return withHistory(s, { nodes, edges });
    }

    case "updateMeta":
      return withHistory(s, { meta: { ...s.meta, ...a.patch } });

    case "undo": {
      const prev = s.past[s.past.length - 1];
      if (!prev) return s;
      return {
        ...s,
        ...prev,
        past: s.past.slice(0, -1),
        future: [snapshot(s), ...s.future],
      };
    }

    case "redo": {
      const nxt = s.future[0];
      if (!nxt) return s;
      return {
        ...s,
        ...nxt,
        past: [...s.past, snapshot(s)],
        future: s.future.slice(1),
      };
    }
  }
}

/**
 * Reducer entry point. Any action other than a node edit (or a canvas size
 * measurement) ends a run of coalesced edits.
 */
export function editorReducer(s: EditorState, a: EditorAction): EditorState {
  const next = reduce(s, a);
  const measureOnly =
    a.type === "nodeChanges" && a.changes.every((c) => c.type === "dimensions");
  if (next.coalesce === undefined || a.type === "updateNode" || measureOnly) {
    return next;
  }
  return { ...next, coalesce: undefined };
}

/** Edges as drawn: model edges (minus dangling ones) plus derived sensor edges. */
export function displayEdges(graph: Graph): GraphEdge[] {
  const present = new Set(graph.nodes.map((n) => n.id));
  const real = graph.edges.filter(
    (e) => present.has(e.source) && present.has(e.target),
  );
  return [...real, ...deriveSensorEdges(graph)];
}

export function selectedFlowNode(
  s: Pick<EditorState, "nodes">,
): FlowNode | null {
  const sel = s.nodes.filter((n) => n.selected);
  if (sel.length !== 1) return null;
  const n = sel[0];
  return isFlowNode(n) ? n.data.node : null;
}

export function subscribedSensors(s: Pick<EditorState, "nodes">): string[] {
  return s.nodes.filter(isSensorNode).map((n) => n.data.sensor);
}
