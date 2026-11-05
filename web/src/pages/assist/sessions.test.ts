import { describe, expect, it } from "vitest";
import type { AssistSession } from "../../api/assist";
import {
  clampDrawerWidth,
  DEFAULT_DRAWER_WIDTH,
  isBusy,
  MAX_DRAWER_WIDTH,
  MIN_DRAWER_WIDTH,
  parseStoredWidth,
  selectResumable,
  starterPrompts,
} from "./sessions";

function session(
  id: string,
  targetId: string | null,
  status: AssistSession["status"],
  updated: string,
): AssistSession {
  return {
    id,
    target: { kind: "sensor", id: targetId },
    runtime: "claude",
    model: null,
    status,
    error: null,
    draft: {
      yaml: "",
      hash: "h",
      sensor_id: null,
      diagnostics: [],
      updated_at: updated,
      updated_by: "user",
    },
    last_agent_run_id: null,
    created_at: updated,
    updated_at: updated,
  };
}

describe("selectResumable", () => {
  const all = [
    session("old", "gh", "idle", "2026-11-05T09:00:00Z"),
    session("new", "gh", "running", "2026-11-05T11:00:00Z"),
    session("ended", "gh", "closed", "2026-11-05T12:00:00Z"),
    session("other", "linear", "idle", "2026-11-05T12:30:00Z"),
    session("draft", null, "idle", "2026-11-05T10:00:00Z"),
    session("failed", "gh", "failed", "2026-11-05T10:30:00Z"),
  ];

  it("offers open sessions of the same sensor, newest first, failed ones included", () => {
    expect(selectResumable(all, "gh").map((s) => s.id)).toEqual([
      "new",
      "failed",
      "old",
    ]);
  });

  it("offers sessions of a not-yet-existing sensor only to the new-sensor page", () => {
    expect(selectResumable(all, null).map((s) => s.id)).toEqual(["draft"]);
  });

  it("offers nothing for a sensor without sessions", () => {
    expect(selectResumable(all, "nope")).toEqual([]);
  });
});

describe("starterPrompts", () => {
  it("suggests sources to watch for a new sensor and edits for an existing one", () => {
    expect(starterPrompts(null).join(" ")).toMatch(/GitHub review requests/);
    expect(starterPrompts(null).join(" ")).toMatch(/Linear/);
    expect(starterPrompts("gh")).not.toEqual(starterPrompts(null));
  });
});

describe("drawer width", () => {
  it("stays within its limits", () => {
    expect(clampDrawerWidth(100, 1600)).toBe(MIN_DRAWER_WIDTH);
    expect(clampDrawerWidth(5000, 3000)).toBe(MAX_DRAWER_WIDTH);
  });

  it("leaves room for the editor on a narrower window", () => {
    expect(clampDrawerWidth(700, 1000)).toBe(600);
  });

  it("falls back to the default for missing or unusable stored values", () => {
    expect(parseStoredWidth(null)).toBe(DEFAULT_DRAWER_WIDTH);
    expect(parseStoredWidth("abc")).toBe(DEFAULT_DRAWER_WIDTH);
    expect(parseStoredWidth("-5")).toBe(DEFAULT_DRAWER_WIDTH);
    expect(parseStoredWidth("480")).toBe(480);
  });
});

describe("isBusy", () => {
  it("is true while a turn runs or waits on an approval", () => {
    expect(isBusy("running")).toBe(true);
    expect(isBusy("awaiting_approval")).toBe(true);
    expect(isBusy("idle")).toBe(false);
    expect(isBusy("failed")).toBe(false);
  });
});
