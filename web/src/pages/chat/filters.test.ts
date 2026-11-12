import { describe, expect, it } from "vitest";
import {
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
  clearBarFilters,
  hasActiveFilters,
  hasBarFilters,
  NO_CHAT_FILTERS,
  runBanner,
  runRelation,
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
        "source=flow&flow=triage&flow_run=01JC7Z3K9Q8V5T2M4N6P8R0S1T&runtime=claude&status=awaiting_approval&since=2026-11-01&until=2026-11-05&q=out+of+memory",
      ),
    );
    expect(f).toEqual({
      source: "flow",
      flow: "triage",
      flow_run: "01JC7Z3K9Q8V5T2M4N6P8R0S1T",
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

const RUN = "01JC7Z3K9Q8V5T2M4N6P8R0S1T";

describe("the flow_run filter", () => {
  it("is read from the URL, sent to the API and kept in the query", () => {
    const f = filtersFromParams(params(`flow_run=${RUN}&source=flow`));
    expect(f.flow_run).toBe(RUN);
    expect(hasActiveFilters(f)).toBe(true);
    expect(filtersToQuery(f)).toEqual({ flow_run: RUN, source: "flow" });
    expect(filtersToParams(f).get("flow_run")).toBe(RUN);
  });

  it("ignores a value that is not a run id", () => {
    expect(filtersFromParams(params("flow_run=nope")).flow_run).toBe("");
    expect(
      filtersFromParams(params(`flow_run=${RUN.toLowerCase()}`)).flow_run,
    ).toBe("");
  });

  it("stays set when the filter bar is cleared, and is not a bar filter", () => {
    const f = { ...NO_CHAT_FILTERS, flow_run: RUN };
    expect(hasBarFilters(f)).toBe(false);
    expect(hasBarFilters({ ...f, status: "failed" })).toBe(true);
    expect(clearBarFilters({ ...f, status: "failed", q: "x" })).toEqual(f);
  });
});

describe("runBanner", () => {
  it("names the run and its flow, and links the run page", () => {
    expect(runBanner(RUN, "triage")).toEqual({
      text: `Run ${RUN.slice(-8)} of triage`,
      runPath: `/runs/${RUN}`,
    });
  });

  it("omits the flow until it is known", () => {
    expect(runBanner(RUN, null)?.text).toBe(`Run ${RUN.slice(-8)}`);
  });

  it("is absent without a run filter", () => {
    expect(runBanner("", "triage")).toBeNull();
  });
});

describe("runRelation", () => {
  const c = (flow_run_id: string | null, forked: boolean) => ({
    flow_run_id,
    forked_from: forked
      ? { agent_run_id: "ar", flow_id: null, flow_run_id: RUN, node_id: null }
      : null,
  });

  it("marks forks and conversations of continuation runs, not the run's own", () => {
    expect(runRelation(c(RUN, false), RUN)).toBeNull();
    expect(runRelation(c(RUN, true), RUN)).toBe("fork");
    expect(runRelation(c("01OTHER", false), RUN)).toBe("continuation");
    expect(runRelation(c("01OTHER", true), RUN)).toBe("fork");
  });

  it("marks nothing without a run filter", () => {
    expect(runRelation(c("01OTHER", false), "")).toBeNull();
    expect(runRelation(c(null, false), RUN)).toBeNull();
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
