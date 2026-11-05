import { describe, expect, it } from "vitest";
import type { EventDetail } from "../../api/events";
import { decisionCounts, decisionPathText, flattenBranch } from "./tree";

type TreeNode = EventDetail["fan_out"][number]["node_runs"][number];

function node(
  id: string,
  decision: string | null,
  children: TreeNode[] = [],
  status = "done",
): TreeNode {
  return {
    node_run: {
      id: `nr-${id}`,
      node_id: id,
      node_type: "command",
      status,
      decision,
      reason: null,
      ports: [],
      emitted_count: decision === "emitted" ? 1 : 0,
    },
    children,
  } as unknown as TreeNode;
}

describe("flattenBranch", () => {
  const branch = [
    node("filter", "emitted", [
      node("triage", "emitted", [node("notify", "skipped")]),
      node("log", "dropped"),
    ]),
  ];

  it("walks depth first with nesting depth", () => {
    expect(flattenBranch(branch).map((s) => [s.nodeId, s.depth])).toEqual([
      ["filter", 0],
      ["triage", 1],
      ["notify", 2],
      ["log", 1],
    ]);
  });

  it("renders a decision path, falling back to status while unfinished", () => {
    const steps = flattenBranch([
      node("a", "emitted", [node("b", null, [], "running")]),
    ]);
    expect(decisionPathText(steps)).toBe("a:emitted → b:running");
  });

  it("counts decisions", () => {
    expect(decisionCounts(flattenBranch(branch))).toEqual({
      emitted: 2,
      skipped: 1,
      dropped: 1,
    });
  });

  it("handles an empty branch", () => {
    expect(flattenBranch([])).toEqual([]);
    expect(decisionPathText([])).toBe("");
  });
});
