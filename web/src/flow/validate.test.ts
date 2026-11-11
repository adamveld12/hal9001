import { describe, expect, it } from "vitest";
import { triageGithub } from "./fixtures";
import {
  cycleEdgeKeys,
  cycleNodeIds,
  diagnosticField,
  hasErrors,
  indexDiagnostics,
  validateFlow,
  wouldCreateCycle,
  worstSeverity,
} from "./validate";

describe("cycle detection", () => {
  const edges = [
    { from: "a", to: "b" },
    { from: "b", to: "c" },
  ];

  it("rejects a connection that closes a loop, transitively", () => {
    expect(wouldCreateCycle(edges, "c", "a")).toBe(true);
    expect(wouldCreateCycle(edges, "b", "a")).toBe(true);
  });

  it("rejects self connections", () => {
    expect(wouldCreateCycle([], "a", "a")).toBe(true);
  });

  it("allows forward and diamond connections", () => {
    expect(wouldCreateCycle(edges, "a", "c")).toBe(false);
    expect(wouldCreateCycle(edges, "a", "d")).toBe(false);
    expect(wouldCreateCycle([...edges, { from: "a", to: "d" }], "d", "c")).toBe(
      false,
    );
  });

  it("lists nodes on or downstream of an existing cycle", () => {
    const looped = [
      ...edges,
      { from: "c", to: "b" },
      { from: "c", to: "tail" },
    ];
    expect(cycleNodeIds(["a", "b", "c", "tail"], looped).sort()).toEqual([
      "b",
      "c",
      "tail",
    ]);
    expect(cycleNodeIds(["a", "b", "c"], edges)).toEqual([]);
  });

  it("marks the edges between cycle members", () => {
    const flow = triageGithub();
    flow.edges.push({ from: "review-small", to: "classify" });
    expect([...cycleEdgeKeys(flow)].sort()).toEqual([
      "classify|small|review-small",
      "review-small|out|classify",
    ]);
  });
});

