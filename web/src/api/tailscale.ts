import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type TailscaleStatus = Schemas["TailscaleStatus"];
export type TailscaleWarning = Schemas["TailscaleWarning"];
export type TailscaleServeRequest = Schemas["TailscaleServeRequest"];

/** `GET /api/tailscale`: what hal found about Tailscale on this Mac (cached a few seconds server-side). */
export function useTailscaleStatus() {
  return useQuery({
    queryKey: qk("tailscale"),
    queryFn: () => unwrap(api.GET("/api/tailscale")),
  });
}

/** Every tailscale call answers with the status after the change. */
function useStatusMutation<V>(call: (vars: V) => Promise<TailscaleStatus>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: call,
    onSuccess: (status) => {
      qc.setQueryData(qk("tailscale"), status);
      return qc.invalidateQueries({ queryKey: qk("tailscale") });
    },
  });
}

/** `POST /api/tailscale/refresh`: forgets the cached detection and checks again (read-only). */
export function useRefreshTailscale() {
  return useStatusMutation<void>(() =>
    unwrap(api.POST("/api/tailscale/refresh")),
  );
}

/**
 * `POST /api/tailscale/serve`: runs `tailscale serve`, adds the machine name to `allowed_hosts`.
 * Throws ApiError `already-exists` (409, port serves something else), `invalid-state` (409, not
 * ready) or `unavailable` (503, the command failed).
 */
export function useEnableTailscaleServe() {
  return useStatusMutation<TailscaleServeRequest>((body) =>
    unwrap(api.POST("/api/tailscale/serve", { body })),
  );
}

/** `DELETE /api/tailscale/serve`: removes hal's mapping and the `allowed_hosts` entry hal added. */
export function useDisableTailscaleServe() {
  return useStatusMutation<void>(() =>
    unwrap(api.DELETE("/api/tailscale/serve")),
  );
}
