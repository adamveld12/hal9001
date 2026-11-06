import { describe, expect, it } from "vitest";
import {
  approveChoice,
  setApproveChoice,
  toggleTool,
  unknownTools,
  type NodeToolRef,
} from "./toolSelection";

describe("tool selection", () => {
  it("adds and removes ids and drops the field when empty", () => {
    let refs = toggleTool(undefined, "a", true);
    refs = toggleTool(refs, "b", true);
    expect(refs).toEqual(["a", "b"]);
    refs = toggleTool(refs, "a", false);
    expect(refs).toEqual(["b"]);
    expect(toggleTool(refs, "b", false)).toBeUndefined();
  });

  it("sets and clears an approval override without touching other entries", () => {
    const refs: NodeToolRef[] = ["a", { id: "b", approve: "never" }];
    const set = setApproveChoice(refs, "a", "always");
    expect(set).toEqual([
      { id: "a", approve: "always" },
      { id: "b", approve: "never" },
    ]);
    expect(approveChoice(set, "a")).toBe("always");
    const cleared = setApproveChoice(set, "b", "default");
    expect(cleared).toEqual([{ id: "a", approve: "always" }, "b"]);
    expect(approveChoice(cleared, "b")).toBe("default");
  });

  it("keeps a conditional override and reports it as custom", () => {
    const when = { when: { conditions: [] } } as never;
    const refs: NodeToolRef[] = [{ id: "a", approve: when }];
    expect(approveChoice(refs, "a")).toBe("custom");
    expect(setApproveChoice(refs, "a", "custom")).toBe(refs);
  });

  it("finds ids with no tool file", () => {
    expect(unknownTools(["a", { id: "x" }], ["a"])).toEqual(["x"]);
  });
});
