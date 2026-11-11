// Hooks for continuing an agent run in a chat and for what came from a run.
// They sit here so this slice does not edit api/conversations.ts; they can be folded into it later.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "../../api/client";
import { qk } from "../../api/keys";

export type Conversation = Schemas["Conversation"];
export type FlowRun = Schemas["FlowRun"];

const FORK_LIMIT = 50;

/** Chats forked from an agent run (any phase of its node run), newest activity first. */
export function useForksOf(agentRunId: string) {
  return useQuery({
    // Under the `assist-sessions` root so `assist.updated` events refetch it.
    queryKey: qk("assist-sessions", "forks-of", agentRunId),
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/conversations", {
          params: { query: { forked_from: agentRunId, limit: FORK_LIMIT } },
          signal,
        }),
      ),
    select: (page) => page.items,
  });
}

/** Creates a chat that continues the agent run (`POST /api/conversations/{id}/fork`). */
export function useForkConversation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (agentRunId: string) =>
      unwrap(
        api.POST("/api/conversations/{id}/fork", {
          params: { path: { id: agentRunId } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("assist-sessions") }),
  });
}

/** Continuation runs started from one original flow run, newest first. */
export function useContinuationsOf(flowRunId: string) {
  return useQuery({
    queryKey: qk("flow-runs", "continuation-of", flowRunId),
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/flow-runs", {
          params: {
            query: { continuation_of: flowRunId, limit: FORK_LIMIT },
          },
          signal,
        }),
      ),
    select: (page) => page.items,
  });
}
