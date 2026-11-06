import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type Tool = Schemas["Tool"];
export type ToolParam = Schemas["ToolParam"];
export type ToolApprove = Schemas["ToolApprove"];
export type ToolSummary = Schemas["ToolSummary"];
export type ToolDetail = Schemas["ToolDetail"];
export type ToolValidation = Schemas["ToolValidation"];
export type ToolTestResult = Schemas["ToolTestResult"];

const toolsQuery = {
  queryKey: qk("tools"),
  queryFn: () => unwrap(api.GET("/api/tools")),
};

/** All tool files. Refetched on config changes. */
export function useTools() {
  return useQuery(toolsQuery);
}

export function useTool(id: string) {
  return useQuery({
    queryKey: qk("tool", id),
    queryFn: () =>
      unwrap(api.GET("/api/tools/{id}", { params: { path: { id } } })),
  });
}

function invalidateTools(qc: QueryClient) {
  return Promise.all(
    (["tools", "tool"] as const).map((r) =>
      qc.invalidateQueries({ queryKey: qk(r) }),
    ),
  );
}

export function useCreateTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (yaml: string) =>
      unwrap(api.POST("/api/tools", { body: { yaml } })),
    onSuccess: (detail) => {
      qc.setQueryData(qk("tool", detail.id), detail);
      void invalidateTools(qc);
    },
  });
}

export function useUpdateTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; expectedHash: string; yaml: string }) =>
      unwrap(
        api.PUT("/api/tools/{id}", {
          params: { path: { id: v.id } },
          body: { expected_hash: v.expectedHash, yaml: v.yaml },
        }),
      ),
    onSuccess: (detail) => {
      qc.setQueryData(qk("tool", detail.id), detail);
      void invalidateTools(qc);
    },
  });
}

export function useDeleteTool() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; expectedHash: string }) =>
      unwrapVoid(
        api.DELETE("/api/tools/{id}", {
          params: {
            path: { id: v.id },
            query: { expected_hash: v.expectedHash },
          },
        }),
      ),
    onSuccess: (_void, v) => {
      qc.removeQueries({ queryKey: qk("tool", v.id) });
      void invalidateTools(qc);
    },
  });
}

/** Validates YAML without saving; the response has the parsed tool and its effective schema. */
export function useValidateTool() {
  return useMutation({
    mutationFn: (yaml: string) =>
      unwrap(api.POST("/api/tools/validate", { body: { yaml } })),
  });
}

/** Runs the saved tool once with `args`. A failing tool is a normal result (`ok: false`). */
export function useTestTool(id: string) {
  return useMutation({
    mutationFn: (args: Record<string, unknown>) =>
      unwrap(
        api.POST("/api/tools/{id}/test", {
          params: { path: { id } },
          body: { args },
        }),
      ),
  });
}

export function useToolEnv(id: string) {
  return useQuery({
    queryKey: qk("tool-env", id),
    queryFn: () =>
      unwrap(api.GET("/api/tools/{id}/env", { params: { path: { id } } })),
    select: (d) => d.items,
  });
}

export function useSetToolEnv(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; value?: string; secret?: boolean }) =>
      unwrapVoid(
        api.PUT("/api/tools/{id}/env/{key}", {
          params: { path: { id, key: v.key } },
          body: { value: v.value, secret: v.secret },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("tool-env", id) }),
  });
}

export function useDeleteToolEnv(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) =>
      unwrapVoid(
        api.DELETE("/api/tools/{id}/env/{key}", {
          params: { path: { id, key } },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("tool-env", id) }),
  });
}
