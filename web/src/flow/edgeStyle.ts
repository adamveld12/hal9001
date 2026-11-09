import { MarkerType } from "@xyflow/react";
import type { GraphEdge } from "./graph";
import type { RunOverlay } from "./overlay";

export interface EdgeStyleInput {
  /** edge ids (edgeKey) that lie on a cycle */
  cycle: ReadonlySet<string>;
  overlay?: RunOverlay;
}

function colorFor(e: GraphEdge, input: EdgeStyleInput): string | undefined {
  if (input.cycle.has(e.id)) return "var(--bad)";
  if (input.overlay)
    return input.overlay.takenEdges.has(e.id) ? "var(--ok)" : "var(--line)";
  if (e.sourceHandle === "error") return "var(--bad)";
  if (e.sourceHandle === "denied") return "var(--warn)";
  if (e.sourceHandle === "approved") return "var(--ok)";
  return undefined;
}

/** Adds colour, dashes, and arrowheads; does not change topology. */
export function styleEdges(
  edges: GraphEdge[],
  input: EdgeStyleInput,
): GraphEdge[] {
  return edges.map((e) => {
    const color = colorFor(e, input);
    const derived = e.data?.derived === true;
    const taken = input.overlay?.takenEdges.has(e.id) === true;
    return {
      ...e,
      animated: input.cycle.has(e.id),
      style: {
        ...(color ? { stroke: color } : {}),
        ...(derived ? { strokeDasharray: "4 4" } : {}),
        strokeWidth: taken ? 2.5 : 1.5,
      },
      markerEnd: {
        type: MarkerType.ArrowClosed,
        width: 14,
        height: 14,
        ...(color ? { color } : {}),
      },
    };
  });
}
