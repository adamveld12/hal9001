import { describe, expect, it } from "vitest";
import { withMeasured } from "./measured";

describe("withMeasured", () => {
  const nodes = [{ id: "a" }, { id: "b" }];

  it("returns the same array while nothing is measured", () => {
    expect(withMeasured(nodes, new Map())).toBe(nodes);
  });

  it("attaches sizes by node id and leaves unmeasured nodes alone", () => {
    const out = withMeasured(
      nodes,
      new Map([["b", { width: 200, height: 80 }]]),
    );
    expect(out[0]).toBe(nodes[0]);
    expect(out[1]).toEqual({ id: "b", measured: { width: 200, height: 80 } });
  });
});
