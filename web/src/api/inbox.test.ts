import { describe, expect, it } from "vitest";
import {
  matchesInboxQuery,
  mergeInboxPages,
  patchInboxPages,
  type InboxItem,
  type InboxList,
} from "./inbox";

function item(over: Partial<InboxItem> & { id: string }): InboxItem {
  return {
    source: "agent_run",
    source_id: "src",
    flow_run_id: "fr",
    node_run_id: "nr",
    agent_run_id: "ar",
    flow_id: "triage",
    node_id: "note",
    event_id: null,
    title: "Title",
    priority: "normal",
    summary_md: "",
    suggested_reply: null,
    links: [],
    read: false,
    starred: false,
    archived: false,
    dry_run: false,
    parse_error: false,
    created_at: "2026-11-05T10:00:00Z",
    ...over,
  };
}

const page = (...items: InboxItem[]): InboxList => ({
  items,
  next_cursor: null,
});

describe("mergeInboxPages", () => {
  const old = item({
    id: "1",
    priority: "urgent",
    created_at: "2026-11-05T08:00:00Z",
  });
  const fresh = item({
    id: "2",
    priority: "normal",
    created_at: "2026-11-05T11:00:00Z",
  });
  const freshHigh = item({
    id: "3",
    priority: "high",
    created_at: "2026-11-05T09:00:00Z",
  });

  it("merges pages in server order and lists an item once when it shifted between pages", () => {
    const merged = mergeInboxPages([
      page(fresh, freshHigh),
      page(freshHigh, old),
    ]);
    expect(merged.map((i) => i.id)).toEqual(["2", "3", "1"]);
  });
});

describe("matchesInboxQuery", () => {
  it("hides archived items unless archived=true or any", () => {
    const a = item({ id: "a", archived: true });
    expect(matchesInboxQuery(a, {})).toBe(false);
    expect(matchesInboxQuery(a, { archived: "false" })).toBe(false);
    expect(matchesInboxQuery(a, { archived: "true" })).toBe(true);
    expect(matchesInboxQuery(a, { archived: "any" })).toBe(true);
    expect(matchesInboxQuery(item({ id: "b" }), { archived: "true" })).toBe(
      false,
    );
  });

  it("treats priority as a minimum", () => {
    const high = item({ id: "h", priority: "high" });
    expect(matchesInboxQuery(high, { priority: "normal" })).toBe(true);
    expect(matchesInboxQuery(high, { priority: "high" })).toBe(true);
    expect(matchesInboxQuery(high, { priority: "urgent" })).toBe(false);
  });

  it("filters read, starred, dry-run, flow and text", () => {
    const i = item({
      id: "x",
      read: true,
      dry_run: true,
      title: "Deploy Failed",
      summary_md: "see logs",
    });
    expect(matchesInboxQuery(i, { read: false })).toBe(false);
    expect(matchesInboxQuery(i, { starred: true })).toBe(false);
    expect(matchesInboxQuery(i, { dry_run: false })).toBe(false);
    expect(matchesInboxQuery(i, { flow_id: "other" })).toBe(false);
    expect(matchesInboxQuery(i, { q: "deploy" })).toBe(true);
    expect(matchesInboxQuery(i, { q: "LOGS" })).toBe(true);
    expect(matchesInboxQuery(i, { q: "missing" })).toBe(false);
  });
});

describe("patchInboxPages", () => {
  const a = item({ id: "a" });
  const b = item({ id: "b" });

  it("changes only the targeted item", () => {
    const out = patchInboxPages([page(a, b)], "a", { starred: true }, {});
    expect(out[0]?.items.map((i) => i.starred)).toEqual([true, false]);
  });

  it("removes an item that no longer matches the list filters", () => {
    const unread = patchInboxPages(
      [page(a, b)],
      "a",
      { read: true },
      { read: false },
    );
    expect(unread[0]?.items.map((i) => i.id)).toEqual(["b"]);
    const archived = patchInboxPages([page(a, b)], "b", { archived: true }, {});
    expect(archived[0]?.items.map((i) => i.id)).toEqual(["a"]);
  });

  it("keeps an item that still matches", () => {
    const out = patchInboxPages(
      [page(a)],
      "a",
      { read: true },
      { archived: "any" },
    );
    expect(out[0]?.items).toHaveLength(1);
  });
});
