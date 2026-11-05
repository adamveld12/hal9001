import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import { api, ApiError, unwrap, type Schemas } from "./client";
import { qk } from "./keys";
import type { paths } from "./schema";

export type Approval = Schemas["Approval"];
export type ApprovalList = Schemas["ApprovalList"];
export type ApprovalStatus = Schemas["ApprovalStatus"];
export type ApprovalType = Schemas["ApprovalType"];

/** Filters accepted by GET /api/approvals (without paging). */
export type ApprovalsQuery = Omit<
  NonNullable<paths["/api/approvals"]["get"]["parameters"]["query"]>,
  "cursor" | "limit"
>;

const PAGE_SIZE = 50;

/** Approvals, one page at a time, keeping the previous list while filters change. */
export function useApprovalsList(query: ApprovalsQuery) {
  return useInfiniteQuery({
    queryKey: qk("approvals", query),
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/approvals", {
          params: { query: { ...query, limit: PAGE_SIZE, cursor: pageParam } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
  });
}

export function useApproval(id: string | undefined) {
  return useQuery({
    queryKey: qk("approval", id),
    queryFn: () =>
      unwrap(api.GET("/api/approvals/{id}", { params: { path: { id: id! } } })),
    enabled: id !== undefined,
  });
}

export interface Decision {
  id: string;
  action: "approve" | "deny";
  comment?: string;
  /** `tool_call` denials only: cancel the run instead of letting the agent continue. */
  cancelRun?: boolean;
}

/** True when the server refused because the approval was already decided or expired. */
export function isAlreadyDecided(error: unknown): boolean {
  return error instanceof ApiError && error.kind === "invalid-state";
}

/** Replace one approval in every loaded page of a list; the decided one leaves the pending queue. */
export function replaceApprovalInPages(
  pages: readonly ApprovalList[],
  decided: Approval,
  query: ApprovalsQuery,
): ApprovalList[] {
  return pages.map((page) => ({
    ...page,
    items: page.items.flatMap((a) => {
      if (a.id !== decided.id) return [a];
      if (
        query.status !== undefined &&
        !query.status.includes(decided.status)
      ) {
        return [];
      }
      return [decided];
    }),
  }));
}

/**
 * Approve / allow or deny. The decided approval replaces the cached copy at
 * once (leaving the pending queue); everything is refetched afterwards. A 409
 * `invalid-state` means someone else decided first: the lists are refreshed
 * and the caller shows `isAlreadyDecided(error)`.
 */
export function useDecideApproval() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, action, comment, cancelRun }: Decision) => {
      const text = comment?.trim() ? { comment: comment.trim() } : {};
      const params = { path: { id } };
      return action === "approve"
        ? unwrap(
            api.POST("/api/approvals/{id}/approve", { params, body: text }),
          )
        : unwrap(
            api.POST("/api/approvals/{id}/deny", {
              params,
              body: cancelRun ? { ...text, cancel_run: true } : text,
            }),
          );
    },
    onSuccess: (decided) => {
      qc.setQueryData<Approval>(qk("approval", decided.id), decided);
      for (const [key, data] of qc.getQueriesData<InfiniteData<ApprovalList>>({
        queryKey: ["approvals"],
      })) {
        if (!data) continue;
        const query = (key[1] ?? {}) as ApprovalsQuery;
        qc.setQueryData<InfiniteData<ApprovalList>>(key, {
          ...data,
          pages: replaceApprovalInPages(data.pages, decided, query),
        });
      }
    },
    onSettled: (_data, _err, { id }) => {
      void qc.invalidateQueries({ queryKey: ["approvals"] });
      void qc.invalidateQueries({ queryKey: qk("approval", id) });
      void qc.invalidateQueries({ queryKey: qk("status") });
    },
  });
}
