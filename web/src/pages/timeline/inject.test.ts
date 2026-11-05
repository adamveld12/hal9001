import { describe, expect, it } from "vitest";
import { parseInjectBody } from "./inject";

describe("parseInjectBody", () => {
  it("accepts any JSON value, including scalars", () => {
    expect(parseInjectBody('{"a":1}')).toEqual({ ok: true, value: { a: 1 } });
    expect(parseInjectBody("[1,2]")).toEqual({ ok: true, value: [1, 2] });
    expect(parseInjectBody("null")).toEqual({ ok: true, value: null });
    expect(parseInjectBody('"x"')).toEqual({ ok: true, value: "x" });
  });

  it("rejects empty and malformed input", () => {
    expect(parseInjectBody("  ").ok).toBe(false);
    expect(parseInjectBody("{oops").ok).toBe(false);
  });
});
