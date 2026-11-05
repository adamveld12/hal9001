// Pure conversion between the flow model (model.ts) and the React Flow graph
// the canvas edits. The graph carries the full model node in `data.node`, so
// fields this UI does not know about survive a load/save round trip.

import type { Edge, Node } from "@xyflow/react";
import {
  DEFAULT_PORT,
  edgePort,
  entryNodeIds,
  type Flow,
  type FlowEdge,
  type FlowNode,
  type UIPos,
} from "./model";

export const SENSOR_PREFIX = "sensor:";
export const IN_HANDLE = "in";

export interface FlowNodeData extends Record<string, unknown> {
  node: FlowNode;
}
export interface SensorNodeData extends Record<string, unknown> {
  sensor: string;
}

export type FlowGraphNode = Node<FlowNodeData, FlowNode["type"]>;
export type SensorGraphNode = Node<SensorNodeData, "sensor">;
export type GraphNode = FlowGraphNode | SensorGraphNode;
export type GraphEdge = Edge<{ derived?: boolean }>;

/** Flow-level fields that are not part of the graph. */
export type FlowMeta = Omit<Flow, "nodes" | "edges" | "subscribe">;

export interface Graph {
  nodes: GraphNode[];
  /** Model edges only; sensor edges are derived (see deriveSensorEdges). */
  edges: GraphEdge[];
}

export function sensorNodeId(sensor: string): string {
  return SENSOR_PREFIX + sensor;
}

export function isSensorNode(n: GraphNode): n is SensorGraphNode {
  return n.type === "sensor";
}

export function isFlowNode(n: GraphNode): n is FlowGraphNode {
  return n.type !== "sensor";
}

export function flowEdgeToGraphEdge(e: FlowEdge): GraphEdge {
  const port = edgePort(e);
  return {
    id: `${e.from}|${port}|${e.to}`,
    source: e.from,
    sourceHandle: port,
    target: e.to,
    targetHandle: IN_HANDLE,
  };
}

const COL_WIDTH = 300;
const ROW_HEIGHT = 150;
const SENSOR_GAP = 280;
const SENSOR_ROW = 90;

/**
 * Longest-path layering used for nodes that have no persisted `ui`. Cycles do
 * not loop: a node revisited on the current path keeps its current depth.
 */
export function autoLayout(flow: Flow): Map<string, UIPos> {
  const incoming = new Map<string, string[]>();
  for (const n of flow.nodes) incoming.set(n.id, []);
  for (const e of flow.edges) incoming.get(e.to)?.push(e.from);

  const depth = new Map<string, number>();
  const onPath = new Set<string>();
  const visit = (id: string): number => {
    const known = depth.get(id);
    if (known !== undefined) return known;
    if (onPath.has(id)) return 0;
    onPath.add(id);
    let d = 0;
    for (const p of incoming.get(id) ?? []) d = Math.max(d, visit(p) + 1);
    onPath.delete(id);
    depth.set(id, d);
    return d;
  };

  const rows = new Map<number, number>();
  const out = new Map<string, UIPos>();
  for (const n of flow.nodes) {
    const d = visit(n.id);
    const row = rows.get(d) ?? 0;
    rows.set(d, row + 1);
    out.set(n.id, { x: d * COL_WIDTH, y: row * ROW_HEIGHT });
  }
  return out;
}

/** Positions for sensor source nodes: a column left of the leftmost node. */
function layoutSensors(subscribe: string[], positions: UIPos[]): UIPos[] {
  const minX =
    positions.length > 0 ? Math.min(...positions.map((p) => p.x)) : SENSOR_GAP;
  const minY =
    positions.length > 0 ? Math.min(...positions.map((p) => p.y)) : 0;
  return subscribe.map((_, i) => ({
    x: minX - SENSOR_GAP,
    y: minY + i * SENSOR_ROW,
  }));
}

export function flowToGraph(flow: Flow): Graph {
  const auto = autoLayout(flow);
  const nodes: GraphNode[] = flow.nodes.map((node) => ({
    id: node.id,
    type: node.type,
    position: node.ui
      ? { x: node.ui.x, y: node.ui.y }
      : (auto.get(node.id) ?? { x: 0, y: 0 }),
    data: { node },
  })) as FlowGraphNode[];

  const sensorPositions = layoutSensors(
    flow.subscribe,
    nodes.map((n) => n.position),
  );
  flow.subscribe.forEach((sensor, i) => {
    nodes.push({
      id: sensorNodeId(sensor),
      type: "sensor",
      position: sensorPositions[i],
      data: { sensor },
      deletable: true,
    });
  });

  return { nodes, edges: flow.edges.map(flowEdgeToGraphEdge) };
}

/**
 * Inverse of flowToGraph. Node order, edge order, and unknown node fields are
 * preserved; positions are written to `ui` rounded to whole pixels. Sensor
 * node positions are not persisted (the flow format has no place for them).
 */
export function graphToFlow(meta: FlowMeta, graph: Graph): Flow {
  const nodes: FlowNode[] = [];
  const subscribe: string[] = [];
  for (const n of graph.nodes) {
    if (isSensorNode(n)) {
      subscribe.push(n.data.sensor);
    } else {
      nodes.push({
        ...n.data.node,
        ui: { x: Math.round(n.position.x), y: Math.round(n.position.y) },
      });
    }
  }
  const edges: FlowEdge[] = graph.edges.map((e) => {
    const port = e.sourceHandle ?? DEFAULT_PORT;
    const out: FlowEdge = { from: e.source, to: e.target };
    if (port !== DEFAULT_PORT) out.port = port;
    return out;
  });
  return { ...meta, subscribe, nodes, edges };
}

/** Every subscribed sensor feeds every entry node (nodes without incoming edges). */
export function deriveSensorEdges(graph: Graph): GraphEdge[] {
  const flowNodes = graph.nodes.filter(isFlowNode).map((n) => n.data.node);
  const entries = entryNodeIds({
    id: "",
    enabled: true,
    subscribe: [],
    nodes: flowNodes,
    edges: graph.edges.map((e) => ({ from: e.source, to: e.target })),
  });
  const edges: GraphEdge[] = [];
  for (const s of graph.nodes.filter(isSensorNode)) {
    for (const target of entries) {
      edges.push({
        id: `${s.id}->${target}`,
        source: s.id,
        sourceHandle: DEFAULT_PORT,
        target,
        targetHandle: IN_HANDLE,
        selectable: false,
        deletable: false,
        focusable: false,
        data: { derived: true },
      });
    }
  }
  return edges;
}

/** Ids of flow nodes only (excludes sensor nodes). */
export function flowNodeIds(graph: Graph): string[] {
  return graph.nodes.filter(isFlowNode).map((n) => n.id);
}
