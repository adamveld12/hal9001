import { describe, expect, it } from "vitest";
import {
  activeFilterCount,
  DEFAULT_FILTERS,
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
} from "./filters";

describe("inbox filters", () => {
  it("round-trips through URL params and keeps the default URL empty", () => {
    expect(filtersToParams(DEFAULT_FILTERS).toString()).toBe("");
    const f = {
      kind: "action" as const,
      unread: true,
      starred: true,
      archived: false,
      priority: "high" as const,
      dryRun: "hide" as const,
      q: "deploy",
      sort: "time" as const,
    };
    expect(filtersFromParams(filtersToParams(f))).toEqual(f);
  });

  it("ignores unknown parameter values", () => {
    const f = filtersFromParams(
      new URLSearchParams("priority=critical&dry=maybe&sort=oldest&unread=yes"),
    );
    expect(f).toEqual(DEFAULT_FILTERS);
  });

  it("maps filters to API query parameters", () => {
    expect(filtersToQuery(DEFAULT_FILTERS)).toEqual({ archived: "false" });
    expect(
      filtersToQuery({
        ...DEFAULT_FILTERS,
        unread: true,
        starred: true,
        archived: true,
        priority: "urgent",
        dryRun: "only",
        q: "  pr  ",
      }),
    ).toEqual({
      read: false,
      starred: true,
      archived: "true",
      priority: "urgent",
      dry_run: true,
      q: "pr",
    });
    expect(filtersToQuery({ ...DEFAULT_FILTERS, dryRun: "hide" }).dry_run).toBe(
      false,
    );
  });

  it("counts active filters but not the sort order", () => {
    expect(activeFilterCount({ ...DEFAULT_FILTERS, sort: "time" })).toBe(0);
    expect(
      activeFilterCount({ ...DEFAULT_FILTERS, unread: true, q: "x" }),
    ).toBe(2);
  });
});
