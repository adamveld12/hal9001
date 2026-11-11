import { describe, expect, it } from "vitest";
import type { Approval } from "../../api/approvals";
import type { InboxItem } from "../../api/inbox";
import { approval } from "../approvals/fixtures";
import { DEFAULT_FILTERS } from "./filters";
import {
  mergeInboxRows,
  selectionFromPath,
  selectionKey,
  selectionPath,
} from "./merge";

const note = (id: string, created_at: string): InboxItem =>
  ({ id, created_at, title: id, summary_md: "" }) as InboxItem;
const appr = (id: string, created_at: string, over = {}): Approval =>
  approval({ id, created_at, proposal_md: `Proposal ${id}`, ...over });

const notes = [
  note("n1", "2026-01-02T00:00:00Z"),
  note("n2", "2026-01-01T00:00:00Z"),
];
const approvals = [
  appr("a2", "2026-01-01T12:00:00Z"),
  appr("a1", "2026-01-01T06:00:00Z"),
  appr("done", "2026-01-01T00:00:00Z", { status: "approved" }),
];
const keys = (f = DEFAULT_FILTERS) =>
  mergeInboxRows(notes, approvals, f).map((r) => r.key);

describe("mergeInboxRows", () => {
  it("puts pending approvals first, then notes, dropping decided ones", () => {
    expect(keys()).toEqual(["a:a1", "a:a2", "n:n1", "n:n2"]);
  });
  it("interleaves by time when sorted by time", () => {
    expect(keys({ ...DEFAULT_FILTERS, sort: "time" })).toEqual([
      "n:n1",
      "a:a2",
      "a:a1",
      "n:n2",
    ]);
  });
  it("filters by kind", () => {
    expect(keys({ ...DEFAULT_FILTERS, kind: "action" })).toEqual([
      "a:a1",
      "a:a2",
    ]);
    expect(keys({ ...DEFAULT_FILTERS, kind: "notes" })).toEqual([
      "n:n1",
      "n:n2",
    ]);
  });
  it("hides approvals under note-only filters and applies search", () => {
    expect(keys({ ...DEFAULT_FILTERS, starred: true })).toEqual([
      "n:n1",
      "n:n2",
    ]);
    expect(keys({ ...DEFAULT_FILTERS, q: "proposal a2" })).toEqual([
      "a:a2",
      "n:n1",
      "n:n2",
    ]);
  });
});

describe("selectionFromPath", () => {
  it("parses notes and approvals", () => {
    expect(selectionFromPath("/inbox/abc")).toEqual({
      kind: "note",
      id: "abc",
    });
    expect(selectionFromPath("/inbox/approval/x1")).toEqual({
      kind: "approval",
      id: "x1",
    });
  });
  it("ignores other paths", () => {
    expect(selectionFromPath("/inbox")).toBeNull();
    expect(selectionFromPath("/inbox/approval")).toBeNull();
    expect(selectionFromPath("/approvals/x")).toBeNull();
  });
  it("round-trips", () => {
    const s = { kind: "approval", id: "q" } as const;
    expect(selectionFromPath(selectionPath(s))).toEqual(s);
    expect(selectionKey(s)).toBe("a:q");
  });
});
