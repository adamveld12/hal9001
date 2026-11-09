import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "./client";
import { qk } from "./keys";

/** What a rename of one file (sensor, flow, tool, prompt) asks for. */
export interface RenameArgs {
  id: string;
  newId: string;
  expectedHash: string;
}

// A rename moves the file, its env file and the references in flows, and
// carries state keyed by the id, so every cached list and item may change.
function useRenameMutation<D>(
  call: (v: RenameArgs) => Promise<D>,
  oldKey: (id: string) => readonly unknown[],
  newKey: (newId: string) => readonly unknown[],
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: call,
    onSuccess: (detail, v) => {
      qc.removeQueries({ queryKey: oldKey(v.id) });
      qc.setQueryData(newKey(v.newId), detail);
      void qc.invalidateQueries();
    },
  });
}

export function useRenameSensor() {
  return useRenameMutation(
    (v) =>
      unwrap(
        api.POST("/api/sensors/{id}/rename", {
          params: { path: { id: v.id } },
          body: { new_id: v.newId, expected_hash: v.expectedHash },
        }),
      ),
    (id) => qk("sensor", id),
    (id) => qk("sensor", id),
  );
}

export function useRenameFlow() {
  return useRenameMutation(
    (v) =>
      unwrap(
        api.POST("/api/flows/{id}/rename", {
          params: { path: { id: v.id } },
          body: { new_id: v.newId, expected_hash: v.expectedHash },
        }),
      ),
    (id) => qk("flow", id),
    (id) => qk("flow", id),
  );
}

export function useRenameTool() {
  return useRenameMutation(
    (v) =>
      unwrap(
        api.POST("/api/tools/{id}/rename", {
          params: { path: { id: v.id } },
          body: { new_id: v.newId, expected_hash: v.expectedHash },
        }),
      ),
    (id) => qk("tool", id),
    (id) => qk("tool", id),
  );
}

export function useRenamePrompt() {
  return useRenameMutation(
    (v) =>
      unwrap(
        api.POST("/api/prompts/{name}/rename", {
          params: { path: { name: v.id } },
          body: { new_id: v.newId, expected_hash: v.expectedHash },
        }),
      ),
    (name) => qk("prompt", name),
    (name) => qk("prompt", name),
  );
}
