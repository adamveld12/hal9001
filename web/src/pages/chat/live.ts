// Which stream events change the transcript of a flow conversation that is
// being watched: those of its head agent run (events appended, the run's
// status or phase, its approvals).

import type { StreamEvent } from "../../api/streamContext";

/** The runs a flow conversation's transcript is made of. */
export interface WatchedRun {
  /** The last phase of the node run: the one that grows. */
  agentRunId: string | null;
  nodeRunId: string | null;
}

export const LIVE_EVENT_TYPES = [
  "agent_event.appended",
  "agent_run.created",
  "agent_run.updated",
  "approval.created",
  "approval.updated",
] as const;

export function concernsConversation(e: StreamEvent, run: WatchedRun): boolean {
  if (run.agentRunId === null || !e.data) return false;
  switch (e.type) {
    case "agent_event.appended":
    case "approval.created":
    case "approval.updated":
      return e.data.agent_run_id === run.agentRunId;
    case "agent_run.created":
    case "agent_run.updated":
      // A new phase of the node run becomes the head of the conversation.
      return (
        e.data.id === run.agentRunId ||
        (run.nodeRunId !== null && e.data.node_run_id === run.nodeRunId)
      );
    default:
      return false;
  }
}

/** Events of a burst wait this long, so a streaming run refetches the transcript a few times a second at most. */
export const LIVE_REFETCH_MS = 300;
