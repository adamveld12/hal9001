import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../api/inbox";
import {
  agentChatsLink,
  groupByPriority,
  safeHref,
  tracePath,
  withPinned,
} from "./model";

const mk = (id: string, priority: InboxItem["priority"]) =>
  ({ id, priority }) as InboxItem;

describe("groupByPriority", () => {
  it("makes one group per run of equal priority, in order", () => {
    const groups = groupByPriority([
      mk("1", "urgent"),
      mk("2", "urgent"),
      mk("3", "normal"),
      mk("4", "low"),
    ]);
    expect(groups.map((g) => [g.priority, g.items.length])).toEqual([
      ["urgent", 2],
      ["normal", 1],
      ["low", 1],
    ]);
  });

  it("returns nothing for an empty list", () => {
    expect(groupByPriority([])).toEqual([]);
  });
});

describe("withPinned", () => {
  const list = [mk("a", "normal"), mk("b", "normal"), mk("c", "normal")];

  it("re-inserts the open item at its old position", () => {
    const out = withPinned([list[0]!, list[2]!], list[1]!, 1);
    expect(out.map((i) => i.id)).toEqual(["a", "b", "c"]);
  });

  it("clamps the position to the list", () => {
    expect(withPinned([list[0]!], list[1]!, 9).map((i) => i.id)).toEqual([
      "a",
      "b",
    ]);
  });

  it("leaves the list alone when the item is present or nothing is pinned", () => {
    expect(withPinned(list, list[1]!, 0)).toEqual(list);
    expect(withPinned(list, null, 0)).toEqual(list);
  });
});

describe("safeHref", () => {
  it("allows web and mail links", () => {
    expect(safeHref("https://github.com/o/r/pull/1")).toBe(
      "https://github.com/o/r/pull/1",
    );
    expect(safeHref("mailto:a@example.com")).toBe("mailto:a@example.com");
  });

  it("rejects script URLs and non-URLs", () => {
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref("data:text/html,<b>x</b>")).toBeNull();
    expect(safeHref("not a url")).toBeNull();
  });
});

describe("tracePath", () => {
  it("opens the agent run for agent notes and the flow run otherwise", () => {
    expect(
      tracePath({ source: "agent_run", agent_run_id: "A1", flow_run_id: "F1" }),
    ).toBe("/agent-runs/A1");
    expect(
      tracePath({ source: "node_run", agent_run_id: null, flow_run_id: "F1" }),
    ).toBe("/runs/F1");
    expect(
      tracePath({ source: "agent_run", agent_run_id: null, flow_run_id: "F1" }),
    ).toBe("/runs/F1");
  });
});

describe("agentChatsLink", () => {
  const note = (over: Partial<InboxItem>) =>
    ({
      source: "node_run",
      flow_run_id: "01JC7Z3K9Q8V5T2M4N6P8R0S1T",
      agent_chats: 3,
      ...over,
    }) as InboxItem;

  it("labels the count and opens the Chat list filtered to the run", () => {
    expect(agentChatsLink(note({}))).toEqual({
      label: "Agent chats (3)",
      to: "/chat?flow_run=01JC7Z3K9Q8V5T2M4N6P8R0S1T",
    });
  });

  it("shows the link for a single conversation too", () => {
    expect(agentChatsLink(note({ agent_chats: 1 }))?.label).toBe(
      "Agent chats (1)",
    );
  });

  it("is absent for a run without agents", () => {
    expect(agentChatsLink(note({ agent_chats: 0 }))).toBeNull();
  });

  it("is absent for a note that is not from a flow", () => {
    expect(
      agentChatsLink(note({ source: "mcp", flow_run_id: "", agent_chats: 0 })),
    ).toBeNull();
    expect(agentChatsLink(note({ source: "mcp" }))).toBeNull();
  });
});
