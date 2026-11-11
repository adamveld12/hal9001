// Labels and flags of a conversation, shared by the list rows and the header.

import type {
  Conversation,
  ConversationForkRef,
  ConversationStatus,
} from "../../api/conversations";
import { shortId } from "../../lib/format";
import type { Tone } from "../../lib/status";

const LABEL: Record<ConversationStatus, string> = {
  running: "running",
  awaiting_approval: "awaiting approval",
  idle: "idle",
  succeeded: "succeeded",
  failed: "failed",
  killed: "killed",
  denied: "denied",
  interrupted: "interrupted",
  timeout: "timed out",
  closed: "ended",
};

const TONE: Record<ConversationStatus, Tone> = {
  running: "run",
  awaiting_approval: "warn",
  idle: "muted",
  succeeded: "ok",
  failed: "bad",
  killed: "bad",
  denied: "bad",
  interrupted: "warn",
  timeout: "bad",
  closed: "muted",
};

export function statusLabel(status: ConversationStatus): string {
  return LABEL[status];
}

export function statusTone(status: ConversationStatus): Tone {
  return TONE[status];
}

/** A turn or run is in progress: the row shows it as live and the transcript follows it. */
export function isLive(status: ConversationStatus): boolean {
  return status === "running" || status === "awaiting_approval";
}

export function sourceLabel(source: Conversation["source"]): string {
  return source === "flow" ? "Flow run" : "Chat";
}

/** `flow / node` of a flow run, or the flow alone; null when neither is known. */
export function flowNodeLabel(
  c: Pick<Conversation, "flow_id" | "node_id">,
): string | null {
  if (c.flow_id && c.node_id) return `${c.flow_id} / ${c.node_id}`;
  return c.flow_id ?? c.node_id;
}

/** A finished flow conversation can be continued in a chat; the server decides (`continuable`). */
export function canContinue(
  c: Pick<Conversation, "source" | "continuable" | "running">,
): boolean {
  return c.source === "flow" && c.continuable && !c.running;
}

/** `Continued from run 6P8R0S1T` (the short run id), the link text of a fork. */
export function forkOriginLabel(ref: ConversationForkRef): string {
  return `Continued from run ${shortId(ref.agent_run_id)}`;
}

/** "3 forks", "1 fork"; empty for none. */
export function forkCountLabel(n: number): string {
  if (n <= 0) return "";
  return n === 1 ? "1 fork" : `${n} forks`;
}

export interface ConversationLink {
  label: string;
  to: string;
}

/**
 * Where a conversation came from, as links for its header: the run it
 * continues, the flow run, the node in it, the agent run and the flow. A fork
 * takes the flow run and node from the run it continues.
 */
export function conversationLinks(c: Conversation): ConversationLink[] {
  const links: ConversationLink[] = [];
  if (c.forked_from) {
    links.push({
      label: forkOriginLabel(c.forked_from),
      to: `/agent-runs/${c.forked_from.agent_run_id}`,
    });
  }
  const flowRunId = c.flow_run_id ?? c.forked_from?.flow_run_id ?? null;
  const nodeId = c.node_id ?? c.forked_from?.node_id ?? null;
  if (flowRunId) {
    links.push({ label: "Flow run", to: `/runs/${flowRunId}` });
    if (nodeId) {
      links.push({
        label: `Node ${nodeId}`,
        to: `/runs/${flowRunId}?node=${encodeURIComponent(nodeId)}`,
      });
    }
  }
  if (c.agent_run_id) {
    links.push({ label: "Agent run", to: `/agent-runs/${c.agent_run_id}` });
  }
  if (c.flow_id) {
    links.push({
      label: `Flow ${c.flow_id}`,
      to: `/flows/${encodeURIComponent(c.flow_id)}`,
    });
  }
  return links;
}
