import { describe, expect, it } from "vitest";
import { triageGithub } from "./fixtures";
import {
  autoLayout,
  deriveSensorEdges,
  flowToGraph,
  graphToFlow,
  isFlowNode,
  isSensorNode,
  sensorNodeId,
  type FlowMeta,
} from "./graph";
import {
  allPorts,
  declaredPorts,
  entryNodeIds,
  newNode,
  uniqueId,
  type Flow,
} from "./model";

function meta(flow: Flow): FlowMeta {
  const { nodes: _n, edges: _e, subscribe: _s, ...rest } = flow;
  return rest;
}

describe("flow <-> graph conversion", () => {
  it("round-trips a flow with persisted positions exactly", () => {
    const flow = triageGithub();
    expect(graphToFlow(meta(flow), flowToGraph(flow))).toEqual(flow);
  });

  it("keeps unknown node fields and omits the default port on edges", () => {
    const flow = triageGithub();
    (flow.nodes[0] as unknown as Record<string, unknown>).future_field = {
      keep: true,
    };
    flow.edges[0] = {
      from: "only-review-requests",
      port: "out",
      to: "classify",
    };
    const back = graphToFlow(meta(flow), flowToGraph(flow));
    expect(
      (back.nodes[0] as unknown as Record<string, unknown>).future_field,
    ).toEqual({
      keep: true,
    });
    expect(back.edges[0]).toEqual({
      from: "only-review-requests",
      to: "classify",
    });
  });

  it("assigns layered positions to nodes without ui and persists them on save", () => {
    const flow = triageGithub();
    for (const n of flow.nodes) delete n.ui;
    const graph = flowToGraph(flow);
    const pos = (id: string) => graph.nodes.find((n) => n.id === id)!.position;
    expect(pos("only-review-requests").x).toBeLessThan(pos("classify").x);
    expect(pos("classify").x).toBeLessThan(pos("review-small").x);
    // siblings in the same layer must not overlap
    expect(pos("review-small")).not.toEqual(pos("note-large"));
    const saved = graphToFlow(meta(flow), graph);
    expect(saved.nodes.every((n) => n.ui !== undefined)).toBe(true);
    // everything except ui is unchanged
    expect(saved.nodes.map(({ ui: _ui, ...rest }) => rest)).toEqual(flow.nodes);
  });

  it("represents subscriptions as sensor nodes left of the flow and writes them back in order", () => {
    const flow = triageGithub();
    flow.subscribe = ["github-notifications", "linear-assigned"];
    const graph = flowToGraph(flow);
    const sensors = graph.nodes.filter(isSensorNode);
    expect(sensors.map((s) => s.id)).toEqual([
      sensorNodeId("github-notifications"),
      sensorNodeId("linear-assigned"),
    ]);
    const minFlowX = Math.min(
      ...graph.nodes.filter(isFlowNode).map((n) => n.position.x),
    );
    expect(sensors.every((s) => s.position.x < minFlowX)).toBe(true);
    expect(graphToFlow(meta(flow), graph).subscribe).toEqual(flow.subscribe);
  });

  it("rounds dragged positions on save", () => {
    const flow = triageGithub();
    const graph = flowToGraph(flow);
    graph.nodes[0].position = { x: 10.4, y: 99.6 };
    expect(graphToFlow(meta(flow), graph).nodes[0].ui).toEqual({
      x: 10,
      y: 100,
    });
  });

  it("derives sensor edges to entry nodes only", () => {
    const flow = triageGithub();
    flow.subscribe = ["a", "b"];
    flow.nodes.push({
      id: "second-entry",
      type: "inbox",
      title: "x",
      ui: { x: 0, y: 300 },
    });
    const edges = deriveSensorEdges(flowToGraph(flow));
    expect(entryNodeIds(flow).sort()).toEqual([
      "only-review-requests",
      "second-entry",
    ]);
    expect(edges.map((e) => `${e.source}>${e.target}`).sort()).toEqual([
      "sensor:a>only-review-requests",
      "sensor:a>second-entry",
      "sensor:b>only-review-requests",
      "sensor:b>second-entry",
    ]);
    expect(edges.every((e) => e.deletable === false)).toBe(true);
  });

  it("does not loop forever laying out a cyclic flow", () => {
    const flow = triageGithub();
    flow.edges.push({ from: "review-small", to: "only-review-requests" });
    for (const n of flow.nodes) delete n.ui;
    expect(autoLayout(flow).size).toBe(flow.nodes.length);
  });
});

describe("ports", () => {
  it("exposes declared ports plus implicit error, and denied only for approve agents", () => {
    const flow = triageGithub();
    const byId = Object.fromEntries(flow.nodes.map((n) => [n.id, n]));
    expect(allPorts(byId["only-review-requests"])).toEqual(["out", "error"]);
    expect(allPorts(byId["classify"])).toEqual(["small", "large", "error"]);
    expect(allPorts(byId["review-small"])).toEqual(["out", "error", "denied"]);
    const readonly = { ...byId["review-small"], mode: "readonly" as const };
    expect(allPorts(readonly)).toEqual(["out", "error"]);
    expect(declaredPorts(byId["note-large"])).toEqual(["out"]);
  });

  it("adds denied once to any node with requires_approval", () => {
    const flow = triageGithub();
    const byId = Object.fromEntries(flow.nodes.map((n) => [n.id, n]));
    const gated = { ...byId["classify"], requires_approval: true };
    expect(allPorts(gated)).toEqual(["small", "large", "error", "denied"]);
    const note = { ...byId["note-large"], requires_approval: true };
    expect(allPorts(note)).toEqual(["out", "error", "denied"]);
    // An approve-mode agent already has denied; the gate does not repeat it.
    const agent = { ...byId["review-small"], requires_approval: true };
    expect(allPorts(agent)).toEqual(["out", "error", "denied"]);
    // A gated agent outside approve mode gets it from the gate alone.
    const readonly = { ...agent, mode: "readonly" as const };
    expect(allPorts(readonly)).toEqual(["out", "error", "denied"]);
  });
});

describe("node factories", () => {
  it("creates unique ids", () => {
    expect(uniqueId("command", ["command", "command-2"])).toBe("command-3");
    expect(uniqueId("agent", [])).toBe("agent");
  });

  it("builds valid default nodes for each palette type", () => {
    expect(newNode("command", "c", { x: 1, y: 2 })).toMatchObject({
      type: "command",
      lang: "bash",
    });
    expect(newNode("agent", "a", { x: 1, y: 2 })).toMatchObject({
      type: "agent",
      mode: "readonly",
    });
    expect(newNode("inbox", "i", { x: 1, y: 2 })).toMatchObject({
      type: "inbox",
    });
  });
});
