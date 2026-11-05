import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type EventSummary = Schemas["EventSummary"];
export type EventDetail = Schemas["EventDetail"];
export type FlowRunBrief = Schemas["FlowRunBrief"];

/** Server-side filters of GET /api/events (all of them are applied by the server). */
export interface EventsQuery {
  sensor_id?: string;
  flow_id?: string;
  status?: Schemas["FlowRunStatus"];
  decision?: Schemas["NodeRunDecision"];
  kind?: Schemas["FlowRunKind"];
  q?: string;
}

const PAGE_SIZE = 50;

/** Infinite, newest-first event feed. SSE `event.created` / `flow_run.*` refetch it via the `events` root. */
export function useEvents(query: EventsQuery) {
  return useInfiniteQuery({
    queryKey: qk("events", "feed", query),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/events", {
          params: { query: { ...query, cursor: pageParam, limit: PAGE_SIZE } },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
  });
}

/** Sensor ids for the filter and inject pickers. */
export function useSensorOptions() {
  return useQuery({
    queryKey: qk("sensors", "picker"),
    queryFn: async () =>
      (await unwrap(api.GET("/api/sensors"))).items.map((s) => ({
        id: s.id,
        valid: s.sensor !== null,
      })),
  });
}

/** Flow ids for the filter and replay pickers. */
export function useFlowOptions() {
  return useQuery({
    queryKey: qk("flows", "picker"),
    queryFn: async () =>
      (await unwrap(api.GET("/api/flows"))).items.map((f) => ({
        id: f.id,
        enabled: f.enabled,
        valid: f.enabled !== null,
      })),
  });
}

export function useEventDetail(id: string) {
  return useQuery({
    queryKey: qk("event", id),
    queryFn: () =>
      unwrap(api.GET("/api/events/{id}", { params: { path: { id } } })),
  });
}

/** Injects a raw JSON item as if the sensor had polled it. */
export function useInjectEvent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sensorId, item }: { sensorId: string; item: unknown }) =>
      unwrap(
        api.POST("/api/events", {
          params: { query: { sensor_id: sensorId } },
          body: item as never,
        }),
      ),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: qk("events") }),
        qc.invalidateQueries({ queryKey: qk("flow-runs") }),
        qc.invalidateQueries({ queryKey: qk("sensors") }),
      ]),
  });
}

export function useReplayEvent(eventId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["ReplayRequest"]) =>
      unwrap(
        api.POST("/api/events/{id}/replay", {
          params: { path: { id: eventId } },
          body: req,
        }),
      ),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: qk("events") }),
        qc.invalidateQueries({ queryKey: qk("event", eventId) }),
        qc.invalidateQueries({ queryKey: qk("flow-runs") }),
      ]),
  });
}
