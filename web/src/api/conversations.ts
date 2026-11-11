// Data hooks for the Chat page: the conversation list (chats and flow agent
// runs in one list), one conversation, a transcript and forking a run into a
// chat. Keys use the roots the stream invalidates (invalidate.ts).

import {
  skipToken,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";
import { qk } from "./keys";

export type Conversation = Schemas["Conversation"];
export type ConversationDetail = Schemas["ConversationDetail"];
export type ConversationMessage = Schemas["ConversationMessage"];
export type ConversationMessageList = Schemas["ConversationMessageList"];
export type ConversationSource = Schemas["ConversationSource"];
export type ConversationStatus = Schemas["ConversationStatus"];
export type ConversationForkRef = Schemas["ConversationForkRef"];

/** Filters of `GET /api/conversations`; an absent key does not filter. */
export interface ConversationQuery {
  source?: ConversationSource;
  flow?: string;
  runtime?: string;
  status?: ConversationStatus;
  /** RFC 3339 time, inclusive. */
  since?: string;
  /** RFC 3339 time, exclusive. */
  until?: string;
  q?: string;
}

const PAGE = 30;

/** What `useConversationList` returns: the loaded pages and the paging state. */
export type ConversationListResult = UseInfiniteQueryResult<
  InfiniteData<Schemas["ConversationList"]>
>;

/**
 * Every conversation, newest activity first, a page at a time. The stream
 * (`agent_run.*`, `approval.*`, `assist.updated`) refetches the loaded pages.
 */
export function useConversationList(query: ConversationQuery) {
  return useInfiniteQuery({
    queryKey: qk("conversations", "list", query),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      unwrap(
        api.GET("/api/conversations", {
          params: { query: { ...query, cursor: pageParam, limit: PAGE } },
          signal,
        }),
      ),
    getNextPageParam: (p) => p.next_cursor ?? undefined,
  });
}

/** One conversation with the chats forked from it. `null` fetches nothing. */
export function useConversation(id: string | null) {
  return useQuery({
    queryKey: qk("conversation", id ?? ""),
    queryFn:
      id === null
        ? skipToken
        : () =>
            unwrap(
              api.GET("/api/conversations/{id}", { params: { path: { id } } }),
            ),
  });
}

/**
 * The transcript of a conversation (the same shape for both sources). The
 * Chat page refetches it while a flow run is watched (useConversationLive).
 * `null` fetches nothing.
 */
export function useConversationMessages(id: string | null) {
  return useQuery({
    queryKey: qk("conversation-messages", id ?? ""),
    queryFn:
      id === null
        ? skipToken
        : () =>
            unwrap(
              api.GET("/api/conversations/{id}/messages", {
                params: { path: { id } },
              }),
            ),
  });
}

/** Continue an agent run in a chat: the result is the new chat conversation (`id` = its assist session). */
export function useForkConversation() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (agentRunId: string) =>
      unwrap(
        api.POST("/api/conversations/{id}/fork", {
          params: { path: { id: agentRunId } },
        }),
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk("conversations") });
      void qc.invalidateQueries({ queryKey: qk("conversation") });
      void qc.invalidateQueries({ queryKey: qk("assist-sessions") });
    },
  });
}
