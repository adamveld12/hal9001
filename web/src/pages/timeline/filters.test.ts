import { describe, expect, it } from "vitest";
import {
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
  hasActiveFilters,
  NO_FILTERS,
} from "./filters";

describe("timeline filters", () => {
  it("defaults to no filters and an empty server query (test runs stay hidden)", () => {
    const f = filtersFromParams(new URLSearchParams());
    expect(f).toEqual(NO_FILTERS);
    expect(hasActiveFilters(f)).toBe(false);
    expect(filtersToQuery(f)).toEqual({});
    expect(filtersToParams(f).toString()).toBe("");
  });

  it("round-trips every filter through URL params", () => {
    const f = {
      sensor: "gh",
      flow: "triage",
      status: "failed",
      decision: "dropped",
      kind: "test",
      q: "needs review",
    } as const;
    expect(filtersFromParams(filtersToParams(f))).toEqual(f);
    expect(hasActiveFilters(f)).toBe(true);
  });

  it("drops enum values the API does not define", () => {
    const f = filtersFromParams(
      new URLSearchParams("status=bogus&decision=maybe&kind=prod&sensor=x"),
    );
    expect(f.status).toBe("");
    expect(f.decision).toBe("");
    expect(f.kind).toBe("");
    expect(f.sensor).toBe("x");
  });

  it("maps filters to API query names and trims search text", () => {
    expect(
      filtersToQuery({
        sensor: "gh",
        flow: "triage",
        status: "running",
        decision: "emitted",
        kind: "replay",
        q: "  foo ",
      }),
    ).toEqual({
      sensor_id: "gh",
      flow_id: "triage",
      status: "running",
      decision: "emitted",
      kind: "replay",
      q: "foo",
    });
  });

  it("omits a whitespace-only search", () => {
    expect(filtersToQuery({ ...NO_FILTERS, q: "   " })).toEqual({});
  });
});
