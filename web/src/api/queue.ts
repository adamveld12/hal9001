import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, errorMessage, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type Queue = Schemas["Queue"];
export type QueueItem = Schemas["QueueItem"];
export type QueueLimits = Schemas["QueueLimits"];

/** The run queue; flow-run, node-run and kill-switch stream events refetch it via the `queue` root. */
export function useQueue(flowId?: string) {
  return useQuery({
    queryKey: qk("queue", flowId ?? ""),
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/queue", {
          params: { query: flowId ? { flow_id: flowId } : {} },
          signal,
        }),
      ),
  });
}

export interface RemoveResult {
  removed: string[];
  failed: { id: string; message: string }[];
}

/**
 * Remove runs from the queue one by one. A run that started in the meantime
 * is refused by the server (409); it is reported in `failed`, the others
 * are still removed.
 */
export function useRemoveFromQueue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (ids: readonly string[]): Promise<RemoveResult> => {
      const out: RemoveResult = { removed: [], failed: [] };
      for (const id of ids) {
        try {
          await unwrap(
            api.DELETE("/api/queue/{flow_run_id}", {
              params: { path: { flow_run_id: id } },
            }),
          );
          out.removed.push(id);
        } catch (e) {
          out.failed.push({ id, message: errorMessage(e) });
        }
      }
      return out;
    },
    onSettled: () => qc.invalidateQueries({ queryKey: qk("queue") }),
  });
}

export function useClearQueue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (flowId: string) =>
      unwrap(
        api.POST("/api/queue/clear", {
          body: flowId ? { flow_id: flowId } : {},
        }),
      ),
    onSettled: () => qc.invalidateQueries({ queryKey: qk("queue") }),
  });
}
