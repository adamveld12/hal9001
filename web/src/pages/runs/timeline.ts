// Phase timeline of an agent node: propose run -> approval -> execute run
// (REQUIREMENTS §7.2). Pure.

import type { Schemas } from "../../api/client";

type AgentRun = Schemas["AgentRun"];
type Approval = Schemas["Approval"];

export interface PhaseRunInput {
  run: AgentRun;
  /** Approvals raised by this run (AgentRunDetail.approvals). */
  approvals: Approval[];
}

export type TimelineStep =
  | {
      kind: "run";
      runId: string;
      phase: AgentRun["phase"];
      status: AgentRun["status"];
      resumedVia: AgentRun["resumed_via"];
      resumeFallback: string | null;
      startedAt: string | null;
      endedAt: string | null;
      current: boolean;
    }
  | {
      kind: "approval";
      approvalId: string;
      status: Approval["status"];
      decidedBy: string | null;
      decidedAt: string | null;
      createdAt: string;
    };

/**
 * One step per phase run in the given (phase) order; each proposal approval
 * is placed right after the run that raised it. Per-tool approvals are not
 * phase steps (see the permission request list).
 */
export function buildPhaseTimeline(
  phases: readonly PhaseRunInput[],
  currentRunId: string,
): TimelineStep[] {
  const steps: TimelineStep[] = [];
  for (const { run, approvals } of phases) {
    steps.push({
      kind: "run",
      runId: run.id,
      phase: run.phase,
      status: run.status,
      resumedVia: run.resumed_via,
      resumeFallback: run.resume_fallback,
      startedAt: run.started_at,
      endedAt: run.ended_at,
      current: run.id === currentRunId,
    });
    for (const a of approvals) {
      if (a.type !== "proposal") continue;
      steps.push({
        kind: "approval",
        approvalId: a.id,
        status: a.status,
        decidedBy: a.decided_by,
        decidedAt: a.decided_at,
        createdAt: a.created_at,
      });
    }
  }
  return steps;
}
