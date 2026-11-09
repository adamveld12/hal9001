// Pure helpers of "Test flow": the request a sample picker builds, the canvas
// overlay of a result, and the wording of the ways a test can be refused.

import type { AssistFlowTestResult, AssistTestRequest } from "../../api/assist";
import { ApiError, errorMessage } from "../../api/client";
import type { Flow, FlowEdge } from "../../flow/model";
import { edgeKey } from "../../flow/model";
import type { RunOverlay } from "../../flow/overlay";
import { buildRunOverlay, type OverlayNodeRun } from "../../flow/runOverlay";
import { diagnosticsOf, type Diagnostic } from "../files/diagnostics";

export type TestSource = "sample" | "event" | "json" | "sensor";

export interface TestInputState {
  source: TestSource;
  /** Index into the session's samples, as chosen in the picker ("" = none). */
  sampleIndex: string;
  eventId: string;
  json: string;
  /** A draft sensor of the session whose output is the input ("" = none). */
  fromSensor: string;
  /** Which item of that sensor's output, as typed (0 is the first). */
  item: string;
}

export const INITIAL_TEST_INPUT: TestInputState = {
  source: "sample",
  sampleIndex: "",
  eventId: "",
  json: "{}",
  fromSensor: "",
  item: "0",
};

export type TestRequestResult =
  { ok: true; body: AssistTestRequest } | { ok: false; error: string };

/** The test request for the picker's state, or what the user still has to choose. */
export function buildFlowTestRequest(s: TestInputState): TestRequestResult {
  switch (s.source) {
    case "sample": {
      if (s.sampleIndex === "") return { ok: false, error: "Pick a sample." };
      return { ok: true, body: { sample_index: Number(s.sampleIndex) } };
    }
    case "event":
      return s.eventId === ""
        ? { ok: false, error: "Pick an event." }
        : { ok: true, body: { event_id: s.eventId } };
    case "json": {
      if (s.json.trim() === "")
        return { ok: false, error: "Paste a JSON payload." };
      try {
        return { ok: true, body: { input: JSON.parse(s.json) as unknown } };
      } catch (e) {
        return {
          ok: false,
          error: `Invalid JSON: ${e instanceof Error ? e.message : String(e)}`,
        };
      }
    }
    case "sensor": {
      if (s.fromSensor === "") {
        return { ok: false, error: "Pick a draft sensor." };
      }
      const item = s.item.trim() === "" ? 0 : Number(s.item);
      if (!Number.isInteger(item) || item < 0) {
        return {
          ok: false,
          error: "The item number must be a whole number, 0 or more.",
        };
      }
      return { ok: true, body: { from_sensor: s.fromSensor, item } };
    }
  }
}

/**
 * The read-only run overlay of a test result on the canvas, from the result
 * alone (no extra requests). Undefined when the canvas no longer has the nodes
 * or edges the test ran through, so an old result never marks a different
 * graph.
 */
export function overlayFromTestResult(
  flow: Flow,
  result: AssistFlowTestResult,
): RunOverlay | undefined {
  const nodeIds = new Set(flow.nodes.map((n) => n.id));
  if (result.nodes.some((n) => !nodeIds.has(n.node_id))) return undefined;
  const edges = new Set(flow.edges.map(edgeKey));
  const taken = result.edges_taken.map((e): FlowEdge => ({
    from: e.from,
    to: e.to,
    port: e.port,
  }));
  if (taken.some((e) => !edges.has(edgeKey(e)))) return undefined;

  const entered = new Set(result.edges_taken.map((e) => e.to));
  const runs: OverlayNodeRun[] = result.nodes.map((n) => ({
    id: n.node_run_id,
    node_id: n.node_id,
    // The test started the entry nodes; every other node was fed by an edge.
    parent_node_run_id: entered.has(n.node_id) ? n.node_run_id : null,
    status: n.status,
    decision: n.decision,
    reason: n.reason,
    error: n.error,
    ports: n.ports,
    emitted_count: n.outputs.length,
  }));
  return {
    nodes: buildRunOverlay(flow, runs).nodes,
    takenEdges: new Set(taken.map(edgeKey)),
  };
}

export interface TestFailure {
  message: string;
  /** Findings of a 422: the draft does not validate. */
  diagnostics: Diagnostic[];
}

/** What to tell the user when the test request itself was refused. */
export function describeTestFailure(e: unknown): TestFailure {
  const diagnostics = diagnosticsOf(e) ?? [];
  if (e instanceof ApiError) {
    if (e.status === 422) {
      return {
        message: diagnostics.some((d) => d.severity === "error")
          ? "The draft does not validate, so it was not run."
          : `${errorMessage(e)} Choose an input or fix the draft.`,
        diagnostics,
      };
    }
    if (e.status === 409) {
      return {
        message: `Approval needed: ${errorMessage(e)}`,
        diagnostics,
      };
    }
    if (e.status === 423) {
      return {
        message: "The kill switch is engaged; resume to run tests.",
        diagnostics,
      };
    }
  }
  return { message: errorMessage(e), diagnostics };
}
