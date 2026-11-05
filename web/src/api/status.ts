import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type Status = Schemas["Status"];

/** Header counters and kill-switch state. Pushed by the stream; polled as a fallback. */
export function useStatus() {
  return useQuery({
    queryKey: qk("status"),
    queryFn: () => unwrap(api.GET("/api/status")),
    refetchInterval: 30_000,
  });
}

export function usePauseKillSwitch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (req: Schemas["PauseRequest"]) =>
      unwrap(api.POST("/api/killswitch/pause", { body: req })),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("status") }),
  });
}

export function useResumeKillSwitch() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/api/killswitch/resume")),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk("status") }),
  });
}

export function useWhoAmI() {
  return useQuery({
    queryKey: qk("whoami"),
    queryFn: () => unwrap(api.GET("/api/auth/whoami")),
    retry: false,
  });
}
