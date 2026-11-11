import { kindTone } from "../../lib/status";
import { useFlowRunList } from "../../api/runs";
import { useFlowOptions } from "../../api/events";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  Select,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { Time } from "../../components/Time";
import { Link } from "react-router";
import { durationBetween, shortId } from "../../lib/format";
import {
  flowRunFiltersToQuery,
  hasActive,
  KINDS,
  NO_FLOW_RUN_FILTERS,
  RUN_STATUSES,
  type FlowRunFilters,
} from "./runList";

/**
 * Recent flow runs with filters and "load more". With `flowId` the list is
 * that flow's runs only and the flow filter and column are hidden. Filter
 * storage (URL or local state) belongs to the caller.
 */
export function FlowRunsList({
  filters,
  onChange,
  flowId,
}: {
  filters: FlowRunFilters;
  onChange: (f: FlowRunFilters) => void;
  flowId?: string;
}) {
  const flows = useFlowOptions();
  const list = useFlowRunList(flowRunFiltersToQuery(filters, flowId));
  const runs = list.data?.pages.flatMap((p) => p.items) ?? [];
  const set = <K extends keyof FlowRunFilters>(k: K, v: FlowRunFilters[K]) =>
    onChange({ ...filters, [k]: v });
  const active = hasActive(flowId ? { ...filters, flow: "" } : filters);

  const flowIds = flows.data?.map((f) => f.id) ?? [];
  if (filters.flow && !flowIds.includes(filters.flow))
    flowIds.unshift(filters.flow);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-auto">
      <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2">
        {!flowId && (
          <Select
            className="w-40"
            aria-label="Flow"
            value={filters.flow}
            onChange={(e) => set("flow", e.target.value)}
          >
            <option value="">All flows</option>
            {flowIds.map((id) => (
              <option key={id} value={id}>
                {id}
              </option>
            ))}
          </Select>
        )}
        <Select
          className="w-40"
          aria-label="Run status"
          value={filters.status}
          onChange={(e) =>
            set("status", e.target.value as FlowRunFilters["status"])
          }
        >
          <option value="">Any run status</option>
          {RUN_STATUSES.map((s) => (
            <option key={s} value={s}>
              {s.replaceAll("_", " ")}
            </option>
          ))}
        </Select>
        <Select
          className="w-40"
          aria-label="Run kind"
          value={filters.kind}
          onChange={(e) =>
            set("kind", e.target.value as FlowRunFilters["kind"])
          }
        >
          <option value="">Live, replay and continuation</option>
          {KINDS.map((k) => (
            <option key={k} value={k}>
              {k} only
            </option>
          ))}
        </Select>
        {active && (
          <Button variant="ghost" onClick={() => onChange(NO_FLOW_RUN_FILTERS)}>
            Clear
          </Button>
        )}
      </div>
      {list.isPending ? (
        <LoadingState label="Loading runs" />
      ) : list.isError && !list.data ? (
        <ErrorState error={list.error} onRetry={() => void list.refetch()} />
      ) : runs.length === 0 ? (
        <EmptyState
          title={active ? "No matching runs" : "No runs yet"}
          hint={
            active
              ? "Nothing matches these filters."
              : "Runs appear when a flow handles an event."
          }
        />
      ) : (
        <>
          <Table>
            <thead>
              <tr>
                <Th>Run</Th>
                {!flowId && <Th>Flow</Th>}
                <Th>Status</Th>
                <Th>Kind</Th>
                <Th>Started</Th>
                <Th>Duration</Th>
                <Th>Trigger event</Th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.id} className="hover:bg-raised/60">
                  <Td>
                    <Link
                      to={`/runs/${r.id}`}
                      className="font-mono text-accent"
                    >
                      {shortId(r.id)}
                    </Link>
                  </Td>
                  {!flowId && (
                    <Td>
                      <Link to={`/flows/${r.flow_id}`} className="text-accent">
                        {r.flow_id}
                      </Link>
                    </Td>
                  )}
                  <Td>
                    <StatusBadge status={r.status} />
                  </Td>
                  <Td>
                    <span className="inline-flex gap-1">
                      <Badge tone={kindTone(r.kind)}>{r.kind}</Badge>
                      {r.dry_run && <Badge tone="info">dry run</Badge>}
                    </span>
                  </Td>
                  <Td>
                    <Time ts={r.started_at ?? r.created_at} />
                  </Td>
                  <Td className="tabular-nums">
                    {durationBetween(r.started_at, r.ended_at)}
                  </Td>
                  <Td>
                    {r.event_id ? (
                      <Link
                        to={`/events/${r.event_id}`}
                        className="font-mono text-accent"
                      >
                        {shortId(r.event_id)}
                      </Link>
                    ) : (
                      <span className="text-muted">–</span>
                    )}
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
              <span className="text-xs text-muted">End of runs</span>
            )}
          </div>
          {list.isFetchNextPageError && <ErrorState error={list.error} />}
        </>
      )}
    </div>
  );
}
