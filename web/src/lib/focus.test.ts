import { describe, expect, it } from "vitest";
import { assistDrawerOpen, focusLost, noFocusOnClick } from "./focus";

describe("focusLost", () => {
  const body = {} as Element;
  it("is true for nothing or <body> focused", () => {
    expect(focusLost(null, body)).toBe(true);
    expect(focusLost(body, body)).toBe(true);
  });
  it("is false when an element holds focus", () => {
    expect(focusLost({} as Element, body)).toBe(false);
  });
});

describe("assistDrawerOpen", () => {
  it("follows the drawer's presence", () => {
    expect(assistDrawerOpen({ querySelector: () => ({}) as Element })).toBe(
      true,
    );
    expect(assistDrawerOpen({ querySelector: () => null })).toBe(false);
  });
});

describe("noFocusOnClick", () => {
  it("cancels the mousedown default, which is what moves focus", () => {
    let prevented = false;
    noFocusOnClick.onMouseDown({ preventDefault: () => (prevented = true) });
    expect(prevented).toBe(true);
  });
});
