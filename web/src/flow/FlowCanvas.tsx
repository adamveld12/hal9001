import {
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Connection,
  type EdgeChange,
  type NodeChange,
} from "@xyflow/react";
import {
  useCallback,
  useMemo,
  useState,
  type DragEvent,
  type ReactNode,
} from "react";
import { withMeasured, type MeasuredDims } from "./measured";
import { CanvasContext, type CanvasContextValue } from "./canvasContext";
import { styleEdges } from "./edgeStyle";
import { displayEdges } from "./editorState";
import type { GraphEdge, GraphNode } from "./graph";
import { FlowNodeView, SensorNodeView } from "./nodes";
import type { Diagnostic, NodeType, UIPos } from "./model";
import type { RunOverlay } from "./overlay";

const NODE_TYPES = {
  command: FlowNodeView,
  agent: FlowNodeView,
  inbox: FlowNodeView,
  transform: FlowNodeView,
  sensor: SensorNodeView,
};

export const PALETTE_MIME = "application/x-hal9001-palette";

export type PaletteDrag =
  { kind: "node"; type: NodeType } | { kind: "sensor"; id: string };

export interface FlowCanvasProps {
  nodes: GraphNode[];
  /** Model edges (sensor edges are derived here). */
  edges: GraphEdge[];
  readOnly?: boolean;
  diagnostics?: ReadonlyMap<string, Diagnostic[]>;
  overlay?: RunOverlay;
  knownSensors?: ReadonlySet<string>;
  cycleKeys?: ReadonlySet<string>;
  onNodesChange?: (changes: NodeChange<GraphNode>[]) => void;
  onEdgesChange?: (changes: EdgeChange<GraphEdge>[]) => void;
  onConnect?: (c: Connection) => void;
  isValidConnection?: (c: Connection | GraphEdge) => boolean;
  onDropPalette?: (item: PaletteDrag, position: UIPos) => void;
  onNodeClick?: (id: string) => void;
  onPaneClick?: () => void;
  /** Rendered inside the flow (use for Panels / palette). */
  children?: ReactNode;
}

const EMPTY_DIAGS: ReadonlyMap<string, Diagnostic[]> = new Map();
const EMPTY_SET: ReadonlySet<string> = new Set();

function Inner(props: FlowCanvasProps) {
  const rf = useReactFlow();
  const readOnly = props.readOnly === true;

  // A canvas without onNodesChange (run and version views) never stores the
  // sizes React Flow measures on the node objects, and the MiniMap skips nodes
  // without a size. Keep the measured sizes here and hand them back.
  const [dims, setDims] = useState<ReadonlyMap<string, MeasuredDims>>(
    new Map(),
  );
  const { onNodesChange } = props;
  const handleNodesChange = useCallback(
    (changes: NodeChange<GraphNode>[]) => {
      if (onNodesChange) {
        onNodesChange(changes);
        return;
      }
      setDims((prev) => {
        let next: Map<string, MeasuredDims> | undefined;
        for (const c of changes) {
          if (c.type !== "dimensions" || !c.dimensions) continue;
          const old = prev.get(c.id);
          if (
            old?.width === c.dimensions.width &&
            old.height === c.dimensions.height
          )
            continue;
          next ??= new Map(prev);
          next.set(c.id, c.dimensions);
        }
        return next ?? prev;
      });
    },
    [onNodesChange],
  );
  const nodes = useMemo(
    () => (onNodesChange ? props.nodes : withMeasured(props.nodes, dims)),
    [onNodesChange, props.nodes, dims],
  );

  const edges = useMemo(
    () =>
      styleEdges(displayEdges({ nodes: props.nodes, edges: props.edges }), {
        cycle: props.cycleKeys ?? EMPTY_SET,
        overlay: props.overlay,
      }),
    [props.nodes, props.edges, props.cycleKeys, props.overlay],
  );

  const ctx = useMemo<CanvasContextValue>(() => {
    const usedPorts = new Map<string, Set<string>>();
    for (const e of props.edges) {
      const set = usedPorts.get(e.source) ?? new Set<string>();
      set.add(e.sourceHandle ?? "out");
      usedPorts.set(e.source, set);
    }
    return {
      readOnly,
      diagnostics: props.diagnostics ?? EMPTY_DIAGS,
      overlay: props.overlay,
      usedPorts,
      knownSensors: props.knownSensors,
    };
  }, [
    readOnly,
    props.diagnostics,
    props.overlay,
    props.edges,
    props.knownSensors,
  ]);

  const onDragOver = useCallback((e: DragEvent) => {
    if (e.dataTransfer.types.includes(PALETTE_MIME)) {
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    }
  }, []);

  const { onDropPalette } = props;
  const onDrop = useCallback(
    (e: DragEvent) => {
      const raw = e.dataTransfer.getData(PALETTE_MIME);
      if (!raw || !onDropPalette) return;
      e.preventDefault();
      const item = JSON.parse(raw) as PaletteDrag;
      onDropPalette(
        item,
        rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }),
      );
    },
    [onDropPalette, rf],
  );

  return (
    <CanvasContext.Provider value={ctx}>
      <ReactFlow<GraphNode, GraphEdge>
        nodes={nodes}
        edges={edges}
        nodeTypes={NODE_TYPES}
        onNodesChange={handleNodesChange}
        onEdgesChange={props.onEdgesChange}
        onConnect={props.onConnect}
        isValidConnection={props.isValidConnection}
        onNodeClick={(_, n) => props.onNodeClick?.(n.id)}
        onPaneClick={props.onPaneClick}
        onDragOver={onDragOver}
        onDrop={onDrop}
        nodesDraggable={!readOnly}
        nodesConnectable={!readOnly}
        edgesReconnectable={false}
        deleteKeyCode={readOnly ? null : ["Backspace", "Delete"]}
        fitView
        fitViewOptions={{
          // leave room for the palette panel on the left while editing
          padding: {
            top: "48px",
            right: "48px",
            bottom: "48px",
            left: readOnly ? "48px" : "216px",
          },
          maxZoom: 1,
        }}
        minZoom={0.2}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
        <Controls showInteractive={false} />
        <MiniMap
          pannable
          zoomable
          className="!bg-panel"
          maskColor="transparent"
          nodeColor="var(--line)"
        />
        {props.children}
      </ReactFlow>
    </CanvasContext.Provider>
  );
}

export function FlowCanvas(props: FlowCanvasProps) {
  return (
    <ReactFlowProvider>
      <Inner {...props} />
    </ReactFlowProvider>
  );
}
