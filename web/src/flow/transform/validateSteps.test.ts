import { describe, expect, it } from "vitest";
import type { Flow } from "../model";
import { validateFlow } from "../validate";
import {
  claimingPath,
  groupStepDiagnostics,
  parseStepPath,
  renderedPaths,
  stepMessages,
} from "./diagnostics";
import { newStep } from "./steps";
import type { TransformStep } from "./types";
import { validateSteps } from "./validateSteps";

const paths = (steps: TransformStep[]) =>
  validateSteps(steps).map((d) => d.path);

describe("validateSteps", () => {
  it("accepts complete steps", () => {
    expect(
      validateSteps([
        {
          type: "filter",
          conditions: [
            { field: "state", op: "equals", value: "open" },
            { field: "labels[].name", op: "in", value: ["a", 2] },
            { field: "draft", op: "is_false" },
          ],
        },
        {
          type: "set",
          field: "n",
          value: { kind: "function", field: "t", function: "length" },
        },
        { type: "limit", field: "l", count: 0 },
        {
          type: "route",
          rules: [{ port: "a", conditions: [{ field: "x", op: "exists" }] }],
        },
      ]),
    ).toEqual([]);
  });

  it("flags an empty new filter at the field and the missing value", () => {
    expect(paths([newStep("filter")])).toEqual([
      "steps[0].conditions[0].field",
      "steps[0].conditions[0].value",
    ]);
  });

  it("applies the operator value rules", () => {
    const check = (c: TransformStep["conditions"]) =>
      validateSteps([{ type: "filter", conditions: c }]).map((d) => d.path);
    expect(check([{ field: "a", op: "exists", value: 1 }])).toEqual([
      "steps[0].conditions[0].value",
    ]);
    expect(check([{ field: "a", op: "in", value: [] }])).toEqual([
      "steps[0].conditions[0].value",
    ]);
    expect(check([{ field: "a", op: "starts_with", value: 3 }])).toEqual([
      "steps[0].conditions[0].value",
    ]);
    expect(check([{ field: "a", op: "gt", value: true }])).toEqual([
      "steps[0].conditions[0].value",
    ]);
    expect(check([{ field: "a", op: "gt", value: "2025-01-01" }])).toEqual([]);
    expect(
      check([{ field: "a", op: "gt", value: 1, case_sensitive: true }]),
    ).toEqual(["steps[0].conditions[0].case_sensitive"]);
    expect(check([])).toEqual(["steps[0].conditions"]);
  });

  it("requires a route to be the only and last step, with named ports", () => {
    const route: TransformStep = {
      type: "route",
      rules: [{ port: "a", conditions: [{ field: "x", op: "exists" }] }],
    };
    expect(paths([route, { type: "remove", fields: ["a"] }])).toEqual([
      "steps[0]",
    ]);
    expect(paths([route, route])).toEqual(["steps[0]", "steps[1]"]);
    expect(
      paths([
        {
          type: "route",
          rules: [{ port: "", conditions: [{ field: "x", op: "exists" }] }],
        },
      ]),
    ).toEqual(["steps[0].rules[0].port"]);
    expect(
      validateSteps([
        {
          type: "route",
          default_port: "error",
          rules: [
            { port: "error", conditions: [{ field: "x", op: "exists" }] },
          ],
        },
      ]).map((d) => d.path),
    ).toEqual(["steps[0].rules[0].port", "steps[0].default_port"]);
  });

  it("keeps list elements out of single-field steps", () => {
    expect(
      paths([
        { type: "pick", fields: ["a", "l[].x", ""] },
        { type: "set", field: "a[].b", value: { kind: "constant", value: 1 } },
      ]),
    ).toEqual(["steps[0].fields[1]", "steps[0].fields[2]", "steps[1].field"]);
  });

  it("checks set values per kind", () => {
    const set = (value: TransformStep["value"]) =>
      paths([{ type: "set", field: "f", value }]);
    expect(set({ kind: "constant" })).toEqual(["steps[0].value.value"]);
    expect(set({ kind: "field" })).toEqual(["steps[0].value.field"]);
    expect(set({ kind: "template" })).toEqual(["steps[0].value.template"]);
    expect(set({ kind: "function", field: "x" })).toEqual([
      "steps[0].value.function",
    ]);
    expect(set({ kind: "function", field: "x", function: "join" })).toEqual([
      "steps[0].value.arg",
    ]);
    expect(
      set({ kind: "function", field: "x", function: "join", arg: "" }),
    ).toEqual([]);
    expect(
      set({ kind: "function", field: "x", function: "split", arg: "" }),
    ).toEqual(["steps[0].value.arg"]);
    expect(
      set({ kind: "function", field: "x", function: "default", arg: 0 }),
    ).toEqual([]);
  });

  it("checks list steps", () => {
    expect(
      paths([
        { type: "limit", field: "l", count: -1 },
        { type: "limit", field: "l", count: 1.5 },
        { type: "limit", field: "l" },
        { type: "aggregate", field: ".", into: "n", op: "count" },
        { type: "aggregate", field: "l", op: "group_count", into: "n" },
        { type: "sort" },
      ]),
    ).toEqual([
      "steps[0].count",
      "steps[1].count",
      "steps[2].count",
      "steps[3].field",
      "steps[4].group_by",
      "steps[5].field",
    ]);
  });
});

