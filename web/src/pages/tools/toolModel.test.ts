import { describe, expect, it } from "vitest";
import type { Tool } from "../../api/tools";
import {
  argFields,
  buildArgs,
  paramsToSchema,
  schemaToParams,
  toolToYaml,
} from "./toolModel";

const tool: Tool = {
  id: "lookup",
  description: "Look: something up",
  params: {
    query: { type: "string", description: "What", required: true },
    limit: { type: "integer", default: 5 },
    mode: { type: "string", enum: ["fast", "deep"] },
  },
  lang: "bash",
  script: "echo hi\necho there",
  approve: {
    when: { conditions: [{ field: "mode", op: "equals", value: "deep" }] },
  },
  timeout: "10s",
};

describe("params <-> schema", () => {
  it("builds the schema the server builds", () => {
    const s = paramsToSchema(tool.params!);
    expect(s.required).toEqual(["query"]);
    expect((s.properties as Record<string, unknown>).limit).toEqual({
      type: "integer",
      default: 5,
    });
    expect(s.additionalProperties).toBe(false);
  });

  it("round-trips params", () => {
    expect(schemaToParams(paramsToSchema(tool.params!))).toEqual(tool.params);
  });

  it("refuses a schema params cannot express", () => {
    expect(
      schemaToParams({
        type: "object",
        properties: { a: { type: "string", pattern: "^x" } },
      }),
    ).toBeNull();
    expect(
      schemaToParams({
        type: "object",
        properties: { a: { type: "array", items: { type: "string" } } },
      }),
    ).toBeNull();
  });
});

describe("toolToYaml", () => {
  it("writes block scripts, quoted text and JSON for nested values", () => {
    const y = toolToYaml(tool);
    expect(y).toContain('description: "Look: something up"');
    expect(y).toContain("script: |\n  echo hi\n  echo there");
    expect(y).toContain('  limit: {"type":"integer","default":5}');
    expect(y).toContain('approve: {"when":');
    expect(y).toContain("timeout: 10s");
  });

  it("writes a raw schema instead of params", () => {
    const y = toolToYaml({
      ...tool,
      params: undefined,
      schema: { type: "object" },
    });
    expect(y).toContain('schema: {"type":"object"}');
    expect(y).not.toContain("params:");
  });
});

describe("args form", () => {
  const fields = argFields(paramsToSchema(tool.params!));

  it("has a field per property", () => {
    expect(fields.map((f) => f.name)).toEqual(["limit", "mode", "query"]);
    expect(fields.find((f) => f.name === "query")?.required).toBe(true);
  });

  it("types values and leaves empty optional ones out", () => {
    const r = buildArgs(fields, { query: "x", limit: "7", mode: "" });
    expect(r.errors).toEqual({});
    expect(r.args).toEqual({ query: "x", limit: 7 });
  });

  it("reports missing and malformed values", () => {
    const r = buildArgs(fields, { query: "", limit: "1.5" });
    expect(r.errors).toEqual({
      query: "required",
      limit: "must be a whole number",
    });
  });

  it("parses arrays and objects as JSON", () => {
    const f = argFields({
      properties: { tags: { type: "array" }, opts: { type: "object" } },
    });
    expect(buildArgs(f, { tags: '["a"]', opts: "{}" }).args).toEqual({
      tags: ["a"],
      opts: {},
    });
    expect(buildArgs(f, { tags: "{}", opts: "[]" }).errors).toEqual({
      tags: "must be a JSON array",
      opts: "must be a JSON object",
    });
  });
});