describe("validateFlow", () => {
  it("accepts the §6 example", () => {
    expect(hasErrors(validateFlow(triageGithub()))).toBe(false);
  });

  it("reports an edge on a port the node does not declare", () => {
    const flow = triageGithub();
    flow.edges[1] = { from: "classify", port: "medium", to: "review-small" };
    const d = validateFlow(flow).find((x) => x.message.includes("no port"));
    expect(d).toMatchObject({
      severity: "error",
      node_id: "classify",
      path: "edges[1].port",
    });
  });

  it("allows the implicit error port, and denied only on approve agents", () => {
    const flow = triageGithub();
    flow.edges.push({ from: "review-small", port: "denied", to: "note-large" });
    flow.edges.push({ from: "classify", port: "error", to: "note-large" });
    expect(hasErrors(validateFlow(flow))).toBe(false);
    if (flow.nodes[2].type === "agent") flow.nodes[2].mode = "readonly";
    expect(
      validateFlow(flow).some((x) => x.message.includes('no port "denied"')),
    ).toBe(true);
  });

  it("allows edges from denied on a gated node and flags them when the gate is removed", () => {
    const flow = triageGithub();
    const note = flow.nodes[3];
    if (note.type !== "inbox") throw new Error("fixture changed");
    note.requires_approval = true;
    flow.edges.push({ from: "note-large", port: "denied", to: "review-small" });
    expect(
      validateFlow(flow).some((x) => x.message.includes('no port "denied"')),
    ).toBe(false);
    delete note.requires_approval;
    expect(
      validateFlow(flow).some((x) => x.message.includes('no port "denied"')),
    ).toBe(true);
  });

  it("rejects requires_approval on an approval node", () => {
    const flow = triageGithub();
    flow.nodes.push({
      id: "ask",
      type: "approval",
      title: "ok?",
      description: "d",
      requires_approval: true,
    } as never);
    const d = validateFlow(flow).find(
      (x) => x.node_id === "ask" && x.path?.endsWith(".requires_approval"),
    );
    expect(d?.severity).toBe("error");
  });

  it("allows approval_timeout on agents, but elsewhere only with requires_approval", () => {
    const flow = triageGithub();
    const cmd = flow.nodes[0];
    const agent = flow.nodes[2];
    if (cmd.type !== "command" || agent.type !== "agent")
      throw new Error("fixture changed");
    agent.approval_timeout = "30m";
    cmd.approval_timeout = "30m";
    const timeoutErrors = () =>
      validateFlow(flow).filter((x) => x.path?.endsWith(".approval_timeout"));
    expect(timeoutErrors().map((x) => x.node_id)).toEqual([cmd.id]);
    cmd.requires_approval = true;
    expect(timeoutErrors()).toEqual([]);
  });

  it("reports unknown nodes, duplicate ids, self loops, and cycles", () => {
    const flow = triageGithub();
    flow.edges.push({ from: "ghost", to: "classify" });
    flow.nodes.push({ ...flow.nodes[3], ui: undefined });
    flow.edges.push({ from: "note-large", to: "note-large" });
    flow.edges.push({ from: "review-small", to: "only-review-requests" });
    const messages = validateFlow(flow).map((d) => d.message);
    expect(messages).toContain('edge starts at unknown node "ghost"');
    expect(messages).toContain('duplicate node id "note-large"');
    expect(messages).toContain('node "note-large" is connected to itself');
    expect(messages.some((m) => m.includes("DAG"))).toBe(true);
  });

  it("flags empty scripts and implicit ports declared as outputs", () => {
    const flow = triageGithub();
    if (flow.nodes[0].type === "command") flow.nodes[0].script = "  ";
    if (flow.nodes[1].type === "command")
      flow.nodes[1].ports = ["small", "error", "small"];
    const diags = validateFlow(flow);
    expect(diags.find((d) => d.node_id === "only-review-requests")?.path).toBe(
      "nodes[0].script",
    );
    const portMsgs = diags
      .filter((d) => d.node_id === "classify")
      .map((d) => d.message);
    expect(portMsgs).toContain(
      '"error" is an implicit port and cannot be declared',
    );
    expect(portMsgs).toContain('port "small" is declared twice');
  });

  it("warns, without erroring, when no sensor is subscribed", () => {
    const flow = triageGithub();
    flow.subscribe = [];
    const diags = validateFlow(flow);
    expect(hasErrors(diags)).toBe(false);
    expect(
      diags.some((d) => d.severity === "warning" && d.path === "subscribe"),
    ).toBe(true);
  });
});

describe("diagnostic placement", () => {
  it("maps server paths to a field name", () => {
    expect(
      diagnosticField({
        path: "nodes[2].cwd.repo",
        severity: "error",
        message: "",
      }),
    ).toBe("repo");
    expect(
      diagnosticField({
        path: "nodes[2].ports[1]",
        severity: "error",
        message: "",
      }),
    ).toBe("ports");
    expect(diagnosticField({ severity: "error", message: "" })).toBeUndefined();
    expect(
      diagnosticField({ path: "nodes[2]", severity: "error", message: "" }),
    ).toBeUndefined();
    expect(
      diagnosticField({
        path: "nodes.classify.script",
        severity: "error",
        message: "",
      }),
    ).toBe("script");
  });

  it("groups by node_id, falls back to the node order in the path, and keeps flow-level ones", () => {
    const flow = triageGithub();
    const idx = indexDiagnostics(
      [
        {
          node_id: "classify",
          path: "nodes[1].script",
          severity: "error",
          message: "a",
        },
        { path: "nodes[3].title", severity: "warning", message: "b" },
        { path: "subscribe", severity: "error", message: "c" },
      ],
      flow,
    );
    expect(idx.byNode.get("classify")).toHaveLength(1);
    expect(idx.byNode.get("note-large")?.[0].message).toBe("b");
    expect(idx.flow.map((d) => d.message)).toEqual(["c"]);
    expect(worstSeverity(idx.byNode.get("note-large"))).toBe("warning");
    expect(worstSeverity(undefined)).toBeNull();
  });
});
