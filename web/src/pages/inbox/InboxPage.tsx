import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useLocation, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  isAlreadyDecided,
  useApproval,
  useApprovalsList,
  useDecideApproval,
  type Approval,
} from "../../api/approvals";
import { qk } from "../../api/keys";
import { ApprovalDetail } from "../approvals/ApprovalDetail";
import { decisionNotice } from "../approvals/queue";
import {
  mergeInboxPages,
  useBulkInbox,
  useInboxItem,
  useInboxList,
  useUpdateInbox,
  type InboxBulkAction,
  type InboxBulkFilter,
  type InboxBulkResult,
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
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { cx } from "../../lib/cx";
import { BulkBar } from "./BulkBar";
import {
  CLEAR_PRESETS,
  confirmText,
  pageSelection,
  presetFilter,
  pruneSelection,
  toggleAllOnPage,
  toggleSelected,
  type ClearPreset,
  type ClearPresetId,
} from "./bulk";
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
import {
  mergeInboxRows,
  noteKey,
  selectionFromKey,
  selectionFromPath,
  selectionKey,
  selectionPath,
} from "./merge";
import { rowDomId, withPinned } from "./model";
import { moveCursor, neighborAfterRemoval } from "./selection";

interface Position {
  id: string;
  index: number;
  /** Filters in effect when the position was recorded. */
  search: string;
}

const TOAST_MS = 10_000;

interface Toast {
  message: string;
  /** Items to unarchive when the user presses Undo. */
  undoIds?: string[];
}

function bulkMessage(action: InboxBulkAction, r: InboxBulkResult): string {
  const n = `${r.changed} ${r.changed === 1 ? "item" : "items"}`;
  const verb: Record<InboxBulkAction, string> = {
    archive: "Archived",
    unarchive: "Restored",
    mark_read: "Marked read:",
    mark_unread: "Marked unread:",
    delete: "Deleted",
  };
  let text = `${verb[action]} ${n}.`;
  if (r.skipped_starred > 0) text += ` ${r.skipped_starred} starred kept.`;
  if (r.skipped_not_archived > 0) {
    text += ` ${r.skipped_not_archived} not archived, skipped.`;
  }
  return text;
}

const SHORTCUTS: [string, string][] = [
  ["j k", "move"],
  ["Enter", "open"],
  ["e", "archive"],
  ["s", "star"],
  ["u", "read"],
];

export function InboxPage() {
  const location = useLocation();
  const selection = useMemo(
    () => selectionFromPath(location.pathname),
    [location.pathname],
  );
  const openId = selection?.kind === "note" ? selection.id : undefined;
  const openApprovalId =
    selection?.kind === "approval" ? selection.id : undefined;
  const openKey = selection ? selectionKey(selection) : undefined;
  const qc = useQueryClient();
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
  const approvalList = useApprovalsList({ status: ["pending"] });
  const approvalDetail = useApproval(openApprovalId);
  const decide = useDecideApproval();
  const { hasNextPage: moreApprovals, fetchNextPage: fetchMoreApprovals } =
    approvalList;
  useEffect(() => {
    if (moreApprovals) void fetchMoreApprovals();
  }, [moreApprovals, fetchMoreApprovals]);
  const pendingApprovals = useMemo(
    () => approvalList.data?.pages.flatMap((p) => p.items) ?? [],
    [approvalList.data],
  );

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
      : // Deep link to a note the list does not hold (archived, filtered out).
        openId && openPos?.id !== openId && detail.data?.id === openId
        ? detail.data
        : undefined;
  const items = withPinned(
    loaded,
    pinned,
    openPos && openPos.id === openId ? openPos.index : 0,
  );
  const noteIds = items.map((i) => i.id);
  const rows = useMemo(
    () => mergeInboxRows(items, pendingApprovals, filters),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [loaded, pinned, pendingApprovals, filters],
  );
  const ids = rows.map((r) => r.key);

  const [cursorState, setCursorState] = useState<{
    openKey: string | undefined;
    id: string;
  } | null>(null);
  // The cursor (a row key) follows the open item until the keyboard moves it.
  const cursorId =
    cursorState && cursorState.openKey === openKey
      ? cursorState.id
      : (openKey ?? null);
  const setCursor = (id: string | null) =>
    setCursorState(id ? { openKey, id } : null);

  const [cursorPos, setCursorPos] = useState<Position | null>(null);
  const cursorAt = cursorId ? ids.indexOf(cursorId) : -1;
  if (
    cursorId &&
    cursorAt >= 0 &&
    (cursorPos?.id !== cursorId || cursorPos.index !== cursorAt)
  ) {
    setCursorPos({ id: cursorId, index: cursorAt, search });
  }

  // Also runs when the row first appears, so a deep link scrolls once the list loads.
  const cursorListed = cursorAt >= 0;
  useEffect(() => {
    if (cursorId && cursorListed) {
      document
        .getElementById(rowDomId(cursorId))
        ?.scrollIntoView({ block: "nearest" });
    }
  }, [cursorId, cursorListed]);

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

  const open = (key: string, replace = false) => {
    const sel = selectionFromKey(key);
    if (sel) {
      void navigate({ pathname: selectionPath(sel), search }, { replace });
    }
  };
  const closeDetail = () => void navigate({ pathname: "/inbox", search });

  const patch = (item: InboxItem, p: InboxPatch) =>
    update.mutate({ id: item.id, patch: p });

  /** Archiving leaves the list; focus (and the open note) moves to the next one. */
  const toggleArchive = (item: InboxItem) => {
    const key = noteKey(item.id);
    const next = neighborAfterRemoval(ids, key);
    patch(item, { archived: !item.archived });
    if (key === cursorId) setCursor(next);
    if (key === openKey) {
      if (next) open(next, true);
      else closeDetail();
    }
  };

  const shown: InboxItem | undefined =
    detail.data ?? items.find((i) => i.id === openId);
  const cursorItem =
    items.find((i) => noteKey(i.id) === cursorId) ??
    (cursorId === openKey && openId ? shown : undefined);

  // Approvals: the comment draft, the outcome notice and the decision itself.
  const [draft, setDraft] = useState({ id: "", text: "" });
  const comment = draft.id === openApprovalId ? draft.text : "";
  const [notice, setNotice] = useState<string | null>(null);
  const shownApproval: Approval | undefined =
    approvalDetail.data ??
    pendingApprovals.find((a) => a.id === openApprovalId);
  const canDecide =
    shownApproval?.status === "pending" && shownApproval.id === openApprovalId;
  const decideApproval = (
    a: Approval,
    action: "approve" | "deny",
    cancelRun?: boolean,
  ) => {
    if (decide.isPending) return;
    const pendingKeys = rows
      .filter((r) => r.kind === "approval")
      .map((r) => r.key);
    const next = neighborAfterRemoval(
      pendingKeys,
      selectionKey({ kind: "approval", id: a.id }),
    );
    decide.mutate(
      { id: a.id, action, comment, cancelRun },
      {
        onSuccess: (done) => {
          setNotice(decisionNotice(done));
          setDraft({ id: "", text: "" });
          // Keep working through the queue; the last one stays open to show its outcome.
          if (next) open(next, true);
        },
        onError: (err) => {
          if (isAlreadyDecided(err)) {
            setNotice(
              "That approval was already decided or has expired. The list was refreshed.",
            );
          }
        },
      },
    );
  };
  const onStale = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["approvals"] });
    void qc.invalidateQueries({ queryKey: qk("approval", openApprovalId) });
  }, [qc, openApprovalId]);

  useHotkeys({
    j: () => setCursor(moveCursor(ids, cursorId, 1, cursorPos?.index ?? 0)),
    k: () => setCursor(moveCursor(ids, cursorId, -1, cursorPos?.index ?? 0)),
    Enter: () => {
      if (cursorId) open(cursorId);
    },
    Escape: () => {
      if (openKey) closeDetail();
    },
    a: () => canDecide && decideApproval(shownApproval, "approve"),
    d: () => canDecide && decideApproval(shownApproval, "deny"),
    c: () =>
      canDecide &&
      shownApproval.type === "tool_call" &&
      decideApproval(shownApproval, "deny", true),
    e: () => cursorItem && toggleArchive(cursorItem),
    s: () => cursorItem && patch(cursorItem, { starred: !cursorItem.starred }),
    u: () => cursorItem && patch(cursorItem, { read: !cursorItem.read }),
  });

  // Bulk selection and the "Clear…" presets.
  const bulk = useBulkInbox();
  const counter = useBulkInbox();
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const sel = pruneSelection(selected, noteIds);
  const [clearing, setClearing] = useState<{
    preset: ClearPreset;
    filter: InboxBulkFilter;
  } | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [toast, setToast] = useState<Toast | null>(null);
  const [bulkError, setBulkError] = useState<string | null>(null);
  useEffect(() => {
    if (!toast) return;
    const t = setTimeout(() => setToast(null), TOAST_MS);
    return () => clearTimeout(t);
  }, [toast]);

  const runBulk = (
    action: InboxBulkAction,
    target: { ids: string[] } | { filter: InboxBulkFilter },
    done?: () => void,
  ) => {
    setBulkError(null);
    bulk.mutate(
      { action, ...target },
      {
        onSuccess: (r) => {
          setSelected(new Set());
          done?.();
          setToast({
            message: bulkMessage(action, r),
            undoIds:
              action === "archive" && r.changed > 0 ? r.changed_ids : undefined,
          });
        },
        onError: (e) => {
          done?.();
          setBulkError(errorMessage(e));
        },
      },
    );
  };
  const openPreset = (id: ClearPresetId) => {
    const preset = CLEAR_PRESETS.find((p) => p.id === id)!;
    const filter = presetFilter(id, filters);
    setClearing({ preset, filter });
    counter.reset();
    counter.mutate({ action: "archive", filter, dry_run: true });
  };
  const closePreset = () => {
    setClearing(null);
    counter.reset();
  };
  const undo = (ids: string[]) => {
    setToast(null);
    bulk.mutate(
      { action: "unarchive", ids },
      {
        onSuccess: (r) => setToast({ message: bulkMessage("unarchive", r) }),
        onError: (e) => setBulkError(errorMessage(e)),
      },
    );
  };

  const filtered = activeFilterCount(filters) > 0;
  const unread = status.data?.unread_inbox;
  const pending = status.data?.pending_approvals;
  const subtitle =
    unread === undefined
      ? undefined
      : `${unread} unread${pending ? `, ${pending} need action` : ""}`;
  const showApprovalError = decide.isError && !isAlreadyDecided(decide.error);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title="Inbox"
        subtitle={subtitle}
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
      {bulkError && (
        <div
          role="alert"
          className="flex items-center gap-2 border-b border-bad/40 bg-bad/10 px-4 py-1.5 text-xs text-bad"
        >
          Bulk action failed: {bulkError}
          <Button
            variant="ghost"
            className="ml-auto"
            onClick={() => setBulkError(null)}
          >
            Dismiss
          </Button>
        </div>
      )}
      {notice && (
        <div
          role="status"
          className="flex items-center gap-2 border-b border-ok/40 bg-ok/10 px-4 py-1.5 text-xs text-ok"
        >
          {notice}
          <Button
            variant="ghost"
            className="ml-auto"
            onClick={() => setNotice(null)}
          >
            Dismiss
          </Button>
        </div>
      )}
      {showApprovalError && (
        <div
          role="alert"
          className="flex items-center gap-2 border-b border-bad/40 bg-bad/10 px-4 py-1.5 text-xs text-bad"
        >
          Could not record the decision: {errorMessage(decide.error)}
          <Button
            variant="ghost"
            className="ml-auto"
            onClick={() => decide.reset()}
          >
            Dismiss
          </Button>
        </div>
      )}
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
          aria-label="Inbox items"
          className={cx(
            "min-h-0 w-full shrink-0 flex-col overflow-hidden border-r border-line md:flex md:w-[26rem]",
            openKey ? "hidden" : "flex",
          )}
        >
          {noteIds.length > 0 && (
            <BulkBar
              selection={pageSelection(sel, noteIds)}
              count={sel.size}
              archivedView={filters.archived}
              busy={bulk.isPending}
              onToggleAll={() => setSelected(toggleAllOnPage(sel, noteIds))}
              onAction={(a) => runBulk(a, { ids: [...sel] })}
              onDelete={() => setDeleting(true)}
              onPreset={openPreset}
            />
          )}
          <div className="min-h-0 flex-1 overflow-auto">
            {list.isPending || approvalList.isPending ? (
              <LoadingState label="Loading inbox" />
            ) : list.isError && !list.data ? (
              <ErrorState
                error={list.error}
                onRetry={() => void list.refetch()}
              />
            ) : rows.length === 0 ? (
              <EmptyState
                title={
                  filtered ? "Nothing matches these filters" : "Inbox is empty"
                }
                hint={
                  filtered
                    ? undefined
                    : "Notes and approvals waiting for a decision show up here."
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
                  rows={rows}
                  grouped={filters.sort === "priority"}
                  openKey={openKey}
                  cursorKey={cursorId}
                  selected={sel}
                  onToggle={(id) => setSelected(toggleSelected(sel, id))}
                  search={search}
                />
                {filters.kind !== "action" && list.hasNextPage && (
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
          </div>
        </section>
        <section
          aria-label="Detail"
          className={cx(
            "min-h-0 min-w-0 flex-1 overflow-auto md:block",
            openKey ? "block" : "hidden",
          )}
        >
          {!selection ? (
            <EmptyState
              title="Select an item"
              hint="Pick one from the list, or move with j and k and press Enter."
            />
          ) : selection.kind === "approval" ? (
            shownApproval ? (
              <ApprovalDetail
                key={shownApproval.id}
                approval={shownApproval}
                comment={comment}
                onComment={(text) => setDraft({ id: shownApproval.id, text })}
                busy={decide.isPending}
                onDecide={(action, cancelRun) =>
                  decideApproval(shownApproval, action, cancelRun)
                }
                onStale={onStale}
                backTo={{ pathname: "/inbox", search }}
              />
            ) : approvalDetail.isError ? (
              <ErrorState
                error={approvalDetail.error}
                onRetry={() => void approvalDetail.refetch()}
              />
            ) : (
              <LoadingState label="Loading approval" />
            )
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
      <ConfirmDialog
        open={clearing !== null}
        title={clearing?.preset.label ?? "Clear"}
        confirmLabel={
          counter.data ? `Archive ${counter.data.changed}` : "Archive"
        }
        busy={bulk.isPending}
        confirmDisabled={!counter.data || counter.data.changed === 0}
        onCancel={closePreset}
        onConfirm={() => {
          if (clearing) {
            runBulk("archive", { filter: clearing.filter }, closePreset);
          }
        }}
      >
        {counter.isPending || counter.isIdle
          ? "Counting…"
          : counter.isError
            ? `Could not count: ${errorMessage(counter.error)}`
            : clearing &&
              counter.data &&
              confirmText(
                clearing.preset,
                counter.data.changed,
                counter.data.skipped_starred,
              )}
      </ConfirmDialog>
      <ConfirmDialog
        open={deleting}
        title="Delete archived items"
        confirmLabel={`Delete ${sel.size}`}
        busy={bulk.isPending}
        onCancel={() => setDeleting(false)}
        onConfirm={() =>
          runBulk("delete", { ids: [...sel] }, () => setDeleting(false))
        }
      >
        Permanently delete {sel.size} selected{" "}
        {sel.size === 1 ? "item" : "items"}? This cannot be undone. Starred
        items are kept.
      </ConfirmDialog>
      {toast && (
        <div
          role="status"
          className="fixed bottom-4 left-1/2 z-40 flex -translate-x-1/2 items-center gap-3 rounded-md border border-line bg-panel px-3 py-2 text-xs shadow-lg"
        >
          {toast.message}
          {toast.undoIds && (
            <Button onClick={() => undo(toast.undoIds!)}>Undo</Button>
          )}
          <Button variant="ghost" onClick={() => setToast(null)}>
            Dismiss
          </Button>
        </div>
      )}
    </div>
  );
}
