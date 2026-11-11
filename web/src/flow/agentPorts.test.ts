import { describe, expect, it } from "vitest";
import {
  addPort,
  agentPortDiagnostics,
  inboxFedBySchemalessPorts,
  portNameError,
  removePort,
  removePortEdges,
  renamePort,
  renamePortEdges,
  unusedPorts,
} from "./agentPorts";
import type { AgentNode, Flow } from "./model";
import { validateFlow } from "./validate";

const base: AgentNode = {
  id: "a",
  type: "agent",
  runtime: "claude",
  mode: "readonly",
  instructions: "x",
  output: "note",
};

describe("port names", () => {
  it("accepts [a-z0-9_]+ and rejects reserved, odd and duplicate names", () => {
    expect(portNameError("needs_human2", [])).toBeUndefined();
    for (const bad of ["", "out", "error", "denied", "Big", "a-b", "a b"]) {
      expect(portNameError(bad, [])).toBeDefined();
    }
    expect(portNameError("x", ["x"])).toMatch(/twice/);
  });
});

describe("port edits", () => {
  it("adding the first port clears output; later ports keep order", () => {
    const one = addPort(base, "a1");
    expect(one.output).toBeUndefined();
    expect(Object.keys(addPort(one, "b1").ports ?? {})).toEqual(["a1", "b1"]);
  });

  it("rename keeps position and edges follow", () => {
    const n = addPort(addPort(base, "first"), "second");
    const r = renamePort(n, "first", "renamed");
    expect(Object.keys(r.ports ?? {})).toEqual(["renamed", "second"]);
    const edges = [
      { from: "a", port: "first", to: "x" },
      { from: "a", port: "second", to: "y" },
      { from: "b", port: "first", to: "z" },
    ];
    expect(renamePortEdges(edges, "a", "first", "renamed")).toEqual([
      { from: "a", port: "renamed", to: "x" },
      { from: "a", port: "second", to: "y" },
      { from: "b", port: "first", to: "z" },
    ]);
  });

  it("remove drops the port and its edges; the last port drops emit", () => {
    const n = { ...addPort(base, "p"), emit: "many" as const };
    const r = removePort(n, "p");
    expect(r.ports).toBeUndefined();
    expect(r.emit).toBeUndefined();
    const edges = [
      { from: "a", port: "p", to: "x" },
      { from: "a", port: "q", to: "y" },
    ];
    expect(removePortEdges(edges, "a", "p")).toEqual([edges[1]]);
  });
});

describe("diagnostics", () => {
  it("warns about ports without edges", () => {
    const n = addPort(addPort(base, "used"), "idle");
    const edges = [{ from: "a", port: "used", to: "i" }];
    expect(unusedPorts(n, edges)).toEqual(["idle"]);
    const d = agentPortDiagnostics(n, 0, edges);
    expect(d).toHaveLength(1);
    expect(d[0]).toMatchObject({ code: "unused_port", severity: "warning" });
  });

  it("inbox title may be empty only behind schema-less ports", () => {
    const flow = (schema?: object): Flow => ({
      id: "f",
      enabled: true,
      subscribe: ["s"],
      nodes: [
        {
          ...addPort(base, "p"),
          ports: { p: schema ? { schema } : {} },
        } as AgentNode,
        { id: "i", type: "inbox", title: "" },
      ],
      edges: [{ from: "a", port: "p", to: "i" }],
    });
    expect(inboxFedBySchemalessPorts(flow(), "i")).toBe(true);
    expect(validateFlow(flow()).some((d) => d.path === "nodes[1].title")).toBe(
      false,
    );
    expect(
      validateFlow(flow({ type: "object" })).some(
        (d) => d.path === "nodes[1].title",
      ),
    ).toBe(true);
  });
});
