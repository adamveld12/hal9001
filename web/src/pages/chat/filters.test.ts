import { describe, expect, it } from "vitest";
import {
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
  hasActiveFilters,
  NO_CHAT_FILTERS,
} from "./filters";

const params = (s: string) => new URLSearchParams(s);

describe("filtersFromParams", () => {
  it("is empty without a query", () => {
    expect(filtersFromParams(params(""))).toEqual(NO_CHAT_FILTERS);
    expect(hasActiveFilters(filtersFromParams(params("")))).toBe(false);
  });

  it("reads every filter", () => {
    const f = filtersFromParams(
      params(
        "source=flow&flow=triage&runtime=claude&status=awaiting_approval&since=2026-11-01&until=2026-11-05&q=out+of+memory",
      ),
    );
    expect(f).toEqual({
      source: "flow",
      flow: "triage",
      runtime: "claude",
      status: "awaiting_approval",
      since: "2026-11-01",
      until: "2026-11-05",
      q: "out of memory",
    });
    expect(hasActiveFilters(f)).toBe(true);
  });

  it("drops unknown values and impossible dates", () => {
    const f = filtersFromParams(
      params("source=other&status=nope&since=2026-02-30&until=yesterday"),
    );
    expect(f).toEqual(NO_CHAT_FILTERS);
  });
});

describe("filtersToParams", () => {
  it("round-trips and keeps unrelated keys", () => {
    const f = { ...NO_CHAT_FILTERS, source: "chat" as const, q: "a b" };
    const p = filtersToParams(
      f,
      params("assist=01JC&source=flow&status=failed"),
    );
    expect(p.get("source")).toBe("chat");
    expect(p.get("q")).toBe("a b");
    expect(p.get("status")).toBeNull();
    expect(p.get("assist")).toBe("01JC");
    expect(filtersFromParams(p)).toEqual(f);
  });
});

describe("filtersToQuery", () => {
  it("sends only what is set", () => {
    expect(filtersToQuery(NO_CHAT_FILTERS)).toEqual({});
    expect(
      filtersToQuery({ ...NO_CHAT_FILTERS, flow: "triage", q: "  x " }),
    ).toEqual({ flow: "triage", q: "x" });
  });

  it("covers the whole last day: since is its start, until the start of the next day", () => {
    const q = filtersToQuery({
      ...NO_CHAT_FILTERS,
      since: "2026-11-01",
      until: "2026-11-30",
    });
    expect(q.since).toBe(new Date(2026, 10, 1).toISOString());
    expect(q.until).toBe(new Date(2026, 11, 1).toISOString());
  });
});
