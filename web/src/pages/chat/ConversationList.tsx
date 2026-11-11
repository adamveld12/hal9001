import { Link } from "react-router";
import type {
  Conversation,
  ConversationListResult,
} from "../../api/conversations";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
} from "../../components/ui";
import { ConversationRow } from "./ConversationRow";
import { chatPath, NEW_CHAT_ID } from "./path";

/** The list's body: rows, paging and the states with no rows. */
export function ConversationList({
  list,
  rows,
  selectedId,
  search,
  filtered,
  onClear,
}: {
  list: ConversationListResult;
  rows: readonly Conversation[];
  selectedId: string | null;
  /** The URL query, so a picked row keeps the filters. */
  search: string;
  /** Any filter is set. */
  filtered: boolean;
  onClear: () => void;
}) {
  if (list.isPending) return <LoadingState label="Loading conversations" />;
  if (list.isError && !list.data) {
    return (
      <ErrorState error={list.error} onRetry={() => void list.refetch()} />
    );
  }
  if (rows.length === 0) {
    return filtered ? (
      <EmptyState
        title="No matching conversations"
        hint="Nothing matches these filters."
        action={<Button onClick={onClear}>Clear filters</Button>}
      />
    ) : (
      <EmptyState
        title="No conversations yet"
        hint="Chats and the agent runs of flow nodes appear here."
        action={
          <Link
            to={{ pathname: chatPath(NEW_CHAT_ID), search }}
            className="inline-flex h-7 items-center rounded-md border border-line bg-panel px-2.5 text-xs font-medium hover:bg-raised"
          >
            Start a chat
          </Link>
        }
      />
    );
  }
  return (
    <>
      <ul aria-label="Conversations" className="m-0 list-none p-0">
        {rows.map((c) => (
          <ConversationRow
            key={c.id}
            c={c}
            selected={c.id === selectedId}
            search={search}
          />
        ))}
      </ul>
      <div className="flex justify-center p-3">
        {list.hasNextPage ? (
          <Button
            disabled={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
          >
            {list.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        ) : (
          <span className="text-xs text-muted">End of conversations</span>
        )}
      </div>
      {list.isFetchNextPageError && <ErrorState error={list.error} />}
    </>
  );
}
