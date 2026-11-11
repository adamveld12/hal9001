import { describe, expect, it } from "vitest";
import type { AssistSession } from "../../api/assist";
import { recentSessions, sessionRowLabel } from "./history";

function s(
  id: string,
  kind: AssistSession["target"]["kind"],
  targetId: string | null,
  title: string | null,
  status: AssistSession["status"] = "idle",
  updated = "2026-11-05T10:00:00Z",
): AssistSession {
  return {
    id,
    title,
    target: { kind, id: targetId },
    updated_at: updated,
    status,
  } as AssistSession;
}

describe("sessionRowLabel", () => {
  it("uses the first message when there is one", () => {
    expect(
      sessionRowLabel(s("1", "general", null, "Why did it fail?")),
    ).toEqual({
      title: "Why did it fail?",
      target: "Ask AI",
    });
  });

  it("names a chat that has no message yet by what it is about", () => {
    expect(sessionRowLabel(s("1", "general", null, null)).title).toBe(
      "New chat",
    );
    expect(sessionRowLabel(s("2", "sensor", "gh", null))).toEqual({
      title: "Sensor gh draft",
      target: "Sensor gh",
    });
    expect(sessionRowLabel(s("3", "flow", null, "  ")).title).toBe(
      "New flow draft",
    );
  });
});

describe("recentSessions", () => {
  it("drops ended chats, sorts newest first and limits", () => {
    const all = [
      s("old", "general", null, "a", "idle", "2026-11-05T09:00:00Z"),
      s("ended", "general", null, "b", "closed", "2026-11-05T12:00:00Z"),
      s("new", "general", null, "c", "running", "2026-11-05T11:00:00Z"),
      s("mid", "sensor", "gh", "d", "idle", "2026-11-05T10:00:00Z"),
    ];
    expect(recentSessions(all).map((x) => x.id)).toEqual(["new", "mid", "old"]);
    expect(recentSessions(all, 2).map((x) => x.id)).toEqual(["new", "mid"]);
  });
});