describe("flow validation of transform nodes", () => {
  const flow = (steps: TransformStep[], edgePort?: string): Flow => ({
    id: "f",
    enabled: true,
    subscribe: ["s"],
    nodes: [
      { id: "t", type: "transform", steps },
      { id: "next", type: "inbox", title: "x" },
    ],
    edges: [{ from: "t", port: edgePort, to: "next" }],
  });

  it("reports step problems with node paths and the node id", () => {
    const diags = validateFlow(flow([newStep("filter")]));
    expect(diags.map((d) => [d.node_id, d.path])).toEqual([
      ["t", "nodes[0].steps[0].conditions[0].field"],
      ["t", "nodes[0].steps[0].conditions[0].value"],
    ]);
  });

  it("flags an edge from a route port that no longer exists instead of dropping it", () => {
    const route = (port: string): TransformStep => ({
      type: "route",
      rules: [{ port, conditions: [{ field: "x", op: "exists" }] }],
    });
    expect(validateFlow(flow([route("urgent")], "urgent"))).toEqual([]);
    const diags = validateFlow(flow([route("renamed")], "urgent"));
    expect(diags).toHaveLength(1);
    expect(diags[0]).toMatchObject({
      path: "edges[0].port",
      node_id: "t",
      severity: "error",
      message: 'node "t" has no port "urgent"',
    });
  });
});

describe("diagnostic paths", () => {
  it("normalizes preview and flow paths to a step and a field path", () => {
    expect(parseStepPath("steps[1].conditions[0].value")).toEqual({
      step: 1,
      field: "conditions[0].value",
    });
    expect(parseStepPath("nodes[2].steps[1].conditions[0].value")).toEqual({
      step: 1,
      field: "conditions[0].value",
    });
    expect(parseStepPath("nodes.triage.steps[0]")).toEqual({
      step: 0,
      field: "",
    });
    expect(parseStepPath("nodes[2].timeout")).toBeNull();
    expect(parseStepPath("steps")).toBeNull();
    expect(parseStepPath(undefined)).toBeNull();
  });

  it("groups by step and collapses duplicates", () => {
    const d = (path: string, message = "m") => ({
      path,
      message,
      severity: "error" as const,
    });
    const g = groupStepDiagnostics([
      d("steps[1].field"),
      d("nodes[0].steps[1].field"),
      d("steps[0].conditions[0].op", "other"),
      d("nodes[0].timeout"),
    ]);
    expect([...g.byStep.keys()].sort()).toEqual([0, 1]);
    expect(g.byStep.get(1)).toHaveLength(1);
    expect(g.rest.map((x) => x.path)).toEqual(["nodes[0].timeout"]);
  });

  it("shows a message under the deepest field the form renders, else on the card", () => {
    const step: TransformStep = {
      type: "filter",
      conditions: [{ field: "a", op: "equals", value: "x" }],
    };
    const rendered = renderedPaths(step);
    expect(claimingPath(rendered, "conditions[0].value")).toBe(
      "conditions[0].value",
    );
    expect(claimingPath(rendered, "conditions[0].value[2]")).toBe(
      "conditions[0].value",
    );
    expect(claimingPath(rendered, "conditions[3].value")).toBe("conditions");
    expect(claimingPath(rendered, "bogus")).toBe("");
    const msgs = stepMessages(step, [
      {
        field: "conditions[0].value",
        msg: { severity: "error", message: "v" },
      },
      { field: "", msg: { severity: "error", message: "whole" } },
      { field: "nonsense", msg: { severity: "warning", message: "w" } },
    ]);
    expect(msgs.at("conditions[0].value").map((m) => m.message)).toEqual(["v"]);
    expect(msgs.card.map((m) => m.message)).toEqual(["whole", "w"]);
  });

  it("claims route rule fields by index", () => {
    const step: TransformStep = {
      type: "route",
      rules: [
        { port: "a", conditions: [{ field: "x", op: "exists" }] },
        { port: "b", conditions: [{ field: "y", op: "exists" }] },
      ],
    };
    const r = renderedPaths(step);
    expect(r.has("rules[1].conditions[0].field")).toBe(true);
    expect(r.has("rules[1].port")).toBe(true);
    expect(r.has("rules[2].port")).toBe(false);
  });
});
