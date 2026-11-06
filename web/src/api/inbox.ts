import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";
import type { paths } from "./schema";

export type InboxItem = Schemas["InboxItem"];
export type InboxList = Schemas["InboxList"];
export type InboxPatch = Schemas["InboxPatch"];
export type Priority = Schemas["Priority"];
export type InboxSort = "priority" | "time";

/** Filters accepted by GET /api/inbox (without paging). */
export type InboxQuery = Omit<
  NonNullable<paths["/api/inbox"]["get"]["parameters"]["query"]>,
  "cursor" | "limit"
>;

export const PRIORITIES: readonly Priority[] = [
  "low",
  "normal",
  "high",
  "urgent",
];

/** Higher is more important. */
export function priorityRank(p: Priority): number {
  return PRIORITIES.indexOf(p);
}

/**
 * Flatten loaded pages into one list in server order (the server sorts by
 * priority or time; see `sort`). Items that shifted between pages during a
 * refetch are listed once.
 */
export function mergeInboxPages(pages: readonly InboxList[]): InboxItem[] {
  const seen = new Set<string>();
  const out: InboxItem[] = [];
  for (const page of pages) {
    for (const item of page.items) {
      if (seen.has(item.id)) continue;
      seen.add(item.id);
      out.push(item);
    }
  }
  return out;
}

/** Whether an item belongs in the list produced by `query` (mirrors the server filter). */
export function matchesInboxQuery(item: InboxItem, q: InboxQuery): boolean {
  if (q.priority && priorityRank(item.priority) < priorityRank(q.priority)) {
    return false;
  }
  if (q.read !== undefined && item.read !== q.read) return false;
  if (q.starred !== undefined && item.starred !== q.starred) return false;
  const archived = q.archived ?? "false";
  if (archived === "true" && !item.archived) return false;
  if (archived === "false" && item.archived) return false;
  if (q.dry_run !== undefined && item.dry_run !== q.dry_run) return false;
  if (q.flow_id && item.flow_id !== q.flow_id) return false;
  if (q.q) {
    const needle = q.q.toLowerCase();
    if (
      !item.title.toLowerCase().includes(needle) &&
      !item.summary_md.toLowerCase().includes(needle)
    ) {
      return false;
    }
  }
  return true;
}

/**
 * Apply a patch to one item across loaded pages. The item is dropped from the
 * pages when the patched item no longer matches the list's filters.
 */
export function patchInboxPages(
  pages: readonly InboxList[],
  id: string,
  patch: InboxPatch,
  query: InboxQuery,
): InboxList[] {
  return pages.map((page) => ({
    ...page,
    items: page.items.flatMap((item) => {
      if (item.id !== id) return [item];
      const next = { ...item, ...patch };
      return matchesInboxQuery(next, query) ? [next] : [];
    }),
  }));
}

const PAGE_SIZE = 50;

/** Inbox items, one page at a time, keeping the previous list while filters change. */
export function useInboxList(query: InboxQuery, sort: InboxSort) {
  return useInfiniteQuery({
    queryKey: qk("inbox", { ...query, sort }),
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/api/inbox", {
          params: {
            query: { ...query, sort, limit: PAGE_SIZE, cursor: pageParam },
          },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    placeholderData: keepPreviousData,
  });
}

export function useInboxItem(id: string | undefined) {
  return useQuery({
    queryKey: qk("inbox-item", id),
    queryFn: () =>
      unwrap(api.GET("/api/inbox/{id}", { params: { path: { id: id! } } })),
    enabled: id !== undefined,
  });
}

type ListSnapshot = [readonly unknown[], InfiniteData<InboxList> | undefined][];

/** Read / star / archive with an immediate cache update, rolled back on failure. */
export function useUpdateInbox() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: InboxPatch }) =>
      unwrap(
        api.PATCH("/api/inbox/{id}", {
          params: { path: { id } },
          body: patch,
        }),
      ),
    onMutate: async ({ id, patch }) => {
      await Promise.all([
        qc.cancelQueries({ queryKey: ["inbox"] }),
        qc.cancelQueries({ queryKey: qk("inbox-item", id) }),
      ]);
      const lists: ListSnapshot = qc.getQueriesData<InfiniteData<InboxList>>({
        queryKey: ["inbox"],
      });
      const detail = qc.getQueryData<InboxItem>(qk("inbox-item", id));
      for (const [key, data] of lists) {
        if (!data) continue;
        const query = (key[1] ?? {}) as InboxQuery;
        qc.setQueryData<InfiniteData<InboxList>>(key, {
          ...data,
          pages: patchInboxPages(data.pages, id, patch, query),
        });
      }
      if (detail) {
        qc.setQueryData<InboxItem>(qk("inbox-item", id), {
          ...detail,
          ...patch,
        });
      }
      return { lists, detail };
    },
    onError: (_err, { id }, ctx) => {
      if (!ctx) return;
      for (const [key, data] of ctx.lists) qc.setQueryData(key, data);
      qc.setQueryData(qk("inbox-item", id), ctx.detail);
    },
    onSuccess: (item, { id }) => {
      qc.setQueryData<InboxItem>(qk("inbox-item", id), item);
    },
    onSettled: (_data, _err, { id }) => {
      void qc.invalidateQueries({ queryKey: ["inbox"] });
      void qc.invalidateQueries({ queryKey: qk("inbox-item", id) });
      void qc.invalidateQueries({ queryKey: qk("status") });
    },
  });
}

export type InboxBulkRequest = Schemas["InboxBulkRequest"];
export type InboxBulkFilter = Schemas["InboxBulkFilter"];
export type InboxBulkResult = Schemas["InboxBulkResult"];
export type InboxBulkAction = InboxBulkRequest["action"];

/** The server accepts at most this many ids per request. */
export const BULK_MAX_IDS = 1000;

function emptyBulkResult(): InboxBulkResult {
  return {
    matched: 0,
    changed: 0,
    skipped_starred: 0,
    skipped_not_archived: 0,
    changed_ids: [],
  };
}

/** Run a bulk request. Id lists longer than the server limit are sent in several requests and summed. */
export async function postInboxBulk(
  req: InboxBulkRequest,
): Promise<InboxBulkResult> {
  const send = (body: InboxBulkRequest) =>
    unwrap(api.POST("/api/inbox/bulk", { body }));
  const ids = req.ids;
  if (!ids || ids.length <= BULK_MAX_IDS) return send(req);
  const total = emptyBulkResult();
  for (let i = 0; i < ids.length; i += BULK_MAX_IDS) {
    const r = await send({ ...req, ids: ids.slice(i, i + BULK_MAX_IDS) });
    total.matched += r.matched;
    total.changed += r.changed;
    total.skipped_starred += r.skipped_starred;
    total.skipped_not_archived += r.skipped_not_archived;
    total.changed_ids.push(...r.changed_ids);
  }
  return total;
}

/** Bulk archive / unarchive / mark / delete; refreshes the lists and the unread badge. */
export function useBulkInbox() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: postInboxBulk,
    onSuccess: (_r, req) => {
      if (req.dry_run) return;
      void qc.invalidateQueries({ queryKey: ["inbox"] });
      void qc.invalidateQueries({ queryKey: ["inbox-item"] });
      void qc.invalidateQueries({ queryKey: qk("status") });
    },
  });
}
