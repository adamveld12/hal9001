import { describe, expect, it } from "vitest";
import {
  canConnect,
  editorReducer,
  initState,
  isDirty,
  serialize,
  type EditorAction,
} from "./editorState";
import { triageGithub } from "./fixtures";
import { emptyFlow } from "./model";

function run(flow = triageGithub(), ...actions: EditorAction[]) {
  return actions.reduce(editorReducer, initState(flow));
}

describe("editor reducer", () => {
  it("starts clean and becomes dirty after an edit, clean again after markSaved", () => {
    let s = initState(triageGithub());
    expect(isDirty(s)).toBe(false);
    s = editorReducer(s, { type: "updateMeta", patch: { enabled: false } });
    expect(isDirty(s)).toBe(true);
    s = editorReducer(s, { type: "markSaved", flow: serialize(s) });
    expect(isDirty(s)).toBe(false);
  });

  it("connects declared ports and rejects cycles, duplicates, unknown ports, and sensor targets", () => {
    const s = run(triageGithub(), {
      type: "addSensor",
      sensor: "gh",
      position: { x: 0, y: 0 },
    });
    expect(
      canConnect(s, { source: "review-small", target: "only-review-requests" }),
    ).toBe(false); // cycle
    expect(
      canConnect(s, {
        source: "classify",
        sourceHandle: "small",
        target: "review-small",
      }),
    ).toBe(false); // duplicate
    expect(
      canConnect(s, {
        source: "classify",
        sourceHandle: "medium",
        target: "note-large",
      }),
    ).toBe(false);
    expect(
      canConnect(s, {
        source: "classify",
        sourceHandle: "large",
        target: "sensor:gh",
      }),
    ).toBe(false);
    expect(canConnect(s, { source: "sensor:gh", target: "classify" })).toBe(
      false,
    );
    expect(
      canConnect(s, {
        source: "review-small",
        sourceHandle: "denied",
        target: "note-large",
      }),
    ).toBe(true);
  });

  it("builds a four-node flow by hand and serializes it", () => {
    let s = initState({ ...emptyFlow("hand-made") });
    const add = (
      nodeType: "command" | "agent" | "inbox",
      x: number,
    ): EditorAction => ({
      type: "addNode",
      nodeType,
      position: { x, y: 10 },
    });
    s = [
      { type: "addSensor", sensor: "gh", position: { x: -300, y: 0 } },
      add("command", 0),
      add("agent", 300),
      add("inbox", 600),
      add("inbox", 600),
    ].reduce((st, a) => editorReducer(st, a as EditorAction), s);
    s = editorReducer(s, {
      type: "connect",
      connection: {
        source: "command",
        sourceHandle: "out",
        target: "agent",
        targetHandle: "in",
      },
    });
    s = editorReducer(s, {
      type: "connect",
      connection: {
        source: "agent",
        sourceHandle: "out",
        target: "inbox",
        targetHandle: "in",
      },
    });
    s = editorReducer(s, {
      type: "connect",
      connection: {
        source: "agent",
        sourceHandle: "error",
        target: "inbox-2",
        targetHandle: "in",
      },
    });
    const flow = serialize(s);
    expect(flow.subscribe).toEqual(["gh"]);
    expect(flow.nodes.map((n) => n.id)).toEqual([
      "command",
      "agent",
      "inbox",
      "inbox-2",
    ]);
    expect(flow.edges).toEqual([
      { from: "command", to: "agent" },
      { from: "agent", to: "inbox" },
      { from: "agent", port: "error", to: "inbox-2" },
    ]);
    expect(flow.nodes[1].ui).toEqual({ x: 300, y: 10 });
  });

  it("removing a node removes its edges and records one undoable step", () => {
    let s = run(triageGithub(), {
      type: "nodeChanges",
      changes: [{ type: "remove", id: "classify" }],
    });
    expect(serialize(s).edges).toEqual([]);
    expect(serialize(s).nodes.map((n) => n.id)).not.toContain("classify");
    s = editorReducer(s, { type: "undo" });
    expect(serialize(s)).toEqual(triageGithub());
    s = editorReducer(s, { type: "redo" });
    expect(serialize(s).nodes.map((n) => n.id)).not.toContain("classify");
  });

  it("renames a node through its edges and refuses taken or empty ids", () => {
    let s = run(triageGithub(), {
      type: "renameNode",
      id: "classify",
      to: "router",
    });
    expect(serialize(s).edges).toEqual([
      { from: "only-review-requests", to: "router" },
      { from: "router", port: "small", to: "review-small" },
      { from: "router", port: "large", to: "note-large" },
    ]);
    const before = s;
    s = editorReducer(s, {
      type: "renameNode",
      id: "router",
      to: "note-large",
    });
    s = editorReducer(s, { type: "renameNode", id: "router", to: "  " });
    expect(s).toBe(before);
  });

  it("records a drag as a single history step", () => {
    const pos = (x: number, dragging: boolean): EditorAction => ({
      type: "nodeChanges",
      changes: [
        { type: "position", id: "classify", position: { x, y: 80 }, dragging },
      ],
    });
    let s = [
      pos(410, true),
      pos(420, true),
      pos(430, true),
      pos(430, false),
    ].reduce(editorReducer, initState(triageGithub()));
    expect(serialize(s).nodes[1].ui).toEqual({ x: 430, y: 80 });
    expect(s.past).toHaveLength(1);
    s = editorReducer(s, { type: "undo" });
    expect(serialize(s).nodes[1].ui).toEqual({ x: 400, y: 80 });
  });

  it("dropping a sensor node unsubscribes", () => {
    const s = run(triageGithub(), {
      type: "nodeChanges",
      changes: [{ type: "remove", id: "sensor:github-notifications" }],
    });
    expect(serialize(s).subscribe).toEqual([]);
  });

  it("changing node type-specific config keeps the node's edges", () => {
    const s = run(triageGithub(), {
      type: "updateNode",
      id: "classify",
      patch: (n) =>
        n.type === "command" ? { ...n, ports: ["small", "large", "huge"] } : n,
    });
    expect(
      canConnect(s, {
        source: "classify",
        sourceHandle: "huge",
        target: "note-large",
      }),
    ).toBe(true);
    expect(serialize(s).edges).toHaveLength(3);
  });
});
