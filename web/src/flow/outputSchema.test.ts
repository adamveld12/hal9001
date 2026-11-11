import { describe, expect, it } from "vitest";
import {
  NOTE_SCHEMA,
  NOTE_SCHEMA_TEXT,
  outputFieldHints,
  parseOutputSchema,
  schemaText,
} from "./outputSchema";

describe("output schema helpers", () => {
  it("hints list top-level properties", () => {
    expect(
      outputFieldHints({
        type: "object",
        properties: { verdict: { type: "string" }, score: { type: "number" } },
      }),
    ).toEqual([
      "{{.input.agent.output.verdict}}",
      "{{.input.agent.output.score}}",
    ]);
  });

  it("hints fall back to the note schema when unset", () => {
    expect(outputFieldHints(undefined)).toContain(
      "{{.input.agent.output.summary_md}}",
    );
    expect(outputFieldHints(undefined)).toHaveLength(
      Object.keys(NOTE_SCHEMA.properties as object).length,
    );
  });

  it("hints are empty without properties", () => {
    expect(outputFieldHints({ type: "object" })).toEqual([]);
    expect(outputFieldHints({ properties: [] })).toEqual([]);
  });

  it("parses empty text as unset and objects as values", () => {
    expect(parseOutputSchema("  ")).toEqual({ ok: true, value: undefined });
    expect(parseOutputSchema('{"type":"object"}')).toEqual({
      ok: true,
      value: { type: "object" },
    });
    expect(parseOutputSchema(NOTE_SCHEMA_TEXT)).toEqual({
      ok: true,
      value: NOTE_SCHEMA,
    });
  });

  it("rejects invalid JSON and non-objects", () => {
    expect(parseOutputSchema("{").ok).toBe(false);
    expect(parseOutputSchema("[]").ok).toBe(false);
    expect(parseOutputSchema("3").ok).toBe(false);
    expect(parseOutputSchema("null").ok).toBe(false);
  });

  it("schemaText is empty for unset", () => {
    expect(schemaText(undefined)).toBe("");
    expect(JSON.parse(schemaText({ type: "object" }))).toEqual({
      type: "object",
    });
  });
});
