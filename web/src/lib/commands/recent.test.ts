import { describe, expect, it } from "vitest";
import { MAX_RECENT, parseRecent, pushRecent } from "./recent";

describe("pushRecent", () => {
  it("puts the newest first and moves a repeat to the front", () => {
    expect(pushRecent(["a", "b"], "c")).toEqual(["c", "a", "b"]);
    expect(pushRecent(["a", "b", "c"], "c")).toEqual(["c", "a", "b"]);
  });

  it("keeps at most MAX_RECENT", () => {
    const many = Array.from({ length: MAX_RECENT }, (_, i) => `id${i}`);
    const next = pushRecent(many, "new");
    expect(next).toHaveLength(MAX_RECENT);
    expect(next[0]).toBe("new");
    expect(next).not.toContain(`id${MAX_RECENT - 1}`);
  });
});

describe("parseRecent", () => {
  it("reads a stored list and drops anything else", () => {
    expect(parseRecent('["a","b"]')).toEqual(["a", "b"]);
    expect(parseRecent('["a",1,"",null]')).toEqual(["a"]);
    expect(parseRecent("not json")).toEqual([]);
    expect(parseRecent('{"a":1}')).toEqual([]);
    expect(parseRecent(null)).toEqual([]);
  });
});
