import { Link, useSearchParams } from "react-router";
import { useMemo } from "react";
import { useAgentRunList } from "../../api/runs";
import { useRuntimeNames } from "../../api/settings";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Select,
  Spinner,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { durationBetween, shortId } from "../../lib/format";
import {
  AGENT_RUN_STATUSES,
  agentRunFiltersFromParams,
  agentRunFiltersToQuery,
  agentRunOwner,
  filtersToParams,
  hasActive,
  NO_AGENT_RUN_FILTERS,
  type AgentRunFilters,
} from "./runList";
import type { AgentRun } from "../../api/runs";

function Owner({ run }: { run: AgentRun }) {
  const o = agentRunOwner(run);
  if (o.kind === "flow") {
    return (
      <span className="inline-flex flex-wrap items-center gap-1.5">
        <Link to={`/flows/${o.flowId}`} className="text-accent">
          {o.flowId}
        </Link>
        {o.flowRunId && (
          <Link
            to={`/runs/${o.flowRunId}`}
            className="font-mono text-muted hover:text-accent"
          >
            run {shortId(o.flowRunId)}
          </Link>
        )}
      </span>
    );
  }
  if (o.kind === "assist") {
    return (
      <Link
        to={`/sensors?assist=${o.sessionId}`}
        className="inline-flex items-center gap-1.5 text-accent"
      >
        Assist session <span className="font-mono">{shortId(o.sessionId)}</span>
      </Link>
    );
  }
  return <span className="text-muted">–</span>;
}

export function AgentRunsPage() {
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => agentRunFiltersFromParams(params), [params]);
  const set = (next: AgentRunFilters) =>
    setParams(filtersToParams(next), { replace: true });
  const runtimes = useRuntimeNames();
  const list = useAgentRunList(agentRunFiltersToQuery(filters));
  const runs = list.data?.pages.flatMap((p) => p.items) ?? [];
  const active = hasActive(filters);

  const runtimeNames = [...(runtimes.data ?? [])];
  if (filters.runtime && !runtimeNames.includes(filters.runtime))
    runtimeNames.unshift(filters.runtime);

  return (
    <>
      <PageHeader
        title="Agent runs"
        subtitle="Every agent session, from flows and assist chats, newest first"
        actions={
          list.isFetching && !list.isPending ? (
            <Spinner label="Updating" />
          ) : undefined
        }
      />
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2">
        <Select
          className="w-40"
          aria-label="Run status"
          value={filters.status}
          onChange={(e) =>
            set({
              ...filters,
              status: e.target.value as AgentRunFilters["status"],
            })
          }
        >
          <option value="">Any status</option>
          {AGENT_RUN_STATUSES.map((s) => (
            <option key={s} value={s}>
              {s.replaceAll("_", " ")}
            </option>
          ))}
        </Select>
        <Select
          className="w-40"
          aria-label="Runtime"
          value={filters.runtime}
          onChange={(e) => set({ ...filters, runtime: e.target.value })}
        >
          <option value="">Any runtime</option>
          {runtimeNames.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </Select>
        {active && (
          <Button variant="ghost" onClick={() => set(NO_AGENT_RUN_FILTERS)}>
            Clear
          </Button>
        )}
      </div>
      {list.isPending ? (
        <LoadingState label="Loading agent runs" />
      ) : list.isError && !list.data ? (
        <ErrorState error={list.error} onRetry={() => void list.refetch()} />
      ) : runs.length === 0 ? (
        <EmptyState
          title={active ? "No matching agent runs" : "No agent runs yet"}
          hint={
            active
              ? "Nothing matches these filters."
              : "Agent runs appear when a flow agent node or an assist chat runs."
          }
        />
      ) : (
        <div className="overflow-auto">
          <Table>
            <thead>
              <tr>
                <Th>Run</Th>
                <Th>Runtime</Th>
                <Th>Mode</Th>
                <Th>Status</Th>
                <Th>Flow / session</Th>
                <Th>Started</Th>
                <Th>Duration</Th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.id} className="hover:bg-raised/60">
                  <Td>
                    <Link
                      to={`/agent-runs/${r.id}`}
                      className="font-mono text-accent"
                    >
                      {shortId(r.id)}
                    </Link>
                  </Td>
                  <Td>{r.runtime}</Td>
                  <Td>
                    <Badge>{r.mode}</Badge>
                  </Td>
                  <Td>
                    <StatusBadge status={r.status} />
                  </Td>
                  <Td>
                    <Owner run={r} />
                  </Td>
                  <Td>
                    <Time ts={r.started_at ?? r.created_at} />
                  </Td>
                  <Td className="tabular-nums">
                    {durationBetween(r.started_at, r.ended_at)}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
          <div className="flex justify-center p-3">
            {list.hasNextPage ? (
              <Button
                disabled={list.isFetchingNextPage}
                onClick={() => void list.fetchNextPage()}
              >
                {list.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            ) : (
              <span className="text-xs text-muted">End of agent runs</span>
            )}
          </div>
          {list.isFetchNextPageError && <ErrorState error={list.error} />}
        </div>
      )}
    </>
  );
}
