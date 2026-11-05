import {
  keepPreviousData,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { api, unwrap, unwrapVoid, type Schemas } from "./client";
import { qk } from "./keys";

export type PromptSummary = Schemas["PromptSummary"];
export type PromptDetail = Schemas["PromptDetail"];
export type PromptRender = Schemas["PromptRender"];
export type PromptRenderRequest = Schemas["PromptRenderRequest"];

const promptsQuery = {
  queryKey: qk("prompts"),
  queryFn: () => unwrap(api.GET("/api/prompts")),
};

/** All prompt files. Refetched on config changes. */
export function usePrompts() {
  return useQuery(promptsQuery);
}

/** Prompt names for the agent node's dropdown. Shares the cache of `usePrompts`. */
export function usePromptNames() {
  return useQuery({
    ...promptsQuery,
    select: (list) => list.items.map((p) => p.name),
  });
}

export function usePrompt(name: string) {
  return useQuery({
    queryKey: qk("prompt", name),
    queryFn: () =>
      unwrap(api.GET("/api/prompts/{name}", { params: { path: { name } } })),
  });
}

/** Prompts and the flows that select them (`unknown_prompt` diagnostics). */
function invalidatePromptFiles(qc: QueryClient) {
  const roots = ["prompts", "prompt", "flows", "flow"] as const;
  return Promise.all(
    roots.map((r) => qc.invalidateQueries({ queryKey: qk(r) })),
  );
}

export function useCreatePrompt() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (markdown: string) =>
      unwrap(api.POST("/api/prompts", { body: { markdown } })),
    onSuccess: (detail) => {
      qc.setQueryData(qk("prompt", detail.name), detail);
      void invalidatePromptFiles(qc);
    },
  });
}

export function useUpdatePrompt() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { name: string; expectedHash: string; markdown: string }) =>
      unwrap(
        api.PUT("/api/prompts/{name}", {
          params: { path: { name: v.name } },
          body: { expected_hash: v.expectedHash, markdown: v.markdown },
        }),
      ),
    onSuccess: (detail) => {
      qc.setQueryData(qk("prompt", detail.name), detail);
      void invalidatePromptFiles(qc);
    },
  });
}

export function useDeletePrompt() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { name: string; expectedHash: string }) =>
      unwrapVoid(
        api.DELETE("/api/prompts/{name}", {
          params: {
            path: { name: v.name },
            query: { expected_hash: v.expectedHash },
          },
        }),
      ),
    onSuccess: (_void, v) => {
      qc.removeQueries({ queryKey: qk("prompt", v.name) });
      void invalidatePromptFiles(qc);
    },
  });
}

/**
 * Renders unsaved prompt text against a sample. Template errors come back as
 * diagnostics in a 200; a missing event or a malformed request is an error.
 * The previous result stays visible while a new one loads.
 */
export function usePromptRender(request: PromptRenderRequest | null) {
  return useQuery({
    queryKey: qk("prompt", "render", request),
    queryFn: () =>
      unwrap(
        api.POST("/api/prompts/render", {
          body: request as PromptRenderRequest,
        }),
      ),
    enabled: request !== null,
    placeholderData: keepPreviousData,
    retry: false,
    staleTime: Infinity,
  });
}
