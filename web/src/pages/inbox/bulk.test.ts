import { describe, expect, it } from "vitest";
import {
  confirmText,
  CLEAR_PRESETS,
  filtersToBulkFilter,
  pageSelection,
  presetFilter,
  pruneSelection,
  toggleAllOnPage,
  toggleSelected,
} from "./bulk";
import { DEFAULT_FILTERS, type InboxFilters } from "./filters";

const shown: InboxFilters = {
  ...DEFAULT_FILTERS,
  unread: true,
  priority: "high",
  dryRun: "hide",
  q: " deploy ",
};

describe("presetFilter", () => {
  it("maps each preset to a filter over unarchived items", () => {
    expect(presetFilter("read", DEFAULT_FILTERS)).toEqual({
      read: true,
      archived: "false",
    });
    expect(presetFilter("dry-run", DEFAULT_FILTERS)).toEqual({
      dry_run: true,
      archived: "false",
    });
    expect(presetFilter("low", DEFAULT_FILTERS)).toEqual({
      max_priority: "low",
      archived: "false",
    });
    expect(presetFilter("older-7d", DEFAULT_FILTERS)).toEqual({
      older_than: "7d",
      archived: "false",
    });
  });

  it("ignores the list filters except for 'shown'", () => {
    expect(presetFilter("read", shown)).toEqual(
      presetFilter("read", DEFAULT_FILTERS),
    );
  });

  it("'shown' mirrors the current filters, not the loaded rows", () => {
    expect(presetFilter("shown", shown)).toEqual({
      archived: "false",
      read: false,
      priority: "high",
      dry_run: false,
      q: "deploy",
    });
    expect(presetFilter("shown", DEFAULT_FILTERS)).toEqual({
      archived: "false",
    });
  });

  it("has a preset per id", () => {
    expect(CLEAR_PRESETS.map((p) => p.label)).toEqual([
      "Archive everything read",
      "Archive all dry-run items",
      "Archive low priority",
      "Archive older than 7 days",
      "Archive everything shown",
    ]);
  });
});

describe("filtersToBulkFilter", () => {
  it("selects archived items in the archived view and only dry-run items for 'only'", () => {
    expect(
      filtersToBulkFilter({
        ...DEFAULT_FILTERS,
        archived: true,
        dryRun: "only",
        starred: true,
      }),
    ).toEqual({ archived: "true", dry_run: true, starred: true });
  });
});

describe("confirmText", () => {
  const preset = CLEAR_PRESETS[0]!;
  it("states the count and that starred items are kept", () => {
    expect(confirmText(preset, 12, 3)).toContain("Archive 12 items");
    expect(confirmText(preset, 12, 3)).toContain("3 starred items are kept");
    expect(confirmText(preset, 1, 0)).toContain("Archive 1 item ");
    expect(confirmText(preset, 1, 0)).toContain("Starred items are kept");
  });
  it("says so when there is nothing to archive", () => {
    expect(confirmText(preset, 0, 0)).toContain("No read items to archive");
  });
});

describe("selection", () => {
  const page = ["a", "b", "c"];

  it("toggles one id without mutating the input", () => {
    const s = new Set(["a"]);
    expect([...toggleSelected(s, "b")]).toEqual(["a", "b"]);
    expect([...toggleSelected(s, "a")]).toEqual([]);
    expect([...s]).toEqual(["a"]);
  });

  it("reports none, some and all", () => {
    expect(pageSelection(new Set(), page)).toBe("none");
    expect(pageSelection(new Set(["a", "x"]), page)).toBe("some");
    expect(pageSelection(new Set(page), page)).toBe("all");
  });

  it("select-all selects the page, and clears it when everything was selected", () => {
    expect([...toggleAllOnPage(new Set(["a"]), page)].sort()).toEqual(page);
    expect([...toggleAllOnPage(new Set(page), page)]).toEqual([]);
    expect([...toggleAllOnPage(new Set([...page, "z"]), page)]).toEqual(["z"]);
  });

  it("prunes ids that left the list and keeps the set otherwise", () => {
    const s = new Set(["a", "gone"]);
    expect([...pruneSelection(s, page)]).toEqual(["a"]);
    const same = new Set(["a"]);
    expect(pruneSelection(same, page)).toBe(same);
  });
});
