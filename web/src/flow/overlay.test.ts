import { describe, expect, it } from "vitest";
import { gateOutcome, nodeVisual, worseVisual } from "./overlay";

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

  it("shows a gate's denial as denied, not emitted or error", () => {
    const denied = { error: "denied", ports: ["denied"] };
    expect(nodeVisual("done", "emitted", denied)).toBe("denied");
    expect(
      nodeVisual("done", "emitted", {
        error: "approval_timeout",
        ports: ["denied"],
      }),
    ).toBe("denied");
    // Ordinary runs, and a failure that merely has the same class, keep their visual.
    expect(nodeVisual("done", "emitted", { error: null, ports: ["out"] })).toBe(
      "emitted",
    );
    expect(nodeVisual("failed", "error", denied)).toBe("error");
  });
});

describe("gateOutcome", () => {
  it("tells a denial from a timeout, and needs the denied port", () => {
    expect(
      gateOutcome({ status: "done", error: "denied", ports: ["denied"] }),
    ).toBe("denied");
    expect(
      gateOutcome({
        status: "done",
        error: "approval_timeout",
        ports: ["denied"],
      }),
    ).toBe("timeout");
    expect(
      gateOutcome({ status: "done", error: "denied", ports: ["out"] }),
    ).toBeNull();
    expect(
      gateOutcome({ status: "done", error: null, ports: ["denied"] }),
    ).toBeNull();
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
