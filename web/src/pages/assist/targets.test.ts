import { describe, expect, it } from "vitest";
import type { AssistSession } from "../../api/assist";
import { bindSession } from "./sessions";
import { selectResumable, starterPrompts, targetConfig } from "./targets";

function session(
  id: string,
  kind: "sensor" | "flow",
  targetId: string | null,
  status: AssistSession["status"],
  updated: string,
  flowId: string | null = null,
): AssistSession {
  return {
    id,
    target: { kind, id: targetId },
    runtime: "claude",
    model: null,
    status,
    error: null,
    draft: {
      kind,
      yaml: "",
      hash: "h",
      sensor_id: null,
      flow_id: flowId,
      tool_id: null,
      prompts: [],
      sensors: [],
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
    session("old", "sensor", "gh", "idle", "2026-11-05T09:00:00Z"),
    session("new", "sensor", "gh", "running", "2026-11-05T11:00:00Z"),
    session("ended", "sensor", "gh", "closed", "2026-11-05T12:00:00Z"),
    session("other", "sensor", "linear", "idle", "2026-11-05T12:30:00Z"),
    session("draft", "sensor", null, "idle", "2026-11-05T10:00:00Z"),
    session("failed", "sensor", "gh", "failed", "2026-11-05T10:30:00Z"),
    session("flow-gh", "flow", "gh", "idle", "2026-11-05T13:00:00Z"),
    session("flow-new", "flow", null, "idle", "2026-11-05T13:30:00Z"),
  ];

  it("offers open sessions of the same sensor, newest first, failed ones included", () => {
    expect(selectResumable(all, "sensor", "gh").map((s) => s.id)).toEqual([
      "new",
      "failed",
      "old",
    ]);
  });

  it("offers sessions of a not-yet-existing sensor only to the new-sensor page", () => {
    expect(selectResumable(all, "sensor", null).map((s) => s.id)).toEqual([
      "draft",
    ]);
  });

  it("offers nothing for a sensor without sessions", () => {
    expect(selectResumable(all, "sensor", "nope")).toEqual([]);
  });

  it("keeps sessions of a flow apart from a sensor with the same id", () => {
    expect(selectResumable(all, "flow", "gh").map((s) => s.id)).toEqual([
      "flow-gh",
    ]);
    expect(selectResumable(all, "flow", null).map((s) => s.id)).toEqual([
      "flow-new",
    ]);
  });
});

describe("starterPrompts", () => {
  it("suggests sources to watch for a new sensor and edits for an existing one", () => {
    expect(starterPrompts("sensor", null).join(" ")).toMatch(
      /GitHub review requests/,
    );
    expect(starterPrompts("sensor", null).join(" ")).toMatch(/Linear/);
    expect(starterPrompts("sensor", "gh")).not.toEqual(
      starterPrompts("sensor", null),
    );
  });

  it("suggests the flows to start with for a new flow", () => {
    expect(starterPrompts("flow", null)).toEqual([
      "Triage my GitHub review requests",
      "Send urgent Slack mentions to my inbox",
      "Summarize new email",
    ]);
    expect(starterPrompts("flow", "triage")).not.toEqual(
      starterPrompts("flow", null),
    );
  });
});

describe("target config", () => {
  it("titles and links follow the kind", () => {
    const flow = targetConfig("flow");
    const sensor = targetConfig("sensor");
    expect(flow.newTitle).toBe("Create a flow with AI");
    expect(sensor.newTitle).toBe("Create a sensor with AI");
    expect(flow.sessionTitle(null)).toBe("New flow");
    expect(flow.sessionTitle("triage")).toBe("Flow triage");
    expect(sensor.sessionTitle("gh")).toBe("Sensor gh");
    expect(flow.editorPath("triage", "s1")).toBe("/flows/triage?assist=s1");
    expect(sensor.editorPath("gh", "s1")).toBe("/sensors/gh?assist=s1");
    expect(flow.draftPath("s1")).toBe("/flows?assist=s1");
  });
});

describe("bindSession", () => {
  it("binds a sensor session on its sensor and on a page for a new sensor's saved file", () => {
    const s = session("s1", "sensor", "gh", "idle", "t");
    expect(bindSession(s, "sensor", "gh").sessionId).toBe("s1");
    expect(bindSession(s, "sensor", "other")).toEqual({
      sessionId: null,
      otherTarget: "gh",
    });
    const fresh = session("s2", "sensor", null, "idle", "t");
    expect(bindSession(fresh, "sensor", "anything").sessionId).toBe("s2");
  });

  it("binds a flow session on its flow", () => {
    const s = session("s1", "flow", "triage", "idle", "t", "triage");
    expect(bindSession(s, "flow", "triage").sessionId).toBe("s1");
    expect(bindSession(s, "flow", null)).toEqual({
      sessionId: null,
      otherTarget: "triage",
    });
  });

  it("keeps a new-flow session on the page of the flow it saved, and only there", () => {
    const s = session("s1", "flow", null, "idle", "t", "triage");
    expect(bindSession(s, "flow", null).sessionId).toBe("s1");
    expect(bindSession(s, "flow", "triage").sessionId).toBe("s1");
    expect(bindSession(s, "flow", "other")).toEqual({
      sessionId: null,
      otherTarget: "triage",
    });
  });

  it("leaves closed sessions and other kinds alone", () => {
    expect(
      bindSession(session("s", "flow", "t", "closed", "t"), "flow", "t")
        .sessionId,
    ).toBeNull();
    expect(
      bindSession(session("s", "flow", "t", "idle", "t"), "sensor", "t")
        .sessionId,
    ).toBeNull();
  });
});
