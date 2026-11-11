import { describe, expect, it } from "vitest";
import { parsePortSchema, schemaText } from "./portSchema";

describe("port schema text", () => {
  it("empty text is no schema, objects parse", () => {
    expect(parsePortSchema("  ")).toEqual({ ok: true, value: undefined });
    expect(parsePortSchema('{"type":"object"}')).toEqual({
      ok: true,
      value: { type: "object" },
    });
  });

  it("rejects invalid JSON and non-objects", () => {
    expect(parsePortSchema("{").ok).toBe(false);
    expect(parsePortSchema("[]").ok).toBe(false);
    expect(parsePortSchema("null").ok).toBe(false);
  });

  it("schemaText round trips", () => {
    expect(schemaText(undefined)).toBe("");
    expect(JSON.parse(schemaText({ type: "object" }))).toEqual({
      type: "object",
    });
  });
});
