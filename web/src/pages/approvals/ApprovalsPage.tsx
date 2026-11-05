import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router";
import {
  isAlreadyDecided,
  useApproval,
  useApprovalsList,
  useDecideApproval,
  type Approval,
} from "../../api/approvals";
import { errorMessage } from "../../api/client";
import { qk } from "../../api/keys";
import { useStatus } from "../../api/status";
import { Tabs } from "../../components/Tabs";
import {
  Button,
  EmptyState,
  ErrorState,
  Kbd,
  LoadingState,
  PageHeader,
  Select,
} from "../../components/ui";
import { cx } from "../../lib/cx";
import { useHotkeys } from "../inbox/hotkeys";
import { moveCursor, neighborAfterRemoval } from "../inbox/selection";
import { ApprovalDetail } from "./ApprovalDetail";
import { ApprovalList } from "./ApprovalList";
import {
  HISTORY_STATUSES,
  mergeApprovalPages,
  viewFromParams,
  viewToParams,
  viewToQuery,
  type ApprovalsTab,
  type ApprovalsView,
} from "./queue";

interface Notice {
  tone: "ok" | "warn";
  text: string;
}

const SHORTCUTS: [string, string][] = [
  ["j k", "move"],
  ["a", "approve / allow"],
  ["d", "deny"],
  ["c", "deny + cancel run"],
];

const TABS_BASE = [
  { key: "pending", label: "Pending" },
  { key: "history", label: "History" },
] as const;

function decisionNotice(a: Approval): string {
  if (a.status === "approved") {
    return a.type === "tool_call" ? "Tool call allowed." : "Proposal approved.";
  }
  if (a.cancel_run) return "Denied. The run was cancelled.";
  return a.type === "tool_call" ? "Tool call denied." : "Proposal denied.";
}

