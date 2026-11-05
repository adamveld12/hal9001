import { describe, expect, it } from "vitest";
import {
  collapseContext,
  diffsFromToolContent,
  extractCommand,
  extractDiffs,
  lineDiff,
  parseUnified,
  type DiffLine,
} from "./diff";

const summary = (lines: DiffLine[]) =>
  lines.map(
    (l) => `${l.kind === "ctx" ? " " : l.kind === "add" ? "+" : "-"}${l.text}`,
  );

describe("lineDiff", () => {
  it("marks only the changed lines", () => {
    expect(summary(lineDiff("a\nb\nc\n", "a\nB\nc\n"))).toEqual([
      " a",
      "-b",
      "+B",
      " c",
    ]);
  });

  it("handles insertions, deletions, and an empty side", () => {
    expect(summary(lineDiff("a\nc", "a\nb\nc"))).toEqual([" a", "+b", " c"]);
    expect(summary(lineDiff("a\nb\nc", "a\nc"))).toEqual([" a", "-b", " c"]);
    expect(summary(lineDiff("", "x\ny"))).toEqual(["+x", "+y"]);
    expect(summary(lineDiff("x", ""))).toEqual(["-x"]);
    expect(lineDiff("same", "same")).toEqual([{ kind: "ctx", text: "same" }]);
  });

  it("finds the common lines in the middle of a rewrite", () => {
    expect(
      summary(lineDiff("1\nkeep\n2\nkeep2\n3", "A\nkeep\nB\nkeep2\nC")),
    ).toEqual(["-1", "+A", " keep", "-2", "+B", " keep2", "-3", "+C"]);
  });

  it("still returns a correct, if coarse, diff for very large changes", () => {
    const old = Array.from({ length: 600 }, (_, i) => `o${i}`).join("\n");
    const next = Array.from({ length: 600 }, (_, i) => `n${i}`).join("\n");
    const out = lineDiff(old, next);
    expect(out.filter((l) => l.kind === "del")).toHaveLength(600);
    expect(out.filter((l) => l.kind === "add")).toHaveLength(600);
  });
});

describe("collapseContext", () => {
  it("hides long unchanged runs but keeps context around changes", () => {
    const lines: DiffLine[] = [
      ...Array.from({ length: 10 }, (_, i): DiffLine => ({
        kind: "ctx",
        text: `c${i}`,
      })),
      { kind: "add", text: "new" },
      ...Array.from({ length: 2 }, (_, i): DiffLine => ({
        kind: "ctx",
        text: `d${i}`,
      })),
    ];
    const out = collapseContext(lines, 3);
    expect(out.map((l) => l.text)).toEqual([
      "7 unchanged lines",
      "c7",
      "c8",
      "c9",
      "new",
      "d0",
      "d1",
    ]);
    expect(out[0]?.kind).toBe("skip");
  });

  it("leaves a diff without changes alone", () => {
    const lines: DiffLine[] = [{ kind: "ctx", text: "x" }];
    expect(collapseContext(lines)).toEqual(lines);
  });
});

describe("parseUnified", () => {
  it("classifies lines by their leading character", () => {
    const out = parseUnified(
      "--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n ctx\n-old\n+new\n",
    );
    expect(out.map((l) => l.kind)).toEqual([
      "hunk",
      "hunk",
      "hunk",
      "ctx",
      "del",
      "add",
    ]);
    expect(out[4]?.text).toBe("old");
  });
});

describe("extractDiffs", () => {
  it("reads old_string / new_string edits", () => {
    const [d] = extractDiffs({
      file_path: "/src/a.go",
      old_string: "x := 1",
      new_string: "x := 2",
    });
    expect(d?.path).toBe("/src/a.go");
    expect(summary(d!.lines)).toEqual(["-x := 1", "+x := 2"]);
  });

  it("reads camelCase and ACP diff content entries, including new files", () => {
    const diffs = extractDiffs({
      content: [
        { type: "diff", path: "a.txt", oldText: "a", newText: "b" },
        { type: "diff", path: "new.txt", oldText: null, newText: "hello" },
      ],
    });
    expect(diffs.map((d) => d.path)).toEqual(["a.txt", "new.txt"]);
    expect(summary(diffs[1]!.lines)).toEqual(["+hello"]);
  });

  it("expands a list of edits, each under the file's path", () => {
    const diffs = extractDiffs({
      file_path: "f.ts",
      edits: [
        { old_string: "a", new_string: "b" },
        { old_string: "c", new_string: "d" },
      ],
    });
    expect(diffs).toHaveLength(2);
    expect(diffs.every((d) => d.path === "f.ts")).toBe(true);
  });

  it("treats a path with string content as a new file", () => {
    const [d] = extractDiffs({ file_path: "n.md", content: "one\ntwo" });
    expect(summary(d!.lines)).toEqual(["+one", "+two"]);
  });

  it("parses a unified diff string", () => {
    const [d] = extractDiffs({ path: "f", diff: "@@ -1 +1 @@\n-a\n+b" });
    expect(d?.lines.map((l) => l.kind)).toEqual(["hunk", "del", "add"]);
  });

  it("returns nothing for inputs that describe no file change", () => {
    expect(extractDiffs({ command: "ls -la" })).toEqual([]);
    expect(extractDiffs("text")).toEqual([]);
    expect(extractDiffs(null)).toEqual([]);
    expect(extractDiffs({ path: "/etc/hosts" })).toEqual([]);
  });
});

describe("extractCommand", () => {
  it("returns a string or joined argv command", () => {
    expect(extractCommand({ command: "git status" })).toBe("git status");
    expect(extractCommand({ command: ["git", "status"] })).toBe("git status");
  });

  it("returns null when there is none", () => {
    expect(extractCommand({ path: "x" })).toBeNull();
    expect(extractCommand("git status")).toBeNull();
    expect(extractCommand({ command: [1, 2] })).toBeNull();
  });
});

describe("diffsFromToolContent", () => {
  it("turns ACP diff content into file diffs and ignores other content", () => {
    const out = diffsFromToolContent([
      { type: "text", text: "ignored" },
      { type: "diff", path: "a.go", oldText: "x\ny\n", newText: "x\nz\n" },
      { type: "diff", path: "new.txt", oldText: null, newText: "hi\n" },
    ]);
    expect(out).toHaveLength(2);
    expect(out[0]!.path).toBe("a.go");
    expect(summary(out[0]!.lines)).toEqual([" x", "-y", "+z"]);
    expect(summary(out[1]!.lines)).toEqual(["+hi"]);
  });

  it("is empty when the call carries no diff", () => {
    expect(diffsFromToolContent([{ type: "text", text: "t" }])).toEqual([]);
  });
});
