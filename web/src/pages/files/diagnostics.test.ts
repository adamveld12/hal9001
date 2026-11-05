import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "../../api/client";
import {
  conflictHash,
  diagnosticsOf,
  groupDiagnostics,
  isConflict,
  sourceLine,
  visibleDiagnostics,
  type Diagnostic,
} from "./diagnostics";

const d = (
  severity: Diagnostic["severity"],
  message: string,
  line?: number,
): Diagnostic => ({ severity, code: "x", message, ...(line ? { line } : {}) });

function problem(status: number, type: string, extra: object = {}) {
  return new ApiError(
    status,
    {
      type: `urn:hal9001:problem:${type}`,
      title: type,
      status,
      ...extra,
    } as Problem,
    "failed",
  );
}

describe("groupDiagnostics", () => {
  it("orders groups by line, errors first, and puts unlocated findings last", () => {
    const { groups, counts } = groupDiagnostics([
      d("warning", "w5", 5),
      d("error", "nowhere"),
      d("info", "i2", 2),
      d("error", "e5", 5),
    ]);
    expect(groups.map((g) => g.line)).toEqual([2, 5, null]);
    expect(groups[1]?.items.map((i) => i.message)).toEqual(["e5", "w5"]);
    expect(counts).toEqual({ error: 2, warning: 1, info: 1 });
  });

  it("returns no groups for no findings", () => {
    expect(groupDiagnostics([]).groups).toEqual([]);
  });
});

describe("error classification", () => {
  it("reads diagnostics from a validation-failed problem only", () => {
    const list = [d("error", "bad", 3)];
    expect(
      diagnosticsOf(problem(422, "validation-failed", { diagnostics: list })),
    ).toEqual(list);
    expect(diagnosticsOf(problem(409, "conflict"))).toBeNull();
    expect(diagnosticsOf(new Error("boom"))).toBeNull();
  });

  it("treats only hash mismatches as edit conflicts", () => {
    expect(isConflict(problem(409, "conflict"))).toBe(true);
    expect(isConflict(problem(409, "already-exists"))).toBe(false);
    expect(isConflict(problem(409, "invalid-state"))).toBe(false);
  });

  it("extracts the current hash, null when the file is gone", () => {
    expect(conflictHash(problem(409, "conflict", { current_hash: "ab" }))).toBe(
      "ab",
    );
    expect(conflictHash(problem(409, "conflict", { current_hash: null }))).toBe(
      null,
    );
  });
});

describe("sourceLine", () => {
  it("returns the 1-based line and null out of range", () => {
    expect(sourceLine("a\nb\r\nc", 2)).toBe("b");
    expect(sourceLine("a\nb", 3)).toBeNull();
  });
});

describe("visibleDiagnostics", () => {
  const saved = { text: "v1", diagnostics: [d("warning", "from disk")] };
  const checked = { text: "v2", diagnostics: [d("error", "from check", 1)] };

  it("shows the server findings while the draft equals the saved file", () => {
    expect(visibleDiagnostics("v1", saved, null)).toMatchObject({
      origin: "saved",
      stale: false,
    });
  });

  it("prefers a check of exactly this draft", () => {
    const v = visibleDiagnostics("v2", saved, checked);
    expect(v).toMatchObject({ origin: "checked", stale: false });
    expect(v.diagnostics[0]?.message).toBe("from check");
  });

  it("marks findings stale once the draft changed after the check", () => {
    expect(visibleDiagnostics("v3", saved, checked)).toMatchObject({
      origin: "checked",
      stale: true,
    });
  });

  it("marks saved findings stale for an unchecked edited draft", () => {
    expect(visibleDiagnostics("v3", saved, null)).toMatchObject({
      origin: "saved",
      stale: true,
    });
  });

  it("falls back to the saved findings when the draft is reverted", () => {
    expect(visibleDiagnostics("v1", saved, checked)).toMatchObject({
      origin: "saved",
      stale: false,
    });
  });
});
