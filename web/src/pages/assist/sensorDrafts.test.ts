import { describe, expect, it } from "vitest";
import type { Diagnostic } from "../files/diagnostics";
import {
  canvasDiagnostics,
  findingLabel,
  sensorDraftFile,
} from "./sensorDrafts";

const finding = (over: Partial<Diagnostic> = {}): Diagnostic => ({
  severity: "error",
  code: "invalid_duration",
  message: "interval is not a duration",
  ...over,
});

describe("findings about draft sensors", () => {
  it("tells a sensor's findings from the others by their file", () => {
    expect(findingLabel(finding({ file: sensorDraftFile("gh") }))).toBe(
      "sensor gh: ",
    );
    expect(findingLabel(finding({ file: "flow.yaml" }))).toBe("");
    expect(findingLabel(finding({ file: "prompts/p.md" }))).toBe("");
    expect(findingLabel(finding())).toBe("");
  });

  it("says which sensor a message on the canvas is about and leaves the others alone", () => {
    const flowFinding = finding({ message: "unknown node" });
    const [first, second] = canvasDiagnostics([
      flowFinding,
      finding({ file: "sensors/gh.yaml" }),
    ]);
    expect(first).toBe(flowFinding);
    expect(second?.message).toBe("sensor gh: interval is not a duration");
  });
});
