import { describe, expect, it } from "vitest";
import { countBy, factList, rowEntities, trimText } from "./contextSummary";

describe("trimText", () => {
  it("collapses whitespace and cuts with an ellipsis", () => {
    expect(trimText("a\n  b")).toBe("a b");
    const t = trimText("x".repeat(300), 200);
    expect(t).toHaveLength(200);
    expect(t.endsWith("…")).toBe(true);
    expect(trimText(null)).toBe("");
  });
});

describe("countBy", () => {
  it("orders by count then key", () => {
    expect(countBy(["ok", "failed", "ok", "a"], (s) => s)).toBe(
      "ok 2, a 1, failed 1",
    );
    expect(countBy([], (s: string) => s)).toBe("");
  });
});

describe("factList", () => {
  it("drops empty values and stringifies", () => {
    expect(
      factList([
        ["a", 1],
        ["b", undefined],
        ["c", ""],
        ["d", false],
        ["e", null],
      ]),
    ).toEqual([
      { label: "a", value: "1" },
      { label: "d", value: "false" },
    ]);
  });
});

describe("rowEntities", () => {
  it("caps the rows", () => {
    const rows = Array.from({ length: 50 }, (_, i) => i);
    expect(
      rowEntities(rows, (n) => ({ kind: "x", id: String(n) })),
    ).toHaveLength(20);
  });
});
