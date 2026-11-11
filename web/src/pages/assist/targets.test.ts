import { describe, expect, it } from "vitest";
import type { AssistSession } from "../../api/assist";
import { bindSession } from "./sessions";
import { starterPrompts, targetConfig, targetPath } from "./targets";

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
    title: null,
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
  });

  it("a general chat has no page; the others open their editor or list", () => {
    expect(targetPath("general", null)).toBeNull();
    expect(targetPath("flow", "triage")).toBe("/flows/triage");
    expect(targetPath("sensor", "gh")).toBe("/sensors/gh");
    expect(targetPath("tool", null)).toBe("/tools");
  });

  it("the general chat suggests questions about the app", () => {
    expect(starterPrompts("general", null)).toContain(
      "What needs my attention?",
    );
    expect(targetConfig("general").sessionTitle(null)).toBe("Ask AI");
  });
});

describe("bindSession", () => {
  it("binds a sensor session on its sensor and on a page for a new sensor's saved file", () => {
    const s = session("s1", "sensor", "gh", "idle", "t");
    expect(bindSession(s, "sensor", "gh")).toBe("s1");
    expect(bindSession(s, "sensor", "other")).toBeNull();
    const fresh = session("s2", "sensor", null, "idle", "t");
    expect(bindSession(fresh, "sensor", "anything")).toBe("s2");
  });

  it("binds a flow session on its flow", () => {
    const s = session("s1", "flow", "triage", "idle", "t", "triage");
    expect(bindSession(s, "flow", "triage")).toBe("s1");
    expect(bindSession(s, "flow", null)).toBeNull();
  });

  it("keeps a new-flow session on the page of the flow it saved, and only there", () => {
    const s = session("s1", "flow", null, "idle", "t", "triage");
    expect(bindSession(s, "flow", null)).toBe("s1");
    expect(bindSession(s, "flow", "triage")).toBe("s1");
    expect(bindSession(s, "flow", "other")).toBeNull();
  });

  it("leaves closed sessions and other kinds alone", () => {
    expect(
      bindSession(session("s", "flow", "t", "closed", "t"), "flow", "t"),
    ).toBeNull();
    expect(
      bindSession(session("s", "flow", "t", "idle", "t"), "sensor", "t"),
    ).toBeNull();
  });
});
