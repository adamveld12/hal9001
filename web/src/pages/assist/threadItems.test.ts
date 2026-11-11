import { describe, expect, it } from "vitest";
import type { AssistMessage } from "../../api/assist";
import type { SandboxLogEntry } from "../runs/sandboxEntries";
import {
  sandboxGroupHasProblem,
  sandboxGroupLabel,
  splitRunRefs,
  systemTone,
  threadItems,
  type ThreadItem,
} from "./threadItems";

const msg = (id: string, at: string) =>
  ({ id, role: "assistant", created_at: at }) as AssistMessage;

const entry = (id: number, ts: string, extra: Partial<SandboxLogEntry> = {}) =>
  ({
    id,
    session_id: "S1",
    ts,
    kind: "exec",
    level: "info",
    title: `entry ${id}`,
    ...extra,
  }) as SandboxLogEntry;

const shape = (items: ThreadItem[]) =>
  items.map((i) =>
    i.type === "message" ? i.message.id : i.entries.map((e) => e.id).join("+"),
  );

describe("threadItems", () => {
  it("is the messages when there is no sandbox log", () => {
    expect(
      shape(
        threadItems(
          [msg("a", "2026-01-01T00:00:01Z"), msg("b", "2026-01-01T00:00:02Z")],
          [],
        ),
      ),
    ).toEqual(["a", "b"]);
  });

  it("places entries between the messages around their time and groups neighbours", () => {
    const items = threadItems(
      [msg("a", "2026-01-01T00:00:01Z"), msg("b", "2026-01-01T00:00:10Z")],
      [
        entry(1, "2026-01-01T00:00:00Z"),
        entry(2, "2026-01-01T00:00:03Z"),
        entry(3, "2026-01-01T00:00:04Z"),
        entry(4, "2026-01-01T00:00:12Z"),
      ],
    );
    expect(shape(items)).toEqual(["1", "a", "2+3", "b", "4"]);
  });

  it("puts an entry made at the time of a message after it", () => {
    const items = threadItems(
      [msg("a", "2026-01-01T00:00:01Z")],
      [entry(1, "2026-01-01T00:00:01Z")],
    );
    expect(shape(items)).toEqual(["a", "1"]);
  });

  it("orders entries by time even when ids disagree, and with no messages at all", () => {
    expect(
      shape(
        threadItems(
          [],
          [entry(2, "2026-01-01T00:00:01Z"), entry(1, "2026-01-01T00:00:02Z")],
        ),
      ),
    ).toEqual(["2+1"]);
  });

  it("keys a group by its first entry", () => {
    const [item] = threadItems([], [entry(7, "2026-01-01T00:00:01Z")]);
    expect(item).toMatchObject({ type: "sandbox", key: "sandbox:7" });
  });
});

describe("sandbox group summary", () => {
  it("shows the title of one entry and the count of several", () => {
    expect(sandboxGroupLabel([entry(1, "t", { title: "ls -la" })])).toBe(
      "ls -la",
    );
    expect(
      sandboxGroupLabel([
        entry(1, "t", { title: "ls" }),
        entry(2, "t", { title: "cat" }),
      ]),
    ).toBe("2 entries: ls");
  });

  it("marks failed, refused and timed out commands", () => {
    expect(sandboxGroupHasProblem([entry(1, "t")])).toBe(false);
    expect(sandboxGroupHasProblem([entry(1, "t", { exit_code: 2 })])).toBe(
      true,
    );
    expect(sandboxGroupHasProblem([entry(1, "t", { kind: "denied" })])).toBe(
      true,
    );
    expect(sandboxGroupHasProblem([entry(1, "t", { level: "error" })])).toBe(
      true,
    );
    expect(sandboxGroupHasProblem([entry(1, "t", { timed_out: true })])).toBe(
      true,
    );
  });
});

describe("splitRunRefs", () => {
  const id = "01JC7Z3K9Q8V5T2M4N6P8R0S1T";

  it("splits the divider text at the run id", () => {
    expect(
      splitRunRefs(`Continued from run ${id}; earlier context replayed`),
    ).toEqual([
      { type: "text", text: "Continued from run " },
      { type: "run", id },
      { type: "text", text: "; earlier context replayed" },
    ]);
  });

  it("leaves text without a run id as it is", () => {
    expect(splitRunRefs("The sandbox was recreated.")).toEqual([
      { type: "text", text: "The sandbox was recreated." },
    ]);
    expect(splitRunRefs("run abc")).toEqual([
      { type: "text", text: "run abc" },
    ]);
  });
});

describe("systemTone", () => {
  it("shows a system message of a chat as an error", () => {
    expect(systemTone("chat", "The turn failed")).toBe("error");
  });

  it("shows how a flow run ended as a note unless it did not succeed", () => {
    expect(systemTone("flow", "Run finished.")).toBe("note");
    expect(systemTone("flow", "Run finished. Emitted on `ok`.")).toBe("note");
    expect(systemTone("flow", "The proposal was approved.")).toBe("note");
    expect(systemTone("flow", "hal sent the agent a follow-up: x")).toBe(
      "note",
    );
    expect(systemTone("flow", "Run failed: exit 1")).toBe("error");
    expect(systemTone("flow", "Run timeout.")).toBe("error");
    expect(systemTone("flow", null)).toBe("note");
  });
});
