import { describe, expect, it } from "vitest";
import { collapseContext, lineDiff, type DiffLine } from "./diff";

const summary = (d: DiffLine[]) =>
  d.map((l) => (l.type === "gap" ? `~${l.count}` : `${l.type[0]}:${l.text}`));

describe("lineDiff", () => {
  it("shows a new file as all additions", () => {
    expect(summary(lineDiff(null, "a\nb\n"))).toEqual(["a:a", "a:b"]);
  });

  it("finds a changed line between unchanged ones", () => {
    expect(summary(lineDiff("a\nb\nc\n", "a\nB\nc\n"))).toEqual([
      "e:a",
      "d:b",
      "a:B",
      "e:c",
    ]);
  });

  it("handles inserts and deletes in the middle", () => {
    expect(summary(lineDiff("a\nb\nc\nd\n", "a\nc\nx\nd\n"))).toEqual([
      "e:a",
      "d:b",
      "e:c",
      "a:x",
      "e:d",
    ]);
  });

  it("reports nothing changed for equal texts", () => {
    expect(lineDiff("a\nb", "a\nb").every((l) => l.type === "eq")).toBe(true);
  });
});

describe("collapseContext", () => {
  it("replaces long unchanged runs with a gap, keeping context around changes", () => {
    const old = Array.from({ length: 20 }, (_, i) => `l${i}`).join("\n");
    const next = old.replace("l10", "L10");
    const out = summary(collapseContext(lineDiff(old, next), 2));
    expect(out).toEqual([
      "~8",
      "e:l8",
      "e:l9",
      "d:l10",
      "a:L10",
      "e:l11",
      "e:l12",
      "~7",
    ]);
  });

  it("leaves short unchanged runs alone", () => {
    const out = collapseContext(lineDiff("a\nb\nc", "a\nB\nc"), 3);
    expect(out.some((l) => l.type === "gap")).toBe(false);
  });
});
