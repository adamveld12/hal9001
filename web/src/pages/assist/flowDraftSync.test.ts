import { describe, expect, it } from "vitest";
import type { Flow, FlowNode } from "../../flow/model";
import {
  afterPush,
  applyDraft,
  flowKey,
  initFlowSync,
  keepMineVersion,
  needsPush,
  normalizeFlow,
  pendingUpdate,
  takeAiVersion,
  type FlowSyncState,
} from "./flowDraftSync";

const node = (id: string, ui?: { x: number; y: number }): FlowNode => ({
  id,
  type: "command",
  lang: "bash",
  script: "echo",
  ...(ui ? { ui } : {}),
});

function flow(over: Partial<Flow> = {}): Flow {
  return {
    id: "triage",
    enabled: false,
    dry_run: true,
    subscribe: ["gh"],
    nodes: [node("a", { x: 0, y: 0 }), node("b", { x: 300, y: 0 })],
    edges: [{ from: "a", to: "b" }],
    ...over,
  };
}

const draft = (hash: string, by: "agent" | "user" = "agent") => ({
  hash,
  yaml: "",
  updated_by: by,
});

/** A canvas that has loaded `f`: it reports the flow as it serializes it. */
const canvas = (f: Flow) => normalizeFlow(f);

/** State after the first draft was loaded into the canvas. */
function loaded(f: Flow = flow()): FlowSyncState {
  return applyDraft(initFlowSync(), draft("h1"), f, null);
}

/** The canvas after the user moved node `b`. */
const moved = (f: Flow): Flow => ({
  ...f,
  nodes: f.nodes.map((n) =>
    n.id === "b" ? { ...n, ui: { x: 10, y: 99 } } : n,
  ),
});

describe("the first draft", () => {
  it("is loaded into the canvas", () => {
    const s = loaded();
    expect(s.shown?.id).toBe("triage");
    expect(s.rev).toBe(1);
    expect(s.sync.syncedHash).toBe("h1");
    expect(needsPush(s, canvas(flow()))).toBe(false);
  });
});

describe("agent updates", () => {
  it("re-render the canvas when the user has not edited", () => {
    const s = loaded();
    const next = flow({
      nodes: [...flow().nodes, node("c", { x: 600, y: 0 })],
    });
    const r = applyDraft(s, draft("h2"), next, canvas(flow()));
    expect(r.rev).toBe(s.rev + 1);
    expect(r.shown?.nodes.map((n) => n.id)).toEqual(["a", "b", "c"]);
    expect(r.sync.syncedHash).toBe("h2");
  });

  it("wait for a decision when the user has unsent edits", () => {
    const s = loaded();
    const mine = moved(canvas(flow()));
    const next = flow({ id: "triage-2" });
    const r = applyDraft(s, draft("h2"), next, mine);
    expect(r.rev).toBe(s.rev);
    expect(pendingUpdate(r, mine)?.id).toBe("triage-2");
    // Pushing waits for the decision, so the agent's draft is not overwritten.
    expect(needsPush(r, mine)).toBe(false);

    const taken = takeAiVersion(r, mine);
    expect(taken.shown?.id).toBe("triage-2");
    expect(taken.rev).toBe(r.rev + 1);
    expect(pendingUpdate(taken, canvas(next))).toBeNull();

    const kept = keepMineVersion(r);
    expect(pendingUpdate(kept, mine)).toBeNull();
    expect(kept.sync.syncedHash).toBe("h2");
    expect(needsPush(kept, mine)).toBe(true);
  });
});

describe("user edits", () => {
  it("are pushed once and then match the draft", () => {
    const s = loaded();
    const mine = moved(canvas(flow()));
    expect(needsPush(s, mine)).toBe(true);
    expect(s.sync.syncedHash).toBe("h1");

    const after = afterPush(s, { hash: "h2" }, mine);
    expect(needsPush(after, mine)).toBe(false);
    expect(after.sync.syncedHash).toBe("h2");
  });

  it("do not come back as an agent update when the draft is echoed", () => {
    const mine = moved(canvas(flow()));
    const after = afterPush(loaded(), { hash: "h2" }, mine);
    const echoed = applyDraft(after, draft("h2", "user"), mine, mine);
    expect(echoed.rev).toBe(after.rev);
    expect(echoed.sync.pending).toBeNull();
    expect(needsPush(echoed, mine)).toBe(false);
  });
});

