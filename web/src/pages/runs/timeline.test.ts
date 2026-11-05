import { describe, expect, it } from "vitest";
import type { Schemas } from "../../api/client";
import { buildPhaseTimeline } from "./timeline";

function run(
  id: string,
  phase: "propose" | "execute",
  over: Partial<Schemas["AgentRun"]> = {},
): Schemas["AgentRun"] {
  return {
    id,
    phase,
    status: "succeeded",
    resumed_via: null,
    resume_fallback: null,
    started_at: "2026-11-05T10:00:00Z",
    ended_at: "2026-11-05T10:01:00Z",
    ...over,
  } as Schemas["AgentRun"];
}

function approval(
  id: string,
  type: "proposal" | "tool_call",
  over: Partial<Schemas["Approval"]> = {},
): Schemas["Approval"] {
  return {
    id,
    type,
    status: "approved",
    decided_by: "adam",
    decided_at: "2026-11-05T10:02:00Z",
    created_at: "2026-11-05T10:01:30Z",
    ...over,
  } as Schemas["Approval"];
}

describe("buildPhaseTimeline", () => {
  it("orders propose -> approval -> execute and marks the current run", () => {
    const steps = buildPhaseTimeline(
      [
        { run: run("p", "propose"), approvals: [approval("a1", "proposal")] },
        {
          run: run("e", "execute", {
            resumed_via: "new_session",
            resume_fallback: "load unsupported",
          }),
          approvals: [],
        },
      ],
      "e",
    );
    expect(
      steps.map((s) =>
        s.kind === "run" ? `run:${s.phase}` : `approval:${s.status}`,
      ),
    ).toEqual(["run:propose", "approval:approved", "run:execute"]);
    expect(steps[0]).toMatchObject({ current: false });
    expect(steps[2]).toMatchObject({
      current: true,
      resumedVia: "new_session",
      resumeFallback: "load unsupported",
    });
  });

  it("leaves per-tool approvals out of the phase steps", () => {
    const steps = buildPhaseTimeline(
      [{ run: run("e", "execute"), approvals: [approval("t1", "tool_call")] }],
      "e",
    );
    expect(steps).toHaveLength(1);
  });

  it("keeps a pending proposal after its run", () => {
    const steps = buildPhaseTimeline(
      [
        {
          run: run("p", "propose", {
            status: "awaiting_approval",
            ended_at: null,
          }),
          approvals: [
            approval("a1", "proposal", {
              status: "pending",
              decided_by: null,
              decided_at: null,
            }),
          ],
        },
      ],
      "p",
    );
    expect(steps[1]).toMatchObject({
      kind: "approval",
      status: "pending",
      decidedBy: null,
    });
  });
});
