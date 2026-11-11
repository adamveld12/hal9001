import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import {
  reduceProgress,
  runtimeOption,
  type ProgressByRuntime,
} from "../lib/runtimeStatus";
import { qk } from "./keys";
import { useStreamEvents } from "./streamHooks";

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

/** Keys in the `env` file: secret flag, and the value of plain keys only. */
export function useEnvKeys() {
  return useQuery({
    queryKey: qk("env"),
    queryFn: () => unwrap(api.GET("/api/env")),
    select: (d) => d.items,
  });
}

export function useSetEnvValue() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { key: string; value?: string; secret?: boolean }) =>
      unwrapVoid(
        api.PUT("/api/env/{key}", {
          params: { path: { key: v.key } },
          body: { value: v.value, secret: v.secret },
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

/** Runtime picker options (flow editor): name plus the status hint shown next to it. */
export function useRuntimeOptions() {
  return useQuery({
    queryKey: qk("runtimes"),
    queryFn: fetchRuntimes,
    select: (d) => d.items.map(runtimeOption),
  });
}

/** Re-runs detection (CLI, sign-in, Node, adapter) for every runtime. */
export function useDetectRuntimes() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/api/runtimes/detect", { body: {} })),
    onSuccess: (list) => {
      qc.setQueryData(qk("runtimes"), list);
      return qc.invalidateQueries({ queryKey: qk("runtimes") });
    },
  });
}

/** Starts the install (202). Progress arrives as `runtime.adapter` stream events. */
export function useInstallAdapter() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.POST("/api/runtimes/{name}/adapter", {
          params: { path: { name } },
          body: {},
        }),
      ),
    onSettled: () => qc.invalidateQueries({ queryKey: qk("runtimes") }),
  });
}

export function useRemoveAdapter() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) =>
      unwrap(
        api.DELETE("/api/runtimes/{name}/adapter", {
          params: { path: { name } },
        }),
      ),
    onSettled: () => qc.invalidateQueries({ queryKey: qk("runtimes") }),
  });
}

/** Live install log per runtime from `runtime.adapter` stream events. */
export function useAdapterProgress(): ProgressByRuntime {
  const [progress, setProgress] = useState<ProgressByRuntime>({});
  useStreamEvents(["runtime.adapter"], (e) => {
    if (e.type !== "runtime.adapter" || !e.data) return;
    const progress = e.data;
    setProgress((s) => reduceProgress(s, progress));
  });
  return progress;
}
