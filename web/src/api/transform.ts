import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type TransformPreviewRequest = Schemas["TransformPreviewRequest"];
export type TransformPreview = Schemas["TransformPreview"];
export type TransformCompiled = Schemas["TransformCompiled"];
export type TransformStep = Schemas["TransformStep"];
export type FlowSample = Schemas["FlowSample"];

/** A preview result together with the request it answers. */
export interface PreviewAnswer {
  request: TransformPreviewRequest;
  preview: TransformPreview;
}

/**
 * Run `steps` on `input`, step by step. Pass `null` to stay idle. The caller
 * debounces the request (about 300 ms); a request that is superseded before it
 * finishes is aborted, and the previous answer stays in `data` (flagged by
 * `isPlaceholderData`) so the panel does not flash empty between edits. The
 * answer carries its request so the caller can tell which steps it still fits.
 */
export function usePreviewTransform(request: TransformPreviewRequest | null) {
  return useQuery<PreviewAnswer>({
    queryKey: qk("transform-preview", request),
    enabled: request !== null,
    placeholderData: keepPreviousData,
    gcTime: 60_000,
    staleTime: Infinity,
    retry: false,
    queryFn: async ({ signal }) => {
      const body = request as TransformPreviewRequest;
      const preview = await unwrap(
        api.POST("/api/transform/preview", { body, signal }),
      );
      return { request: body, preview };
    },
  });
}

/** The jq program for `steps`; rejects with a 422 ApiError when they are invalid. */
export function compileTransform(
  steps: TransformStep[],
): Promise<TransformCompiled> {
  return unwrap(api.POST("/api/transform/compile", { body: { steps } }));
}

/** Recent real inputs the node would receive, newest first. */
export function useFlowSamples(flowId: string, nodeId: string) {
  return useQuery({
    queryKey: qk("flow-samples", flowId, nodeId),
    // A node that is not saved yet is unknown to the server.
    retry: false,
    queryFn: async ({ signal }) =>
      (
        await unwrap(
          api.GET("/api/flows/{id}/samples", {
            params: {
              path: { id: flowId },
              query: { node_id: nodeId, limit: 10 },
            },
            signal,
          }),
        )
      ).items,
  });
}
