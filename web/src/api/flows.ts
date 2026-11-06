import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { flowFromApi, flowToApi } from "../flow/apiAdapter";
import type { Flow } from "../flow/model";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";
import { invalidateRunNow } from "./sensors";

export type FlowSummary = Schemas["FlowSummary"];
export type FlowDetail = Schemas["FlowDetail"];
export type FlowValidation = Schemas["FlowValidation"];
export type TestNodeResult = Schemas["TestNodeResult"];
export type TestNodeRequest = Schemas["TestNodeRequest"];
export type ApiFlow = Schemas["Flow"];
export type FlowRunNowResult = Schemas["FlowRunNowResult"];

/** The flow the daemon reads from `yaml`, with the editor's defaults; null when the YAML does not parse. */
export async function parseFlowYaml(yaml: string): Promise<Flow | null> {
  const v = await unwrap(api.POST("/api/flows/validate", { body: { yaml } }));
  return v.flow ? flowFromApi(v.flow) : null;
}

/** The parse of a draft's YAML, cached by the draft's hash (the same text always parses the same). */
export function flowDraftParseQuery(hash: string, yaml: string) {
  return queryOptions({
    queryKey: qk("flow", "draft-parse", hash),
    staleTime: Infinity,
    queryFn: async () => ({ flow: await parseFlowYaml(yaml) }),
  });
}

/** The saved flow with its current hash, read fresh. */
export async function fetchFlow(id: string): Promise<FlowDetail> {
  return unwrap(api.GET("/api/flows/{id}", { params: { path: { id } } }));
}

/** The canonical YAML the daemon writes for `flow`. */
export async function renderFlowYaml(flow: Flow): Promise<string | null> {
  const v = await unwrap(
    api.POST("/api/flows/validate", { body: { flow: flowToApi(flow) } }),
  );
  return v.yaml;
}

/** Creates flows/<id>.yaml from the draft's YAML text. */
export async function createFlowFromYaml(yaml: string): Promise<FlowDetail> {
  return unwrap(api.POST("/api/flows", { body: { yaml } }));
}

/** Replaces flows/<id>.yaml; conflict when the file's hash is not `expectedHash`. */
export async function updateFlowFromYaml(
  id: string,
  expectedHash: string,
  yaml: string,
): Promise<FlowDetail> {
  return unwrap(
    api.PUT("/api/flows/{id}", {
      params: { path: { id } },
      body: { expected_hash: expectedHash, yaml },
    }),
  );
}

export function useFlows() {
  return useQuery({
    queryKey: qk("flows"),
    queryFn: async () => (await unwrap(api.GET("/api/flows"))).items,
  });
}

export function useFlow(id: string) {
  return useQuery({
    queryKey: qk("flow", id),
    queryFn: () =>
      unwrap(api.GET("/api/flows/{id}", { params: { path: { id } } })),
  });
}

export function useCreateFlow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (flow: ApiFlow) =>
      unwrap(api.POST("/api/flows", { body: { flow } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("flows") }),
  });
}

/** Save the editor's flow JSON; `expectedHash` is the hash of the file as loaded. */
export function useUpdateFlow(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { expectedHash: string; flow?: ApiFlow; yaml?: string }) =>
      unwrap(
        api.PUT("/api/flows/{id}", {
          params: { path: { id } },
          body: {
            expected_hash: v.expectedHash,
            flow: v.flow,
            yaml: v.yaml,
          } as Schemas["FlowUpdateRequest"],
        }),
      ),
    onSuccess: (detail) => {
      qc.setQueryData(qk("flow", id), detail);
      void qc.invalidateQueries({ queryKey: qk("flows") });
      void qc.invalidateQueries({ queryKey: qk("flow-versions") });
    },
  });
}

export function useDeleteFlow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; expectedHash: string }) =>
      unwrapVoid(
        api.DELETE("/api/flows/{id}", {
          params: {
            path: { id: v.id },
            query: { expected_hash: v.expectedHash },
          },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("flows") }),
  });
}

/** Server-side validation (references to sensors/prompts/runtimes, templates). */
export function useValidateFlow() {
  return useMutation({
    mutationFn: (input: { flow?: ApiFlow; yaml?: string }) =>
      unwrap(
        api.POST("/api/flows/validate", {
          body: input as Schemas["FlowValidateRequest"],
        }),
      ),
  });
}

export function useTestNode(flowId: string) {
  return useMutation({
    mutationFn: (req: TestNodeRequest) =>
      unwrap(
        api.POST("/api/flows/{id}/test-node", {
          params: { path: { id: flowId } },
          body: req,
        }),
      ),
  });
}

/** Recent live/replay events of the given sensors, for "run node on a stored event". */
export function useRecentEvents(
  sensorId: string | undefined,
  enabled: boolean,
) {
  return useQuery({
    queryKey: qk("events", "sample", sensorId ?? ""),
    enabled,
    queryFn: async () =>
      (
        await unwrap(
          api.GET("/api/events", {
            params: { query: { limit: 20, sensor_id: sensorId } },
          }),
        )
      ).items,
  });
}

/** Recent runs of a flow with node runs, for "run node on an upstream node's output". */
export function useFlowRunDetail(runId: string | undefined) {
  return useQuery({
    queryKey: qk("flow-run", runId ?? ""),
    enabled: runId !== undefined,
    queryFn: () =>
      unwrap(
        api.GET("/api/flow-runs/{id}", {
          params: { path: { id: runId as string } },
        }),
      ),
  });
}

export function useFlowRuns(flowId: string, enabled: boolean) {
  return useQuery({
    queryKey: qk("flow-runs", "of-flow", flowId),
    enabled,
    queryFn: async () =>
      (
        await unwrap(
          api.GET("/api/flow-runs", {
            params: { query: { flow_id: flowId, limit: 15 } },
          }),
        )
      ).items,
  });
}

/** Runs the saved version of a flow once: polls each subscribed sensor and resolves when all polls finished. */
export function useRunFlow() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; rerunSeen?: boolean }) =>
      unwrap(
        api.POST("/api/flows/{id}/run", {
          params: { path: { id: v.id } },
          body: { rerun_seen: v.rerunSeen ?? false },
        }),
      ),
    onSettled: () => void invalidateRunNow(qc),
  });
}
