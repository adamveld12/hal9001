import type { Schemas } from "../../api/client";

export type SampleSource = "event" | "node_run" | "json";

export interface SampleState {
  source: SampleSource;
  eventId: string;
  nodeRunId: string;
  outputIndex: string;
  json: string;
}

export type SampleResult =
  { ok: true; input: Schemas["TestNodeInput"] } | { ok: false; error: string };

/** Turn the picker state into the API's TestNodeInput, or say what is missing. */
export function buildSampleInput(s: SampleState): SampleResult {
  switch (s.source) {
    case "event":
      return s.eventId === ""
        ? { ok: false, error: "Pick an event." }
        : { ok: true, input: { source: "event", event_id: s.eventId } };
    case "node_run": {
      if (s.nodeRunId === "")
        return { ok: false, error: "Pick an upstream node run." };
      const idx = s.outputIndex.trim() === "" ? 0 : Number(s.outputIndex);
      if (!Number.isInteger(idx) || idx < 0) {
        return {
          ok: false,
          error: "Output index must be a whole number, 0 or more.",
        };
      }
      return {
        ok: true,
        input: {
          source: "node_run",
          node_run_id: s.nodeRunId,
          output_index: idx,
        },
      };
    }
    case "json": {
      if (s.json.trim() === "")
        return { ok: false, error: "Paste a JSON payload." };
      try {
        return {
          ok: true,
          input: { source: "json", json: JSON.parse(s.json) as unknown },
        };
      } catch (e) {
        return {
          ok: false,
          error: `Invalid JSON: ${e instanceof Error ? e.message : String(e)}`,
        };
      }
    }
  }
}

/** Ids of nodes that feed `nodeId`, whose outputs can be replayed as its input. */
export function upstreamNodeIds(
  edges: { from: string; to: string }[],
  nodeId: string,
): string[] {
  return [...new Set(edges.filter((e) => e.to === nodeId).map((e) => e.from))];
}
