import { describe, expect, it } from "vitest";
import { nodeVisual, worseVisual } from "./overlay";

describe("nodeVisual", () => {
  it("maps in-flight and terminal statuses", () => {
    expect(nodeVisual("running")).toBe("running");
    expect(nodeVisual("queued")).toBe("queued");
    expect(nodeVisual("awaiting_approval")).toBe("awaiting");
    expect(nodeVisual("killed")).toBe("killed");
    expect(nodeVisual("interrupted")).toBe("interrupted");
    expect(nodeVisual("failed")).toBe("error");
  });

  it("uses the decision for finished nodes", () => {
    expect(nodeVisual("done", "emitted")).toBe("emitted");
    expect(nodeVisual("done", "dropped")).toBe("dropped");
    expect(nodeVisual("done", "error")).toBe("error");
    expect(nodeVisual("done", "skipped")).toBe("skipped");
    expect(nodeVisual("skipped", "skipped")).toBe("skipped");
  });
});

describe("worseVisual", () => {
  it("lets an error from any fan-out branch win, and active beats finished", () => {
    expect(worseVisual("emitted", "error")).toBe("error");
    expect(worseVisual("error", "emitted")).toBe("error");
    expect(worseVisual("emitted", "running")).toBe("running");
    expect(worseVisual("dropped", "emitted")).toBe("emitted");
    expect(worseVisual("skipped", "dropped")).toBe("dropped");
  });
});
