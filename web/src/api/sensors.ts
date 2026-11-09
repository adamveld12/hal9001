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

/** The saved sensor with its current hash, read fresh. */
export async function fetchSensor(id: string): Promise<SensorDetail> {
  return unwrap(api.GET("/api/sensors/{id}", { params: { path: { id } } }));
}

/** Creates sensors/<id>.yaml from the YAML text. The file has no id line, so the id travels in the request. */
export async function createSensorFromYaml(
  id: string,
  yaml: string,
): Promise<SensorDetail> {
  return unwrap(api.POST("/api/sensors", { body: { id, yaml } }));
}

/** Replaces sensors/<id>.yaml; conflict when the file's hash is not `expectedHash`. */
export async function updateSensorFromYaml(
  id: string,
  expectedHash: string,
  yaml: string,
): Promise<SensorDetail> {
  return unwrap(
    api.PUT("/api/sensors/{id}", {
      params: { path: { id } },
      body: { expected_hash: expectedHash, yaml },
    }),
  );
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

export type PollResult = Schemas["PollResult"];
export type SensorResetResult = Schemas["SensorResetResult"];

/** A manual poll started or finished: sensors, runs, events and flow runs may all change. */
export function invalidateRunNow(qc: QueryClient) {
  const roots = [
    "sensors",
    "sensor",
    "sensor-runs",
    "flows",
    "flow",
    "flow-runs",
    "flow-run",
    "events",
    "event",
  ] as const;
  return Promise.all(
    roots.map((r) => qc.invalidateQueries({ queryKey: qk(r) })),
  );
}

/**
 * Runs the sensor outside its schedule and resolves when the poll has
 * finished, with its run and what it started. `rerunSeen` also replays items
 * already seen; `flows` limits the target flows.
 */
export function usePollSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; rerunSeen?: boolean; flows?: string[] }) =>
      unwrap(
        api.POST("/api/sensors/{id}/poll", {
          params: { path: { id: v.id } },
          body: { rerun_seen: v.rerunSeen ?? false, flows: v.flows },
        }),
      ),
    onSettled: () => void invalidateRunNow(qc),
  });
}

/** Forgets the sensor's dedup keys and incremental state; with `run` polls right after. History is kept. */
export function useResetSensor() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; run: boolean; rerunFlows?: string[] }) =>
      unwrap(
        api.POST("/api/sensors/{id}/reset", {
          params: { path: { id: v.id } },
          body: { run: v.run, rerun_flows: v.rerunFlows },
        }),
      ),
    onSettled: () => void invalidateRunNow(qc),
  });
}

/** Pauses the scheduled polls of one sensor. "Run now" and event injection keep working. Idempotent. */
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

/** Keys in the sensor's own env file: secret flag, whether each overrides a global key, and the value of plain keys only. */
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
    mutationFn: (v: { key: string; value?: string; secret?: boolean }) =>
      unwrapVoid(
        api.PUT("/api/sensors/{id}/env/{key}", {
          params: { path: { id, key: v.key } },
          body: { value: v.value, secret: v.secret },
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
