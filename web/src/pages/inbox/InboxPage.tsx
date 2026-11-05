import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  mergeInboxPages,
  useInboxItem,
  useInboxList,
  useUpdateInbox,
  type InboxItem,
  type InboxPatch,
} from "../../api/inbox";
import { useStatus } from "../../api/status";
import {
  Button,
  EmptyState,
  ErrorState,
  Kbd,
  LoadingState,
  PageHeader,
} from "../../components/ui";
import { errorMessage } from "../../api/client";
import { cx } from "../../lib/cx";
import {
  activeFilterCount,
  DEFAULT_FILTERS,
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
  type InboxFilters,
} from "./filters";
import { useHotkeys } from "./hotkeys";
import { InboxDetail } from "./InboxDetail";
import { InboxFilterBar } from "./InboxFilterBar";
import { InboxList } from "./InboxList";
import { rowDomId, withPinned } from "./model";
import { moveCursor, neighborAfterRemoval } from "./selection";

interface Position {
  id: string;
  index: number;
  /** Filters in effect when the position was recorded. */
  search: string;
}

const SHORTCUTS: [string, string][] = [
  ["j k", "move"],
  ["Enter", "open"],
  ["e", "archive"],
  ["s", "star"],
  ["u", "read"],
];

export function InboxPage() {
  const { id: openId } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const status = useStatus();

  const filters = useMemo(() => filtersFromParams(params), [params]);
  const query = useMemo(() => filtersToQuery(filters), [filters]);
  const search = filtersToParams(filters).toString();
  const setFilters = useCallback(
    (next: InboxFilters) => setParams(filtersToParams(next), { replace: true }),
    [setParams],
  );

  const list = useInboxList(query, filters.sort);
  const detail = useInboxItem(openId);
  const update = useUpdateInbox();

  const loaded = useMemo(
    () => mergeInboxPages(list.data?.pages ?? []),
    [list.data],
  );

  // Where the open item and the cursor last were in the list, so the open item
  // can stay put and the cursor move on sensibly when a change drops them.
  // Updated during render, which React allows for a component's own state.
  const [openPos, setOpenPos] = useState<Position | null>(null);
  const openAt = openId ? loaded.findIndex((i) => i.id === openId) : -1;
  // While a changed filter is still loading, `loaded` is the previous filter's
  // list (placeholder data); recording a position from it would pin an item the
  // new filter excludes.
  if (
    openId &&
    openAt >= 0 &&
    !list.isPlaceholderData &&
    (openPos?.id !== openId ||
      openPos.index !== openAt ||
      openPos.search !== search)
  ) {
    setOpenPos({ id: openId, index: openAt, search });
  }
  const pinned =
    openPos !== null &&
    openPos.id === openId &&
    openPos.search === search &&
    detail.data?.id === openId
      ? detail.data
      : undefined;
  const items = withPinned(loaded, pinned, openPos?.index ?? 0);
  const ids = items.map((i) => i.id);

  const [cursorState, setCursorState] = useState<{
    openId: string | undefined;
    id: string;
  } | null>(null);
  // The cursor follows the open item until the keyboard moves it.
  const cursorId =
    cursorState && cursorState.openId === openId
      ? cursorState.id
      : (openId ?? null);
  const setCursor = (id: string | null) =>
    setCursorState(id ? { openId, id } : null);

  const [cursorPos, setCursorPos] = useState<Position | null>(null);
  const cursorAt = cursorId ? ids.indexOf(cursorId) : -1;
  if (
    cursorId &&
    cursorAt >= 0 &&
    (cursorPos?.id !== cursorId || cursorPos.index !== cursorAt)
  ) {
    setCursorPos({ id: cursorId, index: cursorAt, search });
  }

  useEffect(() => {
    if (cursorId) {
      document
        .getElementById(rowDomId(cursorId))
        ?.scrollIntoView({ block: "nearest" });
    }
  }, [cursorId]);

  // Opening a note marks it read, once per note, so un-reading it sticks.
  const autoRead = useRef(new Set<string>());
  const { mutate } = update;
  useEffect(() => {
    const it = detail.data;
    if (!it || it.id !== openId || it.read || autoRead.current.has(it.id)) {
      return;
    }
    autoRead.current.add(it.id);
    mutate({ id: it.id, patch: { read: true } });
  }, [detail.data, openId, mutate]);

  const open = (id: string, replace = false) =>
    void navigate({ pathname: `/inbox/${id}`, search }, { replace });
  const closeDetail = () => void navigate({ pathname: "/inbox", search });

  const patch = (item: InboxItem, p: InboxPatch) =>
    update.mutate({ id: item.id, patch: p });

  /** Archiving leaves the list; focus (and the open note) moves to the next one. */
  const toggleArchive = (item: InboxItem) => {
    const next = neighborAfterRemoval(ids, item.id);
    patch(item, { archived: !item.archived });
    if (item.id === cursorId) setCursor(next);
    if (item.id === openId) {
      if (next) open(next, true);
      else closeDetail();
    }
  };

  const shown: InboxItem | undefined =
    detail.data ?? items.find((i) => i.id === openId);
  const cursorItem =
    items.find((i) => i.id === cursorId) ??
    (cursorId === openId ? shown : undefined);

  useHotkeys({
    j: () => setCursor(moveCursor(ids, cursorId, 1, cursorPos?.index ?? 0)),
    k: () => setCursor(moveCursor(ids, cursorId, -1, cursorPos?.index ?? 0)),
    Enter: () => {
      if (cursorId) open(cursorId);
    },
    Escape: () => {
      if (openId) closeDetail();
    },
    e: () => cursorItem && toggleArchive(cursorItem),
    s: () => cursorItem && patch(cursorItem, { starred: !cursorItem.starred }),
    u: () => cursorItem && patch(cursorItem, { read: !cursorItem.read }),
  });

  const filtered = activeFilterCount(filters) > 0;
  const unread = status.data?.unread_inbox;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title="Inbox"
        subtitle={unread === undefined ? undefined : `${unread} unread`}
        actions={
          <div className="hidden items-center gap-3 text-[11px] text-muted lg:flex">
            {SHORTCUTS.map(([key, label]) => (
              <span key={key} className="inline-flex items-center gap-1">
                <Kbd>{key}</Kbd> {label}
              </span>
            ))}
          </div>
        }
      />
      <InboxFilterBar filters={filters} onChange={setFilters} />
      {update.isError && (
        <div
          role="alert"
          className="flex items-center gap-2 border-b border-bad/40 bg-bad/10 px-4 py-1.5 text-xs text-bad"
        >
          Could not update the note: {errorMessage(update.error)}
          <Button
            variant="ghost"
            className="ml-auto"
            onClick={() => update.reset()}
          >
            Dismiss
          </Button>
        </div>
      )}
      <div className="flex min-h-0 flex-1">
        <section
          aria-label="Notes"
          className={cx(
            "min-h-0 w-full shrink-0 flex-col overflow-auto border-r border-line md:flex md:w-[26rem]",
            openId ? "hidden" : "flex",
          )}
        >
          {list.isPending ? (
            <LoadingState label="Loading notes" />
          ) : list.isError && !list.data ? (
            <ErrorState
              error={list.error}
              onRetry={() => void list.refetch()}
            />
          ) : items.length === 0 ? (
            <EmptyState
              title={
                filtered ? "No notes match these filters" : "Inbox is empty"
              }
              hint={
                filtered
                  ? undefined
                  : "Notes written by inbox nodes and agent runs show up here."
              }
              action={
                filtered && (
                  <Button
                    onClick={() =>
                      setFilters({ ...DEFAULT_FILTERS, sort: filters.sort })
                    }
                  >
                    Clear filters
                  </Button>
                )
              }
            />
          ) : (
            <>
              <InboxList
                items={items}
                grouped={filters.sort === "priority"}
                openId={openId}
                cursorId={cursorId}
                search={search}
              />
              {list.hasNextPage && (
                <div className="p-3">
                  <Button
                    disabled={list.isFetchingNextPage}
                    onClick={() => void list.fetchNextPage()}
                  >
                    {list.isFetchingNextPage ? "Loading…" : "Load more"}
                  </Button>
                </div>
              )}
            </>
          )}
        </section>
        <section
          aria-label="Note"
          className={cx(
            "min-h-0 min-w-0 flex-1 overflow-auto md:block",
            openId ? "block" : "hidden",
          )}
        >
          {!openId ? (
            <EmptyState
              title="Select a note"
              hint="Pick one from the list, or move with j and k and press Enter."
            />
          ) : shown ? (
            <InboxDetail
              item={shown}
              onPatch={(p) => patch(shown, p)}
              onArchive={() => toggleArchive(shown)}
              backTo={{ pathname: "/inbox", search }}
            />
          ) : detail.isError ? (
            <ErrorState
              error={detail.error}
              onRetry={() => void detail.refetch()}
            />
          ) : (
            <LoadingState label="Loading note" />
          )}
        </section>
      </div>
    </div>
  );
}
