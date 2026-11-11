import { useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import {
  useConversationList,
  type ConversationDetail,
} from "../../api/conversations";
import { Spinner } from "../../components/ui";
import { cx } from "../../lib/cx";
import { usePageContext } from "../../layout/pageContext";
import { ConversationList } from "./ConversationList";
import {
  ConversationPane,
  NewChatPane,
  NothingSelected,
} from "./ConversationPane";
import { chatPageContext } from "./context";
import {
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
  hasActiveFilters,
  NO_CHAT_FILTERS,
  type ChatFilters,
} from "./filters";
import { FilterBar } from "./FilterBar";
import { mergePages } from "./list";
import { chatPath, NEW_CHAT_ID } from "./path";

/**
 * Every conversation on the left (chats and the agent runs of flow nodes, with
 * filters kept in the URL), the selected one on the right. `/chat/new` is the
 * start view of a new chat.
 */
export function ChatPage() {
  const { id } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => filtersFromParams(params), [params]);
  const setFilters = (next: ChatFilters) =>
    setParams(filtersToParams(next, params), { replace: true });
  const starting = id === NEW_CHAT_ID;
  const selectedId = starting ? null : (id ?? null);
  const search = params.toString() === "" ? "" : `?${params.toString()}`;

  const list = useConversationList(
    useMemo(() => filtersToQuery(filters), [filters]),
  );
  const rows = useMemo(() => mergePages(list.data?.pages), [list.data]);
  const [loaded, setLoaded] = useState<ConversationDetail | null>(null);
  // The row's own data serves until the pane's conversation has loaded.
  const selected =
    loaded?.id === selectedId
      ? loaded
      : (rows.find((c) => c.id === selectedId) ?? null);
  usePageContext(
    chatPageContext({
      filters,
      listed: rows,
      hasMore: list.hasNextPage,
      selected,
      starting,
    }),
  );

  const showPane = starting || selectedId !== null;
  return (
    <div className="flex h-full min-h-0">
      <section
        aria-label="Conversations"
        className={cx(
          "min-h-0 w-full flex-col border-r border-line lg:flex lg:w-[24rem] lg:shrink-0",
          showPane ? "hidden" : "flex",
        )}
      >
        <div className="flex items-center gap-2 border-b border-line px-3 py-2">
          <h1 className="m-0 flex-1 text-sm font-semibold">Chat</h1>
          {list.isFetching && !list.isPending && <Spinner label="Updating" />}
          <Link
            to={{ pathname: chatPath(NEW_CHAT_ID), search }}
            className="inline-flex h-7 items-center gap-1.5 whitespace-nowrap rounded-md border border-line bg-panel px-2.5 text-xs font-medium hover:bg-raised"
          >
            New chat
          </Link>
        </div>
        <FilterBar filters={filters} onChange={setFilters} />
        <div className="min-h-0 flex-1 overflow-auto">
          <ConversationList
            list={list}
            rows={rows}
            selectedId={selectedId}
            search={search}
            filtered={hasActiveFilters(filters)}
            onClear={() => setFilters(NO_CHAT_FILTERS)}
          />
        </div>
      </section>
      <section
        aria-label="Conversation"
        className={cx(
          "min-h-0 min-w-0 flex-1 flex-col lg:flex",
          showPane ? "flex" : "hidden",
        )}
      >
        {starting ? (
          <NewChatPane />
        ) : selectedId !== null ? (
          <ConversationPane
            key={selectedId}
            id={selectedId}
            onLoaded={setLoaded}
          />
        ) : (
          <NothingSelected />
        )}
      </section>
    </div>
  );
}
