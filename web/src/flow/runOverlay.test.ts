import { describe, expect, it } from "vitest";
import type { Flow } from "./model";
import {
  buildRunOverlay,
  groupByNode,
  sensorEdgeId,
  type OverlayNodeRun,
} from "./runOverlay";

const flow: Flow = {
  id: "triage",
  enabled: true,
  subscribe: ["gh"],
  nodes: [
    {
      id: "split",
      type: "command",
      lang: "jq",
      script: ".",
      ports: ["out", "skip"],
    },
    {
      id: "ask",
      type: "agent",
      runtime: "claude",
      mode: "approve",
      prompt: "p",
    },
    { id: "note", type: "inbox", title: "t" },
    { id: "never", type: "inbox", title: "n" },
  ],
  edges: [
    { from: "split", to: "ask" },
    { from: "split", port: "skip", to: "never" },
    { from: "ask", to: "note" },
    { from: "ask", port: "error", to: "never" },
  ],
};

let n = 0;
function run(
  over: Partial<OverlayNodeRun> & { node_id: string },
): OverlayNodeRun {
  n++;
  return {
    id: `nr${n}`,
    parent_node_run_id: null,
    status: "done",
    decision: "emitted",
    reason: null,
    error: null,
    ports: ["out"],
    emitted_count: 1,
    ...over,
  };
}

describe("buildRunOverlay", () => {
  it("leaves unstarted nodes out so the canvas dims them", () => {
    const o = buildRunOverlay(flow, [run({ node_id: "split", ports: [] })]);
    expect(Object.keys(o.nodes)).toEqual(["split"]);
    expect(o.nodes["never"]).toBeUndefined();
  });

  it("shows a denied or timed-out gate as denied, with its reason", () => {
    const o = buildRunOverlay(flow, [
      run({
        node_id: "split",
        ports: ["denied"],
        error: "denied",
        reason: "denied: too risky",
      }),
      run({
        node_id: "note",
        ports: ["denied"],
        error: "approval_timeout",
        reason: "approval timed out, treated as denied",
      }),
    ]);
    expect(o.nodes["split"]).toEqual({
      visual: "denied",
      detail: "denied: too risky",
    });
    expect(o.nodes["note"].visual).toBe("denied");
  });

  it("is empty for a run without node runs", () => {
    const o = buildRunOverlay(flow, []);
    expect(o.nodes).toEqual({});
    expect(o.takenEdges.size).toBe(0);
  });

  it("aggregates fan-out runs: error beats emitted, with a breakdown", () => {
    const parent = run({ node_id: "split", emitted_count: 3 });
    const o = buildRunOverlay(flow, [
      parent,
      run({ node_id: "ask", parent_node_run_id: parent.id }),
      run({ node_id: "ask", parent_node_run_id: parent.id }),
      run({
        node_id: "ask",
        parent_node_run_id: parent.id,
        status: "failed",
        decision: "error",
        error: "timeout",
        ports: ["error"],
      }),
    ]);
    expect(o.nodes["ask"]).toEqual({
      visual: "error",
      detail: "3 runs: 1 error, 2 emitted",
    });
    expect(o.nodes["split"]).toEqual({
      visual: "emitted",
      detail: "3 emitted",
    });
  });

  it("says only the count when every run ended alike", () => {
    const o = buildRunOverlay(flow, [
      run({ node_id: "note" }),
      run({ node_id: "note" }),
    ]);
    expect(o.nodes["note"]).toEqual({ visual: "emitted", detail: "2 runs" });
  });

  it("lets an active run beat finished ones", () => {
    const o = buildRunOverlay(flow, [
      run({ node_id: "ask" }),
      run({
        node_id: "ask",
        status: "awaiting_approval",
        decision: null,
        ports: [],
      }),
    ]);
    expect(o.nodes["ask"].visual).toBe("awaiting");
  });

  it("shows the reason for a single dropped or skipped run, else the error class", () => {
    const o = buildRunOverlay(flow, [
      run({
        node_id: "split",
        decision: "dropped",
        ports: [],
        reason: "not mine",
      }),
      run({
        node_id: "ask",
        status: "skipped",
        decision: "skipped",
        ports: [],
        reason: "dry_run",
      }),
      run({
        node_id: "note",
        status: "failed",
        decision: "error",
        ports: [],
        error: "exit_nonzero",
      }),
    ]);
    expect(o.nodes["split"]).toEqual({ visual: "dropped", detail: "not mine" });
    expect(o.nodes["ask"]).toEqual({ visual: "skipped", detail: "dry_run" });
    expect(o.nodes["note"]).toEqual({
      visual: "error",
      detail: "exit_nonzero",
    });
  });

  it("marks only edges whose source emitted on that port", () => {
    const o = buildRunOverlay(flow, [
      run({ node_id: "split", ports: ["out"] }),
      run({ node_id: "ask", ports: ["out"] }),
    ]);
    expect([...o.takenEdges].sort()).toEqual(["ask|out|note", "split|out|ask"]);
  });

  it("marks the error edge for a failed node and unions ports across runs", () => {
    const o = buildRunOverlay(flow, [
      run({ node_id: "split", ports: ["out", "skip"] }),
      run({
        node_id: "ask",
        status: "failed",
        decision: "error",
        ports: ["error"],
      }),
    ]);
    expect(o.takenEdges.has("ask|error|never")).toBe(true);
    expect(o.takenEdges.has("split|skip|never")).toBe(true);
    expect(o.takenEdges.has("ask|out|note")).toBe(false);
  });

  it("takes no edge from a dropped node", () => {
    const o = buildRunOverlay(flow, [
      run({ node_id: "split", decision: "dropped", ports: [] }),
    ]);
    expect(o.takenEdges.size).toBe(0);
  });

  it("marks the sensor edge into an entry node that started", () => {
    const o = buildRunOverlay(flow, [run({ node_id: "split", ports: [] })], {
      sensorId: "gh",
    });
    expect(o.takenEdges.has(sensorEdgeId("gh", "split"))).toBe(true);
    expect(o.takenEdges.has(sensorEdgeId("gh", "never"))).toBe(false);
  });

  it("does not mark sensor edges when the sensor is unknown or the entry never ran", () => {
    expect(
      buildRunOverlay(flow, [run({ node_id: "split" })]).takenEdges.has(
        "sensor:gh->split",
      ),
    ).toBe(false);
    expect(buildRunOverlay(flow, [], { sensorId: "gh" }).takenEdges.size).toBe(
      0,
    );
  });
});

describe("groupByNode", () => {
  it("keeps creation order within each node", () => {
    const a = run({ node_id: "x" });
    const b = run({ node_id: "y" });
    const c = run({ node_id: "x" });
    const g = groupByNode([a, b, c]);
    expect(g.get("x")).toEqual([a, c]);
    expect(g.get("y")).toEqual([b]);
  });
});
