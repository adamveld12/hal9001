// Pure helpers for continuing agent runs in a chat and for continuation flow runs.

import type { Schemas } from "../../api/client";

type AgentRun = Schemas["AgentRun"];
type Continuation = Schemas["FlowRunContinuation"];

export const CONTINUE_DISABLED_REASON = "Available when the run ends";

/** Whether the run has ended, so a chat can continue it (the server answers 409 while it runs). */
export function canContinue(run: Pick<AgentRun, "status">): boolean {
  return (
    run.status !== "queued" &&
    run.status !== "running" &&
    run.status !== "awaiting_approval"
  );
}

/** Whether a run belongs to a flow node; assist-turn runs are continued in their own chat. */
export function isFlowAgentRun(run: Pick<AgentRun, "node_run_id">): boolean {
  return run.node_run_id !== null;
}

export function chatPath(conversationId: string): string {
  return `/chat/${conversationId}`;
}

export interface ContinuationBanner {
  ofRunId: string;
  ofRunPath: string;
  conversationId: string;
  conversationPath: string;
  node: string;
  port: string;
}

/** The parts of the banner on a continuation run; null for any other run. */
export function continuationBanner(
  c: Continuation | null | undefined,
): ContinuationBanner | null {
  if (!c) return null;
  return {
    ofRunId: c.of_run_id,
    ofRunPath: `/runs/${c.of_run_id}`,
    conversationId: c.conversation_id,
    conversationPath: chatPath(c.conversation_id),
    node: c.node_id,
    port: c.port,
  };
}

/** How a node run of a continuation run came about, from the reason the engine records. */
export type SeedKind = "reused" | "emitted";

const REUSED_PREFIX = "reused from flow run ";
const EMITTED_PREFIX = "emitted from conversation ";

export function seedKind(reason: string | null | undefined): SeedKind | null {
  if (!reason) return null;
  if (reason.startsWith(REUSED_PREFIX)) return "reused";
  if (reason.startsWith(EMITTED_PREFIX)) return "emitted";
  return null;
}

export const SEED_LABEL: Record<SeedKind, string> = {
  reused: "reused",
  emitted: "emitted from conversation",
};
