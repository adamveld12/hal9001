import { describe, expect, it } from "vitest";
import { cx, defaultFullWidth } from "./cx";

describe("cx", () => {
  it("skips falsy parts", () => {
    expect(cx("a", false, null, undefined, "b")).toBe("a b");
  });
});

describe("defaultFullWidth", () => {
  it("is full width when the caller sets no width", () => {
    expect(defaultFullWidth(undefined)).toBe("w-full");
    expect(defaultFullWidth("font-mono max-w-xs")).toBe("w-full");
  });
  it("yields to a caller width, including variants and fractions", () => {
    expect(defaultFullWidth("w-56")).toBe("");
    expect(defaultFullWidth("text-xs w-1/2")).toBe("");
    expect(defaultFullWidth("sm:w-40")).toBe("");
  });
});
