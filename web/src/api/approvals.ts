import { useMemo } from "react";
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
export function useApprovalsList(query: ApprovalsQuery, enabled = true) {
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
    enabled,
  });
}

/** Filter of the pending approvals of one conversation (a chat session id or flow agent run id). */
export function conversationApprovalsQuery(
  conversationId: string,
): ApprovalsQuery {
  return { conversation_id: conversationId, status: ["pending"] };
}

/**
 * The pending approvals of one conversation, oldest first. Stream events
 * refetch the `approvals` family and a decision anywhere updates every cached
 * list, so this stays in step with the Inbox and the Approvals page.
 */
export function useConversationApprovals(conversationId: string | null) {
  const query = useApprovalsList(
    conversationApprovalsQuery(conversationId ?? ""),
    conversationId !== null,
  );
  const items = useMemo(() => {
    const seen = new Set<string>();
    const out: Approval[] = [];
    for (const page of query.data?.pages ?? []) {
      for (const a of page.items) {
        if (seen.has(a.id)) continue;
        seen.add(a.id);
        out.push(a);
      }
    }
    return out.sort(
      (a, b) =>
        Date.parse(a.created_at) - Date.parse(b.created_at) ||
        (a.id < b.id ? -1 : 1),
    );
  }, [query.data]);
  return { ...query, items };
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
  /** Additional steering text; sent with a denial only. */
  comment?: string;
  /** `request` approvals, approve only: new values for editable paths. */
  edits?: Record<string, unknown>;
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
 * Approve or deny. The decided approval replaces the cached copy at
 * once (leaving the pending queue); everything is refetched afterwards. A 409
 * `invalid-state` means someone else decided first: the lists are refreshed
 * and the caller shows `isAlreadyDecided(error)`.
 */
export function useDecideApproval() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, action, comment, cancelRun, edits }: Decision) => {
      const text = comment?.trim() ? { comment: comment.trim() } : {};
      const approveBody =
        edits && Object.keys(edits).length > 0 ? { edits } : {};
      const params = { path: { id } };
      return action === "approve"
        ? unwrap(
            api.POST("/api/approvals/{id}/approve", {
              params,
              body: approveBody,
            }),
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
