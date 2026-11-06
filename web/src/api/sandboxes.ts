import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type SandboxConfig = Schemas["Sandbox"];
export type SandboxDetail = Schemas["SandboxDetail"];
export type SandboxSummary = Schemas["SandboxSummary"];
export type SandboxBackend = Schemas["SandboxBackendName"];
export type SandboxCheckResult = Schemas["SandboxCheckResult"];
export type SandboxEnvKey = Schemas["SandboxEnvKeyList"]["items"][number];

export const SANDBOX_BACKENDS: readonly {
  key: SandboxBackend;
  label: string;
}[] = [
  { key: "local", label: "Local" },
  { key: "docker", label: "Docker" },
  { key: "depot", label: "Depot" },
  { key: "kubernetes", label: "Kubernetes" },
  { key: "ssh", label: "SSH" },
];

export function useSandboxes() {
  return useQuery({
    queryKey: qk("sandboxes"),
    queryFn: () => unwrap(api.GET("/api/sandboxes")),
    select: (d) => d.items,
  });
}

export function useSandbox(backend: SandboxBackend) {
  return useQuery({
    queryKey: qk("sandbox", backend),
    queryFn: () =>
      unwrap(
        api.GET("/api/sandboxes/{backend}", { params: { path: { backend } } }),
      ),
  });
}

/** Rejects with ApiError `conflict` when `expected_hash` is stale, `validation` with diagnostics when invalid. */
export function useUpdateSandbox(backend: SandboxBackend) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { expected_hash: string; config: SandboxConfig }) =>
      unwrap(
        api.PUT("/api/sandboxes/{backend}", {
          params: { path: { backend } },
          body: v,
        }),
      ),
    onSuccess: (detail) => {
      qc.setQueryData(qk("sandbox", backend), detail);
      void qc.invalidateQueries({ queryKey: qk("sandboxes") });
    },
  });
}

/** Tests the given (possibly unsaved) config with the backend's stored environment. A failed check resolves with `ok: false`. */
export function useCheckSandbox(backend: SandboxBackend) {
  return useMutation({
    mutationFn: (config: SandboxConfig) =>
      unwrap(
        api.POST("/api/sandboxes/{backend}/check", {
          params: { path: { backend } },
          body: { config },
        }),
      ),
  });
}

export function useSandboxEnv(backend: SandboxBackend) {
  return useQuery({
    queryKey: qk("sandbox-env", backend),
    queryFn: () =>
      unwrap(
        api.GET("/api/sandboxes/{backend}/env", {
          params: { path: { backend } },
        }),
      ),
    select: (d) => d.items,
  });
}

export function useSetSandboxEnv(backend: SandboxBackend) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; value?: string; secret?: boolean }) =>
      unwrapVoid(
        api.PUT("/api/sandboxes/{backend}/env/{key}", {
          params: { path: { backend, key: v.key } },
          body: { value: v.value, secret: v.secret },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: qk("sandbox-env", backend) }),
  });
}

export function useDeleteSandboxEnv(backend: SandboxBackend) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) =>
      unwrapVoid(
        api.DELETE("/api/sandboxes/{backend}/env/{key}", {
          params: { path: { backend, key } },
        }),
      ),
    onSuccess: () =>
      qc.invalidateQueries({ queryKey: qk("sandbox-env", backend) }),
  });
}
