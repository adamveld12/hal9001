import { describe, expect, it } from "vitest";
import { shouldIgnoreHotkey } from "./hotkeys";
import { moveCursor, neighborAfterRemoval } from "./selection";

const ids = ["a", "b", "c"];

describe("moveCursor", () => {
  it("starts at the first item when nothing is focused", () => {
    expect(moveCursor(ids, null, 1)).toBe("a");
    expect(moveCursor(ids, null, -1)).toBe("a");
  });

  it("moves one step and stops at the ends", () => {
    expect(moveCursor(ids, "a", 1)).toBe("b");
    expect(moveCursor(ids, "c", 1)).toBe("c");
    expect(moveCursor(ids, "a", -1)).toBe("a");
    expect(moveCursor(ids, "c", -1)).toBe("b");
  });

  it("continues from where a vanished item used to be", () => {
    const after = ["a", "c"]; // b left the list at index 1
    expect(moveCursor(after, "b", 1, 1)).toBe("c");
    expect(moveCursor(after, "b", -1, 1)).toBe("a");
    expect(moveCursor(["a"], "b", 1, 5)).toBe("a");
  });

  it("returns null for an empty list", () => {
    expect(moveCursor([], "a", 1)).toBeNull();
  });
});

describe("neighborAfterRemoval", () => {
  it("prefers the next item, then the previous one", () => {
    expect(neighborAfterRemoval(ids, "a")).toBe("b");
    expect(neighborAfterRemoval(ids, "c")).toBe("b");
    expect(neighborAfterRemoval(["a"], "a")).toBeNull();
    expect(neighborAfterRemoval(ids, "zzz")).toBeNull();
  });
});

describe("shouldIgnoreHotkey", () => {
  const base = {
    key: "j",
    ctrlKey: false,
    metaKey: false,
    altKey: false,
    target: { tagName: "BODY" },
  };

  it("lets plain keys through on the page", () => {
    expect(shouldIgnoreHotkey(base)).toBe(false);
  });

  it("ignores keys typed into fields and editors", () => {
    for (const tagName of ["INPUT", "TEXTAREA", "SELECT"]) {
      expect(shouldIgnoreHotkey({ ...base, target: { tagName } })).toBe(true);
    }
    expect(
      shouldIgnoreHotkey({
        ...base,
        target: { tagName: "DIV", isContentEditable: true },
      }),
    ).toBe(true);
  });

  it("ignores browser shortcuts", () => {
    expect(shouldIgnoreHotkey({ ...base, metaKey: true })).toBe(true);
    expect(shouldIgnoreHotkey({ ...base, ctrlKey: true })).toBe(true);
  });

  it("leaves Enter to a focused button or link", () => {
    const enter = { ...base, key: "Enter" };
    expect(
      shouldIgnoreHotkey({ ...enter, target: { tagName: "BUTTON" } }),
    ).toBe(true);
    expect(shouldIgnoreHotkey({ ...enter, target: { tagName: "A" } })).toBe(
      true,
    );
    expect(shouldIgnoreHotkey({ ...enter, target: { tagName: "BODY" } })).toBe(
      false,
    );
  });
});