export function ApprovalsPage() {
  const { id: openId } = useParams<{ id: string }>();
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const status = useStatus();

  const view = useMemo(() => viewFromParams(params), [params]);
  const query = useMemo(() => viewToQuery(view), [view]);
  const search = viewToParams(view).toString();
  const setView = (next: ApprovalsView) =>
    setParams(viewToParams(next), { replace: true });

  const list = useApprovalsList(query);
  const detail = useApproval(openId);
  const decide = useDecideApproval();

  const items = useMemo(
    () => mergeApprovalPages(list.data?.pages ?? [], view.tab),
    [list.data, view.tab],
  );
  const ids = items.map((a) => a.id);

  // Where the open approval last was in the list, so j/k continue from there
  // after it is decided and leaves the queue (updated during render).
  const [openPos, setOpenPos] = useState<{ id: string; index: number } | null>(
    null,
  );
  const openAt = openId ? ids.indexOf(openId) : -1;
  if (
    openId &&
    openAt >= 0 &&
    (openPos?.id !== openId || openPos.index !== openAt)
  ) {
    setOpenPos({ id: openId, index: openAt });
  }

  const [draft, setDraft] = useState({ id: "", text: "" });
  const comment = draft.id === openId ? draft.text : "";
  const [notice, setNotice] = useState<Notice | null>(null);

  const shown: Approval | undefined =
    detail.data ?? items.find((a) => a.id === openId);
  const canDecide = shown?.status === "pending" && shown.id === openId;

  const open = (id: string) =>
    void navigate({ pathname: `/approvals/${id}`, search }, { replace: true });
  const closeDetail = () => void navigate({ pathname: "/approvals", search });

  const submit = (
    a: Approval,
    action: "approve" | "deny",
    cancelRun?: boolean,
  ) => {
    if (decide.isPending) return;
    const next = neighborAfterRemoval(ids, a.id);
    decide.mutate(
      { id: a.id, action, comment, cancelRun },
      {
        onSuccess: (done) => {
          setNotice({ tone: "ok", text: decisionNotice(done) });
          setDraft({ id: "", text: "" });
          // Keep working through the queue; the last one stays open to show its outcome.
          if (view.tab === "pending" && next) open(next);
        },
        onError: (err) => {
          if (isAlreadyDecided(err)) {
            setNotice({
              tone: "warn",
              text: "That approval was already decided or has expired. The list was refreshed.",
            });
          }
        },
      },
    );
  };

  const move = (dir: 1 | -1) => {
    const to = moveCursor(ids, openId ?? null, dir, openPos?.index ?? 0);
    if (to && to !== openId) open(to);
  };

  useHotkeys({
    j: () => move(1),
    k: () => move(-1),
    Escape: () => {
      if (openId) closeDetail();
    },
    a: () => canDecide && submit(shown, "approve"),
    d: () => canDecide && submit(shown, "deny"),
    c: () =>
      canDecide && shown.type === "tool_call" && submit(shown, "deny", true),
  });

  const onStale = useCallback(() => {
    void qc.invalidateQueries({ queryKey: ["approvals"] });
    void qc.invalidateQueries({ queryKey: qk("approval", openId) });
  }, [qc, openId]);

  const pendingCount = status.data?.pending_approvals;
  const tabs = TABS_BASE.map((t) => ({
    ...t,
    count: t.key === "pending" ? pendingCount : undefined,
  }));
  const filtered = view.type !== null || view.status !== null;
  const showError = decide.isError && !isAlreadyDecided(decide.error);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title="Approvals"
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
      <Tabs<ApprovalsTab>
        tabs={tabs}
        active={view.tab}
        onChange={(tab) => {
          // The open approval may not belong to the other list.
          void navigate({
            pathname: "/approvals",
            search: viewToParams({ ...view, tab, status: null }).toString(),
          });
        }}
      />
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-3 py-2">
        <Select
          aria-label="Approval type"
          className="w-auto"
          value={view.type ?? ""}
          onChange={(e) =>
            setView({
              ...view,
              type: (e.target.value || null) as ApprovalsView["type"],
            })
          }
        >
          <option value="">All types</option>
          <option value="proposal">Proposals</option>
          <option value="tool_call">Tool calls</option>
        </Select>
        {view.tab === "history" && (
          <Select
            aria-label="Outcome"
            className="w-auto"
            value={view.status ?? ""}
            onChange={(e) =>
              setView({
                ...view,
                status: (e.target.value || null) as ApprovalsView["status"],
              })
            }
          >
            <option value="">Any outcome</option>
            {HISTORY_STATUSES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </Select>
        )}
      </div>
      {notice && (
        <div
          role="status"
          className={cx(
            "flex items-center gap-2 border-b px-4 py-1.5 text-xs",
            notice.tone === "ok"
              ? "border-ok/40 bg-ok/10 text-ok"
              : "border-warn/40 bg-warn/10 text-warn",
          )}
        >
          {notice.text}
          <Button
            variant="ghost"
            className="ml-auto"
            onClick={() => setNotice(null)}
          >
            Dismiss
          </Button>
        </div>
      )}
      {showError && (
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
      <div className="flex min-h-0 flex-1">
        <section
          aria-label="Approvals"
          className={cx(
            "min-h-0 w-full shrink-0 flex-col overflow-auto border-r border-line md:flex md:w-[26rem]",
            openId ? "hidden" : "flex",
          )}
        >
          {list.isPending ? (
            <LoadingState label="Loading approvals" />
          ) : list.isError && !list.data ? (
            <ErrorState
              error={list.error}
              onRetry={() => void list.refetch()}
            />
          ) : items.length === 0 ? (
            <EmptyState
              title={
                filtered
                  ? "No approvals match these filters"
                  : view.tab === "pending"
                    ? "Nothing is waiting for a decision"
                    : "No decided approvals yet"
              }
              hint={
                view.tab === "pending" && !filtered
                  ? "Proposals and tool calls that need approval show up here."
                  : undefined
              }
            />
          ) : (
            <>
              <ApprovalList items={items} openId={openId} search={search} />
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
          aria-label="Approval"
          className={cx(
            "min-h-0 min-w-0 flex-1 overflow-auto md:block",
            openId ? "block" : "hidden",
          )}
        >
          {!openId ? (
            <EmptyState
              title="Select an approval"
              hint="Pick one from the list, or press j to open the first."
            />
          ) : shown ? (
            <ApprovalDetail
              key={shown.id}
              approval={shown}
              comment={comment}
              onComment={(text) => setDraft({ id: shown.id, text })}
              busy={decide.isPending}
              onDecide={(action, cancelRun) => submit(shown, action, cancelRun)}
              onStale={onStale}
              backTo={{ pathname: "/approvals", search }}
            />
          ) : detail.isError ? (
            <ErrorState
              error={detail.error}
              onRetry={() => void detail.refetch()}
            />
          ) : (
            <LoadingState label="Loading approval" />
          )}
        </section>
      </div>
    </div>
  );
}
