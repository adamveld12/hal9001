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

function createdMs(item: InboxItem): number {
  const t = Date.parse(item.created_at);
  return Number.isNaN(t) ? 0 : t;
}

/** Newest first; ULIDs break ties between equal timestamps. */
function byTime(a: InboxItem, b: InboxItem): number {
  return (
    createdMs(b) - createdMs(a) || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0)
  );
}

/** `priority`: most important first, then newest. `time`: newest first. */
export function compareInbox(sort: InboxSort) {
  return (a: InboxItem, b: InboxItem): number =>
    sort === "priority"
      ? priorityRank(b.priority) - priorityRank(a.priority) || byTime(a, b)
      : byTime(a, b);
}

/**
 * Flatten loaded pages into one list: duplicates (items that shifted between
 * pages during a refetch) are dropped and the result is ordered client-side,
 * because the cursor follows id order, not priority.
 */
export function mergeInboxPages(
  pages: readonly InboxList[],
  sort: InboxSort,
): InboxItem[] {
  const seen = new Set<string>();
  const out: InboxItem[] = [];
  for (const page of pages) {
    for (const item of page.items) {
      if (seen.has(item.id)) continue;
      seen.add(item.id);
      out.push(item);
    }
  }
  return out.sort(compareInbox(sort));
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
