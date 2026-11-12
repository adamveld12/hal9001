import { describe, expect, it } from "vitest";
import type { Conversation } from "../../api/conversations";
import { capPageContext } from "../../lib/pageContext";
import { chatPageContext } from "./context";
import { NO_CHAT_FILTERS } from "./filters";

const conv = (over: Partial<Conversation>): Conversation =>
  ({
    id: "01A",
    source: "flow",
    title: "triage / ask",
    status: "succeeded",
    runtime: "claude",
    flow_id: "triage",
    node_id: "ask",
    forked_from: null,
    fork_count: 0,
    ...over,
  }) as Conversation;

describe("chatPageContext", () => {
  it("names the filters, the counts and the selected conversation", () => {
    const selected = conv({ id: "01B", fork_count: 2 });
    const ctx = chatPageContext({
      filters: { ...NO_CHAT_FILTERS, source: "flow", q: "memory" },
      listed: [
        conv({ id: "01A" }),
        selected,
        conv({ id: "01C", status: "failed" }),
      ],
      hasMore: true,
      selected,
      starting: false,
    });
    expect(ctx.title).toBe("Chat: triage / ask");
    const facts = Object.fromEntries(
      ctx.facts?.map((f) => [f.label, f.value]) ?? [],
    );
    expect(facts["source filter"]).toBe("flow");
    expect(facts["q filter"]).toBe("memory");
    expect(facts["status filter"]).toBeUndefined();
    expect(facts["conversations loaded"]).toBe("3");
    expect(facts["more pages"]).toBe("true");
    expect(facts["selected conversation"]).toBe("01B");
    expect(facts["selected flow / node"]).toBe("triage / ask");
    expect(facts["forks"]).toBe("2");
    expect(ctx.entities?.[0]?.id).toBe("01B");
    expect(ctx.entities?.filter((e) => e.id === "01B")).toHaveLength(1);
  });

  it("tells the assistant which run filters the list", () => {
    const ctx = chatPageContext({
      filters: { ...NO_CHAT_FILTERS, flow_run: "01JC7Z3K9Q8V5T2M4N6P8R0S1T" },
      listed: [],
      hasMore: false,
      selected: null,
      starting: false,
    });
    const facts = Object.fromEntries(
      ctx.facts?.map((f) => [f.label, f.value]) ?? [],
    );
    expect(facts["flow_run filter"]).toBe("01JC7Z3K9Q8V5T2M4N6P8R0S1T");
  });

  it("describes the page without a selection", () => {
    const ctx = chatPageContext({
      filters: NO_CHAT_FILTERS,
      listed: [],
      hasMore: false,
      selected: null,
      starting: true,
    });
    expect(ctx.title).toBe("Chat");
    expect(ctx.summary).toContain("new chat");
  });

  it("fits the page context cap with 30 rows", () => {
    const listed = Array.from({ length: 30 }, (_, i) =>
      conv({ id: `01${String(i).padStart(24, "0")}`, title: "x".repeat(200) }),
    );
    const ctx = capPageContext(
      "/chat",
      chatPageContext({
        filters: NO_CHAT_FILTERS,
        listed,
        hasMore: true,
        selected: null,
        starting: false,
      }),
    );
    expect(JSON.stringify(ctx).length).toBeLessThanOrEqual(3072);
  });
});
