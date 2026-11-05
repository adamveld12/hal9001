import { describe, expect, it } from "vitest";
import type { Schemas } from "../../api/client";
import {
  buildTranscript,
  chunkText,
  lastContiguousSeq,
  mergeEvents,
  toolTone,
} from "./transcript";

type Ev = Schemas["AgentEvent"];

function ev(
  seq: number,
  kind: string,
  payload: unknown,
  toolCallId: string | null = null,
): Ev {
  return {
    agent_run_id: "run",
    seq,
    ts: `2026-11-05T10:00:${String(seq).padStart(2, "0")}Z`,
    kind,
    tool_call_id: toolCallId,
    payload,
  };
}

const chunk = (seq: number, kind: string, text: string) =>
  ev(seq, kind, { content: { type: "text", text } });

describe("mergeEvents", () => {
  it("appends a newer page in order", () => {
    const prev = [chunk(1, "message", "a"), chunk(2, "message", "b")];
    const out = mergeEvents(prev, [chunk(3, "message", "c")]);
    expect(out.map((e) => e.seq)).toEqual([1, 2, 3]);
  });

  it("returns the same array when nothing arrives", () => {
    const prev = [chunk(1, "message", "a")];
    expect(mergeEvents(prev, [])).toBe(prev);
  });

  it("drops duplicates, keeps the later copy, and sorts out-of-order arrivals", () => {
    const prev = [chunk(1, "message", "a"), chunk(2, "message", "b")];
    const out = mergeEvents(prev, [
      chunk(4, "message", "d"),
      chunk(2, "message", "b2"),
      chunk(3, "message", "c"),
    ]);
    expect(out.map((e) => e.seq)).toEqual([1, 2, 3, 4]);
    expect(chunkText(out[1].payload)).toBe("b2");
  });

  it("does not mutate its inputs", () => {
    const prev = [chunk(2, "message", "b")];
    const incoming = [chunk(1, "message", "a"), chunk(2, "message", "b")];
    mergeEvents(prev, incoming);
    expect(prev).toHaveLength(1);
    expect(incoming.map((e) => e.seq)).toEqual([1, 2]);
  });
});

describe("lastContiguousSeq", () => {
  it("stops at the first gap so a refetch can repair it", () => {
    expect(lastContiguousSeq([])).toBe(0);
    expect(
      lastContiguousSeq([chunk(1, "message", "a"), chunk(2, "message", "b")]),
    ).toBe(2);
    expect(
      lastContiguousSeq([
        chunk(1, "message", "a"),
        chunk(2, "message", "b"),
        chunk(4, "message", "d"),
      ]),
    ).toBe(2);
    expect(lastContiguousSeq([chunk(2, "message", "b")])).toBe(0);
  });

  it("fills a gap when the missing event arrives later", () => {
    const first = mergeEvents(
      [],
      [chunk(1, "message", "a"), chunk(3, "message", "c")],
    );
    expect(lastContiguousSeq(first)).toBe(1);
    const healed = mergeEvents(first, [
      chunk(2, "message", "b"),
      chunk(3, "message", "c"),
    ]);
    expect(healed.map((e) => e.seq)).toEqual([1, 2, 3]);
    expect(lastContiguousSeq(healed)).toBe(3);
  });
});

describe("buildTranscript", () => {
  it("joins adjacent chunks of one kind into a single message", () => {
    const items = buildTranscript([
      chunk(1, "message", "Hel"),
      chunk(2, "message", "lo"),
      chunk(3, "thought", "hmm"),
      chunk(4, "message", "Bye"),
    ]);
    expect(items.map((i) => i.type)).toEqual(["message", "thought", "message"]);
    expect(items[0]).toMatchObject({ key: 1, text: "Hello" });
  });

  it("keeps item keys stable as chunks stream in", () => {
    const events = [chunk(1, "message", "a"), chunk(2, "message", "b")];
    const before = buildTranscript(events.slice(0, 1));
    const after = buildTranscript(events);
    expect(after[0].key).toBe(before[0].key);
  });

  it("marks non-text chunks instead of dropping them", () => {
    const items = buildTranscript([
      ev(1, "message", { content: { type: "image" } }),
    ]);
    expect(items[0]).toMatchObject({ type: "message", text: "[image]" });
  });

  it("folds tool_call_update into its tool_call, replacing the fields it carries", () => {
    const items = buildTranscript([
      ev(
        1,
        "tool_call",
        {
          toolCallId: "t1",
          title: "Edit a.go",
          kind: "edit",
          status: "pending",
          rawInput: { path: "a.go" },
        },
        "t1",
      ),
      chunk(2, "message", "working"),
      ev(
        3,
        "tool_call_update",
        {
          toolCallId: "t1",
          status: "completed",
          content: [
            { type: "diff", path: "a.go", oldText: "x\n", newText: "y\n" },
            { type: "content", content: { type: "text", text: "ok" } },
          ],
          rawOutput: { ok: true },
        },
        "t1",
      ),
    ]);
    expect(items.map((i) => i.type)).toEqual(["tool", "message"]);
    expect(items[0]).toMatchObject({
      type: "tool",
      key: 1,
      title: "Edit a.go",
      toolKind: "edit",
      status: "completed",
      input: { path: "a.go" },
      output: { ok: true },
      lastSeq: 3,
      content: [
        { type: "diff", path: "a.go", oldText: "x\n", newText: "y\n" },
        { type: "text", text: "ok" },
      ],
    });
  });

  it("shows an update whose tool_call is not in the log", () => {
    const items = buildTranscript([
      ev(5, "tool_call_update", { status: "failed" }, "orphan"),
    ]);
    expect(items[0]).toMatchObject({
      type: "tool",
      toolCallId: "orphan",
      status: "failed",
    });
  });

  it("keeps separate tool calls separate", () => {
    const items = buildTranscript([
      ev(1, "tool_call", { title: "A" }, "a"),
      ev(2, "tool_call", { title: "B" }, "b"),
      ev(3, "tool_call_update", { status: "completed" }, "a"),
    ]);
    expect(items).toHaveLength(2);
    expect(items[0]).toMatchObject({ title: "A", status: "completed" });
    expect(items[1]).toMatchObject({ title: "B", status: null });
  });

  it("replaces a plan with the next plan update", () => {
    const items = buildTranscript([
      ev(1, "plan", {
        entries: [{ content: "one", priority: "high", status: "pending" }],
      }),
      ev(2, "plan", {
        entries: [
          { content: "one", priority: "high", status: "completed" },
          { content: "two", priority: "low", status: "pending" },
        ],
      }),
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      type: "plan",
      entries: [
        { content: "one", status: "completed" },
        { content: "two", status: "pending" },
      ],
    });
  });

  it("passes unknown kinds through", () => {
    const items = buildTranscript([
      ev(1, "mode_update", { currentModeId: "code" }),
    ]);
    expect(items[0]).toMatchObject({ type: "other", kind: "mode_update" });
  });
});

describe("toolTone", () => {
  it("maps ACP tool statuses", () => {
    expect(toolTone("completed")).toBe("ok");
    expect(toolTone("failed")).toBe("bad");
    expect(toolTone("in_progress")).toBe("run");
    expect(toolTone(null)).toBe("muted");
  });
});
