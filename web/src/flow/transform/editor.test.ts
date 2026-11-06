import { describe, expect, it } from "vitest";
import { flowFromApi, flowToApi } from "../apiAdapter";
import {
  canConnect,
  editorReducer,
  initState,
  isDirty,
  serialize,
  type EditorState,
} from "../editorState";
import { newNode, type Flow, type FlowNode } from "../model";
import { normalizeStep } from "./steps";
import type { TransformStep } from "./types";

const steps: TransformStep[] = [
  {
    type: "filter",
    match: "any",
    conditions: [
      { field: "state", op: "equals", value: "open", case_sensitive: true },
      { field: "labels[].name", op: "in", value: ["bug", "p1"] },
    ],
    keep: false,
  },
  {
    type: "set",
    field: "title_len",
    value: { kind: "function", field: "title", function: "length" },
  },
  { type: "sort", field: "labels", by: "name", order: "desc" },
  {
    type: "route",
    rules: [
      {
        port: "urgent",
        match: "any",
        conditions: [{ field: "severity", op: "equals", value: "urgent" }],
      },
    ],
    default_port: "normal",
  },
];

const flow = (): Flow => ({
  id: "f",
  enabled: true,
  dry_run: false,
  subscribe: ["s"],
  nodes: [
    { id: "t", type: "transform", steps, timeout: "5s", ui: { x: 10, y: 20 } },
    { id: "urgent-note", type: "inbox", title: "x", ui: { x: 300, y: 0 } },
  ],
  edges: [{ from: "t", port: "urgent", to: "urgent-note" }],
});

function edit(
  s: EditorState,
  patch: (n: FlowNode) => FlowNode,
  coalesce?: string,
) {
  return editorReducer(s, { type: "updateNode", id: "t", patch, coalesce });
}

describe("transform nodes in the editor model", () => {
  it("survives load, serialize and the API adapter unchanged", () => {
    const f = flow();
    const s = initState(f);
    expect(serialize(s)).toEqual(f);
    expect(isDirty(s)).toBe(false);
    expect(flowFromApi(flowToApi(f))).toEqual(f);
    // Byte for byte, so key order of the steps is untouched.
    expect(JSON.stringify(serialize(s).nodes)).toBe(JSON.stringify(f.nodes));
  });

  it("keeps steps in canonical order and shape when edited and saved", () => {
    const s = edit(initState(flow()), (n) =>
      n.type === "transform"
        ? {
            ...n,
            steps: (n.steps ?? []).map((st, i) =>
              i === 0 ? normalizeStep({ ...st, keep: true, match: "all" }) : st,
            ),
          }
        : n,
    );
    const t = serialize(s).nodes[0];
    expect(t.type === "transform" && t.steps?.[0]).toEqual({
      type: "filter",
      conditions: steps[0].conditions,
    });
    expect(isDirty(s)).toBe(true);
  });

  it("creates an empty transform from the palette with a unique id", () => {
    let s = initState(flow());
    s = editorReducer(s, {
      type: "addNode",
      nodeType: "transform",
      position: { x: 5, y: 6 },
    });
    s = editorReducer(s, {
      type: "addNode",
      nodeType: "transform",
      position: { x: 7, y: 8 },
    });
    const nodes = serialize(s).nodes;
    expect(nodes.slice(2)).toEqual([
      { id: "transform", type: "transform", steps: [], ui: { x: 5, y: 6 } },
      { id: "transform-2", type: "transform", steps: [], ui: { x: 7, y: 8 } },
    ]);
    expect(newNode("transform", "x", { x: 0, y: 0 })).toMatchObject({
      type: "transform",
      steps: [],
    });
  });

  it("updates the connectable ports when the route changes", () => {
    let s = initState(flow());
    const c = { source: "t", target: "urgent-note" };
    expect(canConnect(s, { ...c, sourceHandle: "normal" })).toBe(true);
    expect(canConnect(s, { ...c, sourceHandle: "error" })).toBe(true);
    expect(canConnect(s, { ...c, sourceHandle: "later" })).toBe(false);
    s = edit(s, (n) =>
      n.type === "transform"
        ? {
            ...n,
            steps: (n.steps ?? []).map((st) =>
              st.type === "route" ? { ...st, default_port: "later" } : st,
            ),
          }
        : n,
    );
    expect(canConnect(s, { ...c, sourceHandle: "later" })).toBe(true);
    expect(canConnect(s, { ...c, sourceHandle: "normal" })).toBe(false);
    // The existing edge is kept, not silently dropped.
    expect(serialize(s).edges).toEqual([
      { from: "t", port: "urgent", to: "urgent-note" },
    ]);
  });

  it("replaces a transform by a command node in place, keeping its edges", () => {
    let s = initState(flow());
    s = edit(s, () => ({
      id: "t",
      type: "command",
      lang: "jq",
      script: ".",
      ports: ["out", "urgent", "normal"],
      ui: { x: 10, y: 20 },
    }));
    const f = serialize(s);
    expect(f.nodes[0].type).toBe("command");
    expect(s.nodes[0].type).toBe("command");
    expect(f.edges).toEqual([{ from: "t", port: "urgent", to: "urgent-note" }]);
  });
});

describe("undo of typing", () => {
  const type = (s: EditorState, text: string, key = "steps[0].field") =>
    edit(
      s,
      (n) =>
        n.type === "transform"
          ? { ...n, steps: [{ type: "sort", field: text }] }
          : n,
      key,
    );

  it("merges keystrokes in one field into one undo step", () => {
    let s = initState(flow());
    for (const t of ["l", "la", "lab"]) s = type(s, t);
    expect(s.past).toHaveLength(1);
    s = editorReducer(s, { type: "undo" });
    expect(serialize(s)).toEqual(flow());
  });

  it("starts a new undo step for another field or after another action", () => {
    let s = initState(flow());
    s = type(s, "a");
    s = type(s, "ab", "steps[0].by");
    expect(s.past).toHaveLength(2);
    s = editorReducer(s, {
      type: "nodeChanges",
      changes: [{ type: "select", id: "t", selected: true }],
    });
    s = type(s, "abc", "steps[0].by");
    expect(s.past).toHaveLength(3);
  });

  it("keeps merging while only canvas measurements arrive in between", () => {
    let s = initState(flow());
    s = type(s, "a");
    s = editorReducer(s, {
      type: "nodeChanges",
      changes: [
        {
          type: "dimensions",
          id: "t",
          dimensions: { width: 240, height: 200 },
        },
      ],
    });
    s = type(s, "ab");
    expect(s.past).toHaveLength(1);
  });

  it("does not merge edits without a key", () => {
    let s = initState(flow());
    s = edit(s, (n) => n);
    s = edit(s, (n) => n);
    expect(s.past).toHaveLength(2);
  });
});
