import { describe, expect, it } from "vitest";
import { kindTone } from "../../lib/status";
import {
  canContinue,
  chatPath,
  continuationBanner,
  isFlowAgentRun,
  seedKind,
} from "./continue";
import { flowRunFiltersFromParams, flowRunFiltersToQuery } from "./runList";

describe("canContinue", () => {
  it("is false while the run is active", () => {
    for (const status of ["queued", "running", "awaiting_approval"] as const) {
      expect(canContinue({ status })).toBe(false);
    }
  });
  it("is true once the run has ended", () => {
    for (const status of [
      "succeeded",
      "failed",
      "denied",
      "killed",
      "interrupted",
      "timeout",
    ] as const) {
      expect(canContinue({ status })).toBe(true);
    }
  });
});

describe("isFlowAgentRun", () => {
  it("is true for runs with a node run", () => {
    expect(isFlowAgentRun({ node_run_id: "01N" })).toBe(true);
    expect(isFlowAgentRun({ node_run_id: null })).toBe(false);
  });
});

describe("continuationBanner", () => {
  it("shapes the links and the emitted port", () => {
    expect(
      continuationBanner({
        of_run_id: "01RUN",
        conversation_id: "01CHAT",
        node_id: "triage",
        port: "escalate",
        approval_id: null,
      }),
    ).toEqual({
      ofRunId: "01RUN",
      ofRunPath: "/runs/01RUN",
      conversationId: "01CHAT",
      conversationPath: chatPath("01CHAT"),
      node: "triage",
      port: "escalate",
    });
  });
  it("is null for other runs", () => {
    expect(continuationBanner(null)).toBeNull();
  });
});

describe("seedKind", () => {
  it("reads the reasons the engine records", () => {
    expect(seedKind("reused from flow run 01RUN")).toBe("reused");
    expect(seedKind("reused from flow run 01RUN: skipped")).toBe("reused");
    expect(seedKind("emitted from conversation 01CHAT")).toBe("emitted");
    expect(seedKind("port out had no edge")).toBeNull();
    expect(seedKind(null)).toBeNull();
  });
});

describe("continuation kind", () => {
  it("is a run list filter", () => {
    const f = flowRunFiltersFromParams(
      new URLSearchParams("kind=continuation"),
    );
    expect(f.kind).toBe("continuation");
    expect(flowRunFiltersToQuery(f)).toEqual({ kind: "continuation" });
  });
  it("has its own badge tone", () => {
    expect(kindTone("continuation")).toBe("run");
    expect(kindTone("live")).toBe("muted");
    expect(kindTone("replay")).toBe("info");
  });
});
