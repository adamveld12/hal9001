import { describe, expect, it } from "vitest";
import {
  awaitedApprovalId,
  commandSnapshot,
  indexText,
  runDuration,
  runLabels,
} from "./runView";

describe("runLabels", () => {
  it("numbers fan-out runs per node and names the upstream run", () => {
    const labels = runLabels([
      { id: "s1", node_id: "split", parent_node_run_id: null },
      { id: "a1", node_id: "ask", parent_node_run_id: "s1" },
      { id: "a2", node_id: "ask", parent_node_run_id: "s1" },
    ]);
    expect(labels.get("a2")).toEqual({
      index: 2,
      total: 2,
      parent: { nodeId: "split", index: 1, total: 1 },
    });
    expect(labels.get("s1")?.parent).toBeNull();
    expect(indexText(labels.get("a2")!)).toBe("#2/2");
    expect(indexText(labels.get("s1")!)).toBe("#1");
  });

  it("tolerates a parent that is not in the list", () => {
    const labels = runLabels([
      { id: "x", node_id: "n", parent_node_run_id: "missing" },
    ]);
    expect(labels.get("x")?.parent).toBeNull();
  });
});

describe("awaitedApprovalId", () => {
  it("reads the approval id from a parked run's reason", () => {
    expect(
      awaitedApprovalId({
        status: "awaiting_approval",
        reason: "awaiting approval ap_123",
      }),
    ).toBe("ap_123");
  });

  it("is null for other statuses and reasons", () => {
    expect(
      awaitedApprovalId({ status: "done", reason: "awaiting approval ap_1" }),
    ).toBeNull();
    expect(
      awaitedApprovalId({ status: "awaiting_approval", reason: null }),
    ).toBeNull();
    expect(
      awaitedApprovalId({ status: "awaiting_approval", reason: "approved" }),
    ).toBeNull();
  });
});

describe("runDuration", () => {
  it("prefers the recorded duration, then elapsed time, then a dash", () => {
    expect(
      runDuration({ duration_ms: 1500, started_at: null, ended_at: null }),
    ).toBe("1.5s");
    expect(
      runDuration(
        {
          duration_ms: null,
          started_at: "2026-11-05T10:00:00Z",
          ended_at: null,
        },
        Date.parse("2026-11-05T10:00:30Z"),
      ),
    ).toBe("30s");
    expect(
      runDuration({ duration_ms: null, started_at: null, ended_at: null }),
    ).toBe("–");
  });
});

describe("commandSnapshot", () => {
  it("returns the script with its language", () => {
    expect(
      commandSnapshot({
        id: "a",
        type: "command",
        lang: "bash",
        script: "echo hi",
      }),
    ).toEqual({
      lang: "bash",
      code: "echo hi",
    });
  });

  it("falls back to the argv form for exec nodes", () => {
    expect(
      commandSnapshot({
        id: "a",
        type: "command",
        lang: "exec",
        command: ["git", "status"],
      }),
    ).toEqual({ lang: "exec", code: "git status" });
  });

  it("has nothing for agent and inbox nodes", () => {
    expect(
      commandSnapshot({
        id: "a",
        type: "agent",
        runtime: "claude",
        mode: "auto",
        prompt: "p",
      }),
    ).toBeNull();
  });
});
