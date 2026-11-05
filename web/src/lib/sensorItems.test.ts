import { describe, expect, it } from "vitest";
import {
  buildColumns,
  defaultVisible,
  describeCell,
  filterRows,
  flattenItem,
  keyRank,
  parseSensorStdout,
  toRows,
} from "./sensorItems";

describe("parseSensorStdout", () => {
  it("treats empty output as zero items", () => {
    expect(parseSensorStdout("  \n")).toEqual({ items: [], error: null });
  });

  it("returns the elements of a single JSON array, pretty-printed or not", () => {
    const text = '[\n  {"id": 1},\n  {"id": 2}\n]\n';
    expect(parseSensorStdout(text).items).toEqual([{ id: 1 }, { id: 2 }]);
    expect(parseSensorStdout('[{"id":1}]').items).toEqual([{ id: 1 }]);
  });

  it("reads JSONL, skipping blank lines", () => {
    const r = parseSensorStdout('{"id":1}\n\n{"id":2}\n');
    expect(r.error).toBeNull();
    expect(r.items).toEqual([{ id: 1 }, { id: 2 }]);
  });

  it("reads concatenated and multi-line values like the daemon", () => {
    expect(parseSensorStdout('{"a":1}{"a":2}').items).toHaveLength(2);
    expect(parseSensorStdout('{\n"a": 1\n}\n{"a": 2}').items).toHaveLength(2);
  });

  it("keeps braces and quotes inside strings from ending a value", () => {
    const r = parseSensorStdout('{"t":"} \\" ]"}\n{"t":"x"}');
    expect(r.error).toBeNull();
    expect(r.items).toEqual([{ t: '} " ]' }, { t: "x" }]);
  });

  it("accepts scalar items", () => {
    expect(parseSensorStdout('1\n"two"\nnull').items).toEqual([1, "two", null]);
  });

  it("does not unwrap an array that is one of several values", () => {
    expect(parseSensorStdout("[1]\n[2]").items).toEqual([[1], [2]]);
  });

  it("reports the line of the first bad value and keeps the ones before it", () => {
    const r = parseSensorStdout('{"id":1}\n{"id":2}\nnot json\n{"id":4}');
    expect(r.error?.line).toBe(3);
    expect(r.items).toEqual([{ id: 1 }, { id: 2 }]);
  });

  it("reports an unterminated value with the line it starts on", () => {
    const r = parseSensorStdout('{"id":1}\n{"id": ');
    expect(r.error?.line).toBe(2);
    expect(r.error?.message).toMatch(/end of input/);
  });

  it("salvages the complete elements of a cut-off array", () => {
    const r = parseSensorStdout('[{"id":1},{"id":2},{"id":');
    expect(r.error).not.toBeNull();
    expect(r.items).toEqual([{ id: 1 }, { id: 2 }]);
  });
});

describe("flattenItem", () => {
  it("flattens nested objects to dot paths", () => {
    const flat = flattenItem({
      id: 1,
      subject: { title: "x", repo: { full_name: "a/b" } },
    });
    expect([...flat.keys()]).toEqual([
      "id",
      "subject.title",
      "subject.repo.full_name",
    ]);
    expect(flat.get("subject.repo.full_name")).toBe("a/b");
  });

  it("stops at the depth limit and keeps the object as one value", () => {
    const flat = flattenItem({ a: { b: { c: { d: 1 } } } }, 3);
    expect([...flat.keys()]).toEqual(["a.b.c"]);
    expect(flat.get("a.b.c")).toEqual({ d: 1 });
  });

  it("keeps arrays and empty objects as values", () => {
    const flat = flattenItem({ labels: ["a"], extra: {} });
    expect(flat.get("labels")).toEqual(["a"]);
    expect(flat.get("extra")).toEqual({});
  });

  it("puts non-object items in a value column", () => {
    expect([...flattenItem("x").entries()]).toEqual([["(value)", "x"]]);
    expect([...flattenItem([1, 2]).keys()]).toEqual(["(value)"]);
  });
});

describe("describeCell", () => {
  it("shows arrays as a count with a preview", () => {
    expect(describeCell(["a", "b"]).text).toBe("[2] a, b");
    expect(describeCell([1, 2, 3, 4]).text).toBe("[4] 1, 2, 3, …");
    expect(describeCell([{ a: 1 }]).text).toBe("[1] {…}");
    expect(describeCell([]).text).toBe("[]");
  });

  it("shows objects as a key count and carries the full value for hover", () => {
    const c = describeCell({ a: 1, b: 2 });
    expect(c.text).toBe("{2 keys}");
    expect(c.full).toContain('"b": 2');
  });

  it("shows null and scalars literally", () => {
    expect(describeCell(null).text).toBe("null");
    expect(describeCell(false).text).toBe("false");
    expect(describeCell(3).kind).toBe("number");
  });
});

describe("buildColumns", () => {
  const rows = [
    { title: "t", id: 1, z: 1, state: "open", "user.id": 7 },
    { title: "u", id: 2, state: "open", extra: 1, "user.id": 8 },
    { id: 3, state: "closed", "user.id": 9 },
  ].map((o) => new Map(Object.entries(o)));

  it("puts identifying fields first, then orders by how many rows have the column", () => {
    expect(buildColumns(rows).map((c) => c.path)).toEqual([
      "id",
      "title",
      "user.id",
      "state",
      "z",
      "extra",
    ]);
  });

  it("counts rows per column", () => {
    const cols = buildColumns(rows);
    expect(cols.find((c) => c.path === "state")?.count).toBe(3);
    expect(cols.find((c) => c.path === "z")?.count).toBe(1);
  });

  it("ranks id before key before other fields", () => {
    expect(keyRank("id")).toBeLessThan(keyRank("key"));
    expect(keyRank("key")).toBeLessThan(keyRank("state"));
    expect(keyRank("repo.id")).toBeLessThan(keyRank("repo.state"));
  });

  it("shows only the first columns by default", () => {
    const cols = buildColumns(rows);
    expect([...defaultVisible(cols, 2)]).toEqual(["id", "title"]);
  });
});

describe("filterRows", () => {
  const rows = toRows([
    {
      item: { id: 1, subject: { title: "Fix the login bug" } },
      dedupKey: "k-1",
    },
    {
      item: { id: 2, labels: ["urgent"] },
      dedupKey: null,
      keyError: "bad template",
    },
    { item: { id: 3, subject: { title: "Docs" } } },
  ]);

  it("matches any flattened cell, case-insensitively", () => {
    expect(filterRows(rows, "LOGIN").map((r) => r.index)).toEqual([0]);
    expect(filterRows(rows, "urgent").map((r) => r.index)).toEqual([1]);
  });

  it("matches dedup keys and key errors", () => {
    expect(filterRows(rows, "k-1").map((r) => r.index)).toEqual([0]);
    expect(filterRows(rows, "template").map((r) => r.index)).toEqual([1]);
  });

  it("returns every row for a blank query", () => {
    expect(filterRows(rows, "  ")).toHaveLength(3);
  });
});
