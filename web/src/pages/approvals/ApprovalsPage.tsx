import { useMemo } from "react";
import { useSearchParams } from "react-router";
import { useApprovalsList } from "../../api/approvals";
import { useStatus } from "../../api/status";
import { Tabs } from "../../components/Tabs";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Select,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList, rowEntities } from "../../lib/contextSummary";
import { approvalEntity } from "./context";
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

const TABS_BASE = [
  { key: "pending", label: "Pending" },
  { key: "history", label: "History" },
] as const;

/**
 * Approval history. Deciding happens in the Inbox: every row opens there.
 */
export function ApprovalsPage() {
  const [params, setParams] = useSearchParams();
  const status = useStatus();

  const view = useMemo(() => viewFromParams(params), [params]);
  const query = useMemo(() => viewToQuery(view), [view]);
  const setView = (next: ApprovalsView) =>
    setParams(viewToParams(next), { replace: true });

  const list = useApprovalsList(query);
  const items = useMemo(
    () => mergeApprovalPages(list.data?.pages ?? [], view.tab),
    [list.data, view.tab],
  );

  const pendingCount = status.data?.pending_approvals;
  const tabs = TABS_BASE.map((t) => ({
    ...t,
    count: t.key === "pending" ? pendingCount : undefined,
  }));
  const filtered = view.type !== null || view.status !== null;
  usePageContext(
    list.isPending
      ? null
      : {
          title: "Approvals",
          facts: factList([
            ["tab", view.tab],
            ["status filter", view.status],
            ["type filter", view.type],
            ["pending approvals", pendingCount],
            ["rows loaded", items.length],
          ]),
          entities: rowEntities(items, approvalEntity),
        },
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PageHeader
        title="Approvals"
        subtitle="Decisions are made in the Inbox; open a row to see it there."
      />
      <Tabs<ApprovalsTab>
        tabs={tabs}
        active={view.tab}
        onChange={(tab) => setView({ ...view, tab, status: null })}
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
          <option value="request">Requests</option>
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
      <div className="min-h-0 flex-1 overflow-auto">
        {list.isPending ? (
          <LoadingState label="Loading approvals" />
        ) : list.isError && !list.data ? (
          <ErrorState error={list.error} onRetry={() => void list.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState
            title={
              filtered
                ? "No approvals match these filters"
                : view.tab === "pending"
                  ? "Nothing is waiting for a decision"
                  : "No decided approvals yet"
            }
          />
        ) : (
          <>
            <ApprovalList items={items} />
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
      </div>
    </div>
  );
}
