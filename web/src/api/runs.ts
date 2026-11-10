// Data hooks for run detail, agent run, and flow version pages. Keys use the
// roots the stream invalidates (invalidate.ts), so live updates need no polling.

import {
  useInfiniteQuery,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useRef } from "react";
import {
  lastEntryId,
  mergeSandboxLog,
  type SandboxLogData,
} from "../pages/runs/sandboxEntries";
import { lastContiguousSeq, mergeEvents } from "../pages/runs/transcript";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";
import { useStreamEvents } from "./streamHooks";

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

/** Entries loaded at first; "load earlier" raises it to `SANDBOX_LOG_MAX` (the API's page limit). */
const SANDBOX_TAIL = 500;
export const SANDBOX_LOG_MAX = 1000;

function fetchSandboxLog(
  scope: SandboxScope,
  id: string,
  query: { after_id?: number; limit: number; tail?: boolean },
  signal: AbortSignal,
) {
  const params = { path: { id }, query };
  return unwrap(
    scope === "agent"
      ? api.GET("/api/agent-runs/{id}/sandbox-log", { params, signal })
      : api.GET("/api/node-runs/{id}/sandbox-log", { params, signal }),
  );
}

export type SandboxScope = "agent" | "node";

/** URL of the log (`raw`: the plain-text download). */
export function sandboxLogUrl(
  scope: SandboxScope,
  id: string,
  raw = false,
): string {
  const base = scope === "agent" ? "agent-runs" : "node-runs";
  return `/api/${base}/${id}/sandbox-log${raw ? "/raw" : ""}`;
}

/**
 * Sandbox log of an agent run or a node run. The first load takes the newest
 * page; every refetch (a `sandbox_*` stream event, coalesced by the stream
 * provider) only asks for entries after the last cached id and merges them.
 * The stream's `sandbox_log.appended` payload has no output, so it is not used
 * directly. `stream.resync` and `loadEarlier` reload the newest page instead.
 */
function useSandboxLog(scope: SandboxScope, id: string | undefined) {
  const qc = useQueryClient();
  const key = qk("sandbox-log", scope, id);
  const limit = useRef(SANDBOX_TAIL);
  const reload = useRef(false);
  useStreamEvents(["stream.resync"], () => {
    reload.current = true;
  });
  const q = useQuery({
    queryKey: key,
    enabled: id !== undefined,
    structuralSharing: false,
    queryFn: async ({ signal }): Promise<SandboxLogData> => {
      const runId = id as string;
      const prev = qc.getQueryData<SandboxLogData>(key);
      if (!prev || reload.current) {
        const page = await fetchSandboxLog(
          scope,
          runId,
          { tail: true, limit: limit.current },
          signal,
        );
        reload.current = false;
        return {
          sessions: page.sessions,
          entries: page.entries,
          hasMore: page.has_more,
        };
      }
      let acc = prev;
      for (;;) {
        const page = await fetchSandboxLog(
          scope,
          runId,
          { after_id: lastEntryId(acc.entries), limit: SANDBOX_TAIL },
          signal,
        );
        acc = mergeSandboxLog(acc, page);
        if (!page.has_more || page.entries.length === 0) return acc;
      }
    },
  });
  const { refetch } = q;
  const loadEarlier = useCallback(() => {
    limit.current = SANDBOX_LOG_MAX;
    reload.current = true;
    void refetch();
  }, [refetch]);
  return { ...q, loadEarlier };
}

export function useAgentRunSandboxLog(id: string | undefined) {
  return useSandboxLog("agent", id);
}

/** Pass `undefined` for nodes that are not agent nodes: nothing is fetched. */
export function useNodeRunSandboxLog(id: string | undefined) {
  return useSandboxLog("node", id);
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

const LIST_PAGE = 50;

export interface FlowRunsQuery {
  flow_id?: string;
  status?: Schemas["FlowRunStatus"];
  kind?: Schemas["FlowRunKind"];
}

/** Newest-first flow runs; `flow_run.*` stream events refetch it via the `flow-runs` root. */
export function useFlowRunList(query: FlowRunsQuery) {
  return useInfiniteQuery({
    queryKey: qk("flow-runs", "list", query),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        api.GET("/api/flow-runs", {
          params: { query: { ...query, cursor: pageParam, limit: LIST_PAGE } },
          signal,
        }),
      ),
    getNextPageParam: (p) => p.next_cursor ?? undefined,
  });
}

export interface AgentRunsQuery {
  status?: Schemas["AgentRunStatus"];
  runtime?: string;
}

/** Newest-first agent runs; `agent_run.*` stream events refetch it via the `agent-runs` root. */
export function useAgentRunList(query: AgentRunsQuery) {
  return useInfiniteQuery({
    queryKey: qk("agent-runs", "list", query),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        api.GET("/api/agent-runs", {
          params: { query: { ...query, cursor: pageParam, limit: LIST_PAGE } },
          signal,
        }),
      ),
    getNextPageParam: (p) => p.next_cursor ?? undefined,
  });
}
