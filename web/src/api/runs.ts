// Data hooks for run detail, agent run, and flow version pages. Keys use the
// roots the stream invalidates (invalidate.ts), so live updates need no polling.

import {
  useInfiniteQuery,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { lastContiguousSeq, mergeEvents } from "../pages/runs/transcript";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type FlowRunDetail = Schemas["FlowRunDetail"];
export type NodeRunDetail = Schemas["NodeRunDetail"];
export type NodeRun = Schemas["NodeRun"];
export type AgentRunDetail = Schemas["AgentRunDetail"];
export type AgentRun = Schemas["AgentRun"];
export type AgentEvent = Schemas["AgentEvent"];
export type PermissionRequest = Schemas["PermissionRequest"];
export type Approval = Schemas["Approval"];
export type FlowVersion = Schemas["FlowVersion"];
export type FlowVersionSummary = Schemas["FlowVersionSummary"];

const EVENT_PAGE = 500;

export function useFlowRun(id: string) {
  return useQuery({
    queryKey: qk("flow-run", id),
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/flow-runs/{id}", { params: { path: { id } }, signal }),
      ),
  });
}

export function useNodeRun(id: string | undefined) {
  return useQuery({
    queryKey: qk("node-run", id),
    enabled: id !== undefined,
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/node-runs/{id}", {
          params: { path: { id: id as string } },
          signal,
        }),
      ),
  });
}

/** Every agent run of a flow run (all phases), newest first. */
export function useFlowRunAgentRuns(flowRunId: string) {
  return useQuery({
    queryKey: qk("agent-runs", { flow_run_id: flowRunId }),
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/agent-runs", {
          params: { query: { flow_run_id: flowRunId, limit: 200 } },
          signal,
        }),
      ),
    select: (page) => page.items,
  });
}

export function useAgentRun(id: string | undefined) {
  return useQuery({
    queryKey: qk("agent-run", id),
    enabled: id !== undefined,
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/agent-runs/{id}", {
          params: { path: { id: id as string } },
          signal,
        }),
      ),
  });
}

/** Details of every phase run of a node run, sharing the `useAgentRun` cache. */
export function usePhaseRuns(ids: readonly string[]) {
  return useQueries({
    queries: ids.map((id) => ({
      queryKey: qk("agent-run", id),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        unwrap(
          api.GET("/api/agent-runs/{id}", { params: { path: { id } }, signal }),
        ),
    })),
    combine: (results) => ({
      pending: results.some((r) => r.isPending),
      error: results.find((r) => r.isError)?.error ?? null,
      runs: results.flatMap((r) => (r.data ? [r.data] : [])),
    }),
  });
}

export function usePermissionRequests(agentRunId: string) {
  return useQuery({
    queryKey: qk("permission-requests", agentRunId),
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/agent-runs/{id}/permission-requests", {
          params: { path: { id: agentRunId } },
          signal,
        }),
      ),
    select: (list) => list.items,
  });
}

async function fetchEventsAfter(
  agentRunId: string,
  afterSeq: number,
  signal: AbortSignal,
): Promise<AgentEvent[]> {
  const out: AgentEvent[] = [];
  let after = afterSeq;
  for (;;) {
    const page = await unwrap(
      api.GET("/api/agent-runs/{id}/events", {
        params: {
          path: { id: agentRunId },
          query: { after_seq: after, limit: EVENT_PAGE },
        },
        signal,
      }),
    );
    out.push(...page.items);
    if (!page.has_more || page.items.length === 0) return out;
    after = page.last_seq;
  }
}

/**
 * The run's transcript in seq order. The first load pages through the whole
 * log; each later refetch (triggered by `agent_event.appended` or a resync)
 * only asks for events after the last cached seq and merges them without
 * duplicates. Stream payloads are truncated previews, so they are not used.
 */
export function useAgentTranscript(agentRunId: string) {
  const qc = useQueryClient();
  return useQuery({
    queryKey: qk("agent-events", agentRunId),
    structuralSharing: false,
    queryFn: async ({ signal }) => {
      const prev =
        qc.getQueryData<AgentEvent[]>(qk("agent-events", agentRunId)) ?? [];
      const afterSeq = lastContiguousSeq(prev);
      const fresh = await fetchEventsAfter(agentRunId, afterSeq, signal);
      return mergeEvents(prev, fresh);
    },
  });
}

/** One version is immutable, so it never refetches. */
export function useFlowVersion(
  flowId: string | undefined,
  hash: string | undefined,
) {
  return useQuery({
    queryKey: qk("flow-version", flowId, hash),
    enabled: flowId !== undefined && hash !== undefined,
    staleTime: Infinity,
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/flows/{id}/versions/{hash}", {
          params: { path: { id: flowId as string, hash: hash as string } },
          signal,
        }),
      ),
  });
}

export function useFlowVersions(flowId: string) {
  return useInfiniteQuery({
    queryKey: qk("flow-versions", flowId),
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        api.GET("/api/flows/{id}/versions", {
          params: {
            path: { id: flowId },
            query: { limit: 50, cursor: pageParam },
          },
          signal,
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (p) => p.next_cursor ?? undefined,
  });
}

/** Sensor that produced a run's event; disabled when the run has no event. */
export function useEventSensor(eventId: string | null) {
  return useQuery({
    queryKey: qk("event", eventId, "sensor"),
    enabled: eventId !== null,
    staleTime: Infinity,
    queryFn: ({ signal }) =>
      unwrap(
        api.GET("/api/events/{id}", {
          params: { path: { id: eventId as string } },
          signal,
        }),
      ),
    select: (d) => d.event.sensor_id,
  });
}
