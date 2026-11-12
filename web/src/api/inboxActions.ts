import {
  useMutation,
  useQueryClient,
  type InfiniteData,
} from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import type { InboxItem, InboxList } from "./inbox";
import { qk } from "./keys";

export type InboxQuestion = Schemas["InboxQuestion"];
export type InboxOption = Schemas["InboxOption"];
export type InboxAction = Schemas["InboxAction"];
export type InboxInteraction = Schemas["InboxInteraction"];
export type InboxAnswerRequest = Schemas["InboxAnswerRequest"];

/** Replace the item with the same id in every loaded page. */
export function replaceInboxItem(
  pages: readonly InboxList[],
  item: InboxItem,
): InboxList[] {
  return pages.map((p) => ({
    ...p,
    items: p.items.map((it) => (it.id === item.id ? item : it)),
  }));
}

/** Put the returned item into the cached item and lists, then refetch. */
function useStoreItem() {
  const qc = useQueryClient();
  return (item: InboxItem) => {
    qc.setQueryData<InboxItem>(qk("inbox-item", item.id), item);
    for (const [key, data] of qc.getQueriesData<InfiniteData<InboxList>>({
      queryKey: ["inbox"],
    })) {
      if (!data) continue;
      // The list keeps the item until the invalidation refetch applies the filters.
      qc.setQueryData<InfiniteData<InboxList>>(key, {
        ...data,
        pages: replaceInboxItem(data.pages, item),
      });
    }
    void qc.invalidateQueries({ queryKey: ["inbox"] });
    void qc.invalidateQueries({ queryKey: qk("inbox-item", item.id) });
  };
}

/** Send answers to the questions of a note. */
export function useAnswerInboxItem() {
  const store = useStoreItem();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: InboxAnswerRequest }) =>
      unwrap(
        api.POST("/api/inbox/{id}/answers", {
          params: { path: { id } },
          body,
        }),
      ),
    onSuccess: store,
  });
}

/** Run the action at `index` of a note. */
export function useRunInboxAction() {
  const store = useStoreItem();
  return useMutation({
    mutationFn: ({ id, index }: { id: string; index: number }) =>
      unwrap(
        api.POST("/api/inbox/{id}/actions/{index}", {
          params: { path: { id, index } },
        }),
      ),
    onSuccess: store,
  });
}

/** Send free text to the agent of a note. */
export function useReplyInboxItem() {
  const store = useStoreItem();
  return useMutation({
    mutationFn: ({ id, text }: { id: string; text: string }) =>
      unwrap(
        api.POST("/api/inbox/{id}/reply", {
          params: { path: { id } },
          body: { text },
        }),
      ),
    onSuccess: store,
  });
}
