import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type UpdateStatus = Schemas["UpdateStatus"];

/** App self-update state. Pushed by the `update.status` stream event. */
export function useUpdateStatus() {
  return useQuery({
    queryKey: qk("update"),
    queryFn: () => unwrap(api.GET("/api/update")),
  });
}

function useStatusMutation<V>(fn: (v: V) => Promise<UpdateStatus>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (status) => qc.setQueryData(qk("update"), status),
  });
}

export function useCheckUpdate() {
  return useStatusMutation(() => unwrap(api.POST("/api/update/check")));
}

/** `force` is offered by the banner only, behind a confirmation. Rejects with a 409 ApiError (`release_url` on the problem) when no installer is connected. */
export function useInstallUpdate() {
  return useStatusMutation((force: boolean) =>
    unwrap(api.POST("/api/update/install", { body: { force } })),
  );
}
