import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type Settings = Schemas["Settings"];
export type Config = Schemas["Config"];
export type SettingsPatch = Schemas["SettingsPatch"];
export type Runtime = Schemas["Runtime"];
export type ProbeResult = Schemas["ProbeResult"];

/** `config.yaml` as JSON plus its hash. Secrets are already redacted by the daemon. */
export function useSettings() {
  return useQuery({
    queryKey: qk("settings"),
    queryFn: () => unwrap(api.GET("/api/settings")),
  });
}

/** Rejects with ApiError `conflict` when `expected_hash` is stale. */
export function useUpdateSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: SettingsPatch) =>
      unwrap(api.PATCH("/api/settings", { body: patch })),
    onSuccess: (settings) => {
      qc.setQueryData(qk("settings"), settings);
      return qc.invalidateQueries({ queryKey: qk("settings") });
    },
  });
}

/** Names of keys in the `env` file. Values are never available. */
export function useEnvKeys() {
  return useQuery({
    queryKey: qk("env"),
    queryFn: () => unwrap(api.GET("/api/env")),
    select: (d) => d.items.map((i) => i.key),
  });
}

export function useSetEnvValue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; value: string }) =>
      unwrapVoid(
        api.PUT("/api/env/{key}", {
          params: { path: { key: v.key } },
          body: { value: v.value },
        }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("env") }),
  });
}

export function useDeleteEnvKey() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (key: string) =>
      unwrapVoid(api.DELETE("/api/env/{key}", { params: { path: { key } } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("env") }),
  });
}

const fetchRuntimes = () => unwrap(api.GET("/api/runtimes"));

/** Configured ACP runtimes with their last probe result. */
export function useRuntimes() {
  return useQuery({
    queryKey: qk("runtimes"),
    queryFn: fetchRuntimes,
    select: (d) => d.items,
  });
}

/** Runtime names for pickers (flow editor agent nodes). Shares the runtimes cache entry. */
export function useRuntimeNames() {
  return useQuery({
    queryKey: qk("runtimes"),
    queryFn: fetchRuntimes,
    select: (d) => d.items.map((r) => r.name),
  });
}

/** Spawns the runtime's command and records what it reports. A failing runtime is a resolved `ok: false`. */
export function useProbeRuntime() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST("/api/runtimes/{name}/probe", { params: { path: { name } } }),
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("runtimes") }),
  });
}