describe("echo suppression", () => {
  it("treats a draft the canvas would draw the same way as no change", () => {
    const s = loaded();
    // Same flow with another key order, an explicit default port, and no `ui`:
    // the daemon's YAML of it parses to this.
    const reordered: Flow = {
      edges: [{ from: "a", to: "b", port: "out" }],
      nodes: [
        {
          script: "echo",
          lang: "bash",
          type: "command",
          id: "a",
          ui: { x: 0, y: 0 },
        },
        {
          script: "echo",
          lang: "bash",
          type: "command",
          id: "b",
          ui: { x: 300, y: 0 },
        },
      ],
      subscribe: ["gh"],
      dry_run: true,
      enabled: false,
      id: "triage",
    };
    expect(flowKey(reordered)).toBe(flowKey(flow()));
    const r = applyDraft(s, draft("h2"), reordered, canvas(flow()));
    expect(r.rev).toBe(s.rev);
    expect(r.sync.pending).toBeNull();
    expect(r.sync.syncedHash).toBe("h2");
    expect(needsPush(r, canvas(flow()))).toBe(false);
  });
});

describe("layout", () => {
  it("places nodes without a position and keeps the ones that have one", () => {
    const f = flow({
      nodes: [node("a", { x: 40, y: 50 }), node("b"), node("c")],
      edges: [
        { from: "a", to: "b" },
        { from: "b", to: "c" },
      ],
    });
    const byId = new Map(normalizeFlow(f).nodes.map((n) => [n.id, n.ui]));
    expect(byId.get("a")).toEqual({ x: 40, y: 50 });
    const b = byId.get("b");
    const c = byId.get("c");
    expect(b).toBeDefined();
    expect(c).toBeDefined();
    expect(c?.x).toBeGreaterThan(b?.x ?? 0);
  });

  it("does not make a draft without positions an update of a canvas that laid it out", () => {
    const bare = flow({ nodes: [node("a"), node("b")] });
    const s = loaded(bare);
    const r = applyDraft(s, draft("h2"), bare, canvas(bare));
    expect(r.rev).toBe(s.rev);
    expect(needsPush(r, canvas(bare))).toBe(false);
  });
});

describe("a draft that does not parse", () => {
  it("keeps the last good graph and flags it", () => {
    const s = loaded();
    const r = applyDraft(s, draft("h2"), null, canvas(flow()));
    expect(r.shown).toBe(s.shown);
    expect(r.rev).toBe(s.rev);
    expect(r.invalid).toBe(true);
    expect(needsPush(r, canvas(flow()))).toBe(false);
  });

  it("is replaced by the user's next edit", () => {
    const broken = applyDraft(loaded(), draft("h2"), null, canvas(flow()));
    const mine = moved(canvas(flow()));
    expect(needsPush(broken, mine)).toBe(true);
    expect(broken.sync.syncedHash).toBe("h2");
    const after = afterPush(broken, { hash: "h3" }, mine);
    expect(after.invalid).toBe(false);
  });

  it("is followed by the agent's fixed version", () => {
    const broken = applyDraft(loaded(), draft("h2"), null, canvas(flow()));
    const fixed = flow({ id: "fixed" });
    const r = applyDraft(broken, draft("h3"), fixed, canvas(flow()));
    expect(r.invalid).toBe(false);
    expect(r.shown?.id).toBe("fixed");
    expect(r.rev).toBe(broken.rev + 1);
  });

  it("does not overwrite an agent update that still waits", () => {
    const s = loaded();
    const mine = moved(canvas(flow()));
    const waiting = applyDraft(s, draft("h2"), flow({ id: "x" }), mine);
    const r = applyDraft(waiting, draft("h3"), null, mine);
    expect(r.invalid).toBe(true);
    expect(needsPush(r, mine)).toBe(false);
  });
});
