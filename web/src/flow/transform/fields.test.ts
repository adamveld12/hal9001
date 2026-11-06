import { describe, expect, it } from "vitest";
import {
  arrayFields,
  discoverFields,
  fieldType,
  plainFields,
  relativeFields,
} from "./fields";

const issue = {
  title: "Fix login",
  number: 42,
  draft: false,
  closed_at: null,
  author: { login: "ada", site_admin: true },
  labels: [
    { name: "bug", color: "red" },
    { name: "p1", description: "urgent" },
  ],
  assignees: ["ada", "bob"],
  "odd.key": 1,
};

describe("discoverFields", () => {
  const fields = discoverFields(issue);
  const byPath = new Map(fields.map((f) => [f.path, f]));

  it("lists top-level fields with type and example, parents before children", () => {
    expect(byPath.get("title")).toEqual({
      path: "title",
      type: "string",
      example: "Fix login",
    });
    expect(byPath.get("number")?.type).toBe("number");
    expect(byPath.get("draft")?.type).toBe("boolean");
    expect(byPath.get("closed_at")?.type).toBe("null");
    const paths = fields.map((f) => f.path);
    expect(paths.indexOf("author")).toBeLessThan(paths.indexOf("author.login"));
  });

  it("descends into nested objects", () => {
    expect(byPath.get("author")).toMatchObject({
      type: "object",
      example: "{2 keys}",
    });
    expect(byPath.get("author.login")?.example).toBe("ada");
    expect(byPath.get("author.site_admin")?.type).toBe("boolean");
  });

  it("uses [] paths for arrays, merging keys over the elements", () => {
    expect(byPath.get("labels")).toMatchObject({
      type: "array",
      example: "[2 items]",
    });
    expect(byPath.get("labels[].name")?.example).toBe("bug");
    expect(byPath.get("labels[].color")?.type).toBe("string");
    // only the second element has it
    expect(byPath.get("labels[].description")?.example).toBe("urgent");
    expect(byPath.get("assignees[]")).toMatchObject({
      type: "string",
      example: "ada",
    });
  });

  it("skips keys that cannot be written as a path", () => {
    expect(byPath.has("odd.key")).toBe(false);
  });

  it("marks a field with conflicting types as unknown and ignores nulls when merging", () => {
    const f = discoverFields({
      a: [{ v: 1 }, { v: "x" }],
      b: [{ v: null }, { v: 2 }],
    });
    expect(fieldType(f, "a[].v")).toBe("unknown");
    expect(fieldType(f, "b[].v")).toBe("number");
    expect(f.find((x) => x.path === "b[].v")?.example).toBe("2");
  });

  it("caps depth and field count", () => {
    const deep = { a: { b: { c: { d: { e: { f: 1 } } } } } };
    expect(discoverFields(deep).map((f) => f.path)).toEqual([
      "a",
      "a.b",
      "a.b.c",
      "a.b.c.d",
      "a.b.c.d.e",
    ]);
    const wide = Object.fromEntries(
      Array.from({ length: 50 }, (_, i) => [`k${i}`, i]),
    );
    expect(discoverFields(wide, { maxFields: 10 })).toHaveLength(10);
  });

  it("offers only the whole value for a sample that is not an object", () => {
    expect(discoverFields("hi")).toEqual([
      { path: ".", type: "string", example: "hi" },
    ]);
    expect(discoverFields([1, 2])).toEqual([
      { path: ".", type: "array", example: "[2 items]" },
    ]);
  });
});

describe("field queries", () => {
  const fields = discoverFields(issue);

  it("lists list-valued fields for the steps that take a list", () => {
    expect(arrayFields(fields).map((f) => f.path)).toEqual([
      "labels",
      "assignees",
    ]);
  });

  it("excludes list elements and the whole value where a step writes or removes", () => {
    const paths = plainFields(fields).map((f) => f.path);
    expect(paths).toContain("author.login");
    expect(paths).not.toContain("labels[].name");
    expect(plainFields(discoverFields("x"))).toEqual([]);
  });

  it("gives paths relative to a list element", () => {
    expect(relativeFields(fields, "labels").map((f) => f.path)).toEqual([
      "name",
      "color",
      "description",
    ]);
    expect(relativeFields(fields, "assignees")).toEqual([
      { path: ".", type: "string", example: "ada" },
    ]);
    expect(relativeFields(fields, "title")).toEqual([]);
  });
});
