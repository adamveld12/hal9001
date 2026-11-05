import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type SensorSummary = Schemas["SensorSummary"];
export type SensorDetail = Schemas["SensorDetail"];
export type SensorRun = Schemas["SensorRun"];
export type SensorValidation = Schemas["SensorValidation"];

const RUNS_PAGE_SIZE = 25;

const sensorsQuery = {
  queryKey: qk("sensors"),
  queryFn: () => unwrap(api.GET("/api/sensors")),
};

/** All sensor files with status. Refetched on sensor runs, new events and config changes. */
export function useSensors() {
  return useQuery(sensorsQuery);
}

/** Sensor ids for pickers (flow editor). Shares the cache of `useSensors`. */
export function useSensorIds() {
  return useQuery({
    ...sensorsQuery,
    select: (list) => list.items.map((s) => s.id),
  });
}

export function useSensor(id: string) {
  return useQuery({
    queryKey: qk("sensor", id),
    queryFn: () =>
      unwrap(api.GET("/api/sensors/{id}", { params: { path: { id } } })),
  });
}

const hasRunning = (runs: readonly SensorRun[]) =>
  runs.some((r) => r.status === "running");

/** Recent polls of a sensor, newest first. Polls while one is running as a fallback to the stream. */
export function useSensorRuns(id: string) {
  return useInfiniteQuery({
    queryKey: qk("sensor-runs", id, "list"),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/sensors/{id}/runs", {
          params: {
            path: { id },
            query: { cursor: pageParam, limit: RUNS_PAGE_SIZE },
          },
        }),
      ),
    getNextPageParam: (page) => page.next_cursor ?? undefined,
    refetchInterval: (q) =>
      q.state.data?.pages.some((p) => hasRunning(p.items)) ? 3000 : false,
  });
}

/** One run with its full inline stdout/stderr (list responses cut them to 4 KiB). */
export function useSensorRun(runId: string, enabled: boolean) {
  return useQuery({
    queryKey: qk("sensor-runs", "run", runId),
    queryFn: () =>
      unwrap(
        api.GET("/api/sensor-runs/{id}", { params: { path: { id: runId } } }),
      ),
    enabled,
    refetchInterval: (q) =>
      q.state.data && hasRunning([q.state.data]) ? 3000 : false,
  });
}

/** Sensors, their runs, and flows (which diagnose unknown sensors) may all change. */
function invalidateSensorFiles(qc: QueryClient) {
  const roots = ["sensors", "sensor", "sensor-runs", "flows", "flow"] as const;
  return Promise.all(
    roots.map((r) => qc.invalidateQueries({ queryKey: qk(r) })),
  );
}

export function useCreateSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (yaml: string) =>
      unwrap(api.POST("/api/sensors", { body: { yaml } })),
    onSuccess: (detail) => {
      qc.setQueryData(qk("sensor", detail.id), detail);
      void invalidateSensorFiles(qc);
    },
  });
}

export function useUpdateSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; expectedHash: string; yaml: string }) =>
      unwrap(
        api.PUT("/api/sensors/{id}", {
          params: { path: { id: v.id } },
          body: { expected_hash: v.expectedHash, yaml: v.yaml },
        }),
      ),
    onSuccess: (detail) => {
      qc.setQueryData(qk("sensor", detail.id), detail);
      void invalidateSensorFiles(qc);
    },
  });
}

export function useDeleteSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; expectedHash: string }) =>
      unwrapVoid(
        api.DELETE("/api/sensors/{id}", {
          params: {
            path: { id: v.id },
            query: { expected_hash: v.expectedHash },
          },
        }),
      ),
    onSuccess: (_void, v) => {
      qc.removeQueries({ queryKey: qk("sensor", v.id) });
      void invalidateSensorFiles(qc);
    },
  });
}

/** Validates YAML without saving. The result is for the exact text sent. */
export function useValidateSensor() {
  return useMutation({
    mutationFn: (yaml: string) =>
      unwrap(api.POST("/api/sensors/validate", { body: { yaml } })),
  });
}

/** Starts a poll outside the schedule; resolves with the new (running) SensorRun. */
export function usePollSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(api.POST("/api/sensors/{id}/poll", { params: { path: { id } } })),
    onSettled: () => void invalidateSensorFiles(qc),
  });
}

/** Pauses the scheduled polls of one sensor. "Poll now" and event injection keep working. Idempotent. */
export function usePauseSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(api.POST("/api/sensors/{id}/pause", { params: { path: { id } } })),
    onSettled: () => void invalidateSensorFiles(qc),
  });
}

/** Lets the scheduler poll the sensor again. Idempotent. */
export function useResumeSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      unwrap(
        api.POST("/api/sensors/{id}/resume", { params: { path: { id } } }),
      ),
    onSettled: () => void invalidateSensorFiles(qc),
  });
}

export type SensorEnvKey = Schemas["SensorEnvKeyList"]["items"][number];

/** Names in the sensor's own env file and whether each overrides a global key. Values are never available. */
export function useSensorEnv(id: string) {
  return useQuery({
    queryKey: qk("sensor-env", id),
    queryFn: () =>
      unwrap(api.GET("/api/sensors/{id}/env", { params: { path: { id } } })),
    select: (d) => d.items,
  });
}

export function useSetSensorEnv(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; value: string }) =>
      unwrapVoid(
        api.PUT("/api/sensors/{id}/env/{key}", {
          params: { path: { id, key: v.key } },
          body: { value: v.value },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("sensor-env", id) }),
  });
}

export function useDeleteSensorEnv(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) =>
      unwrapVoid(
        api.DELETE("/api/sensors/{id}/env/{key}", {
          params: { path: { id, key } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("sensor-env", id) }),
  });
}
