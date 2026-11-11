import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useEvents, useFlowOptions, useSensorOptions } from "../../api/events";
import type { EventSummary, FlowRunBrief } from "../../api/events";
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
  TextInput,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import {
  countBy,
  factList,
  rowEntities,
  trimText,
} from "../../lib/contextSummary";
import { Time } from "../../components/Time";
import {
  DECISIONS,
  filtersFromParams,
  filtersToParams,
  filtersToQuery,
  hasActiveFilters,
  KINDS,
  NO_FILTERS,
  RUN_STATUSES,
  type TimelineFilters,
} from "./filters";
import { InjectDialog } from "./InjectDialog";

const KIND_LABEL: Record<(typeof KINDS)[number], string> = {
  live: "live",
  replay: "replay",
  test: "test",
};

export function TimelinePage() {
  const [params, setParams] = useSearchParams();
  const filters = useMemo(() => filtersFromParams(params), [params]);
  const [injecting, setInjecting] = useState(false);

  const setFilters = (next: TimelineFilters) =>
    setParams(filtersToParams(next), { replace: true });

  const sensors = useSensorOptions();
  const flows = useFlowOptions();
  const feed = useEvents(filtersToQuery(filters));
  const events = feed.data?.pages.flatMap((p) => p.items) ?? [];
  usePageContext(
    feed.isPending
      ? null
      : {
          title: "Timeline",
          facts: factList([
            ["sensor filter", filters.sensor],
            ["flow filter", filters.flow],
            ["run status filter", filters.status],
            ["decision filter", filters.decision],
            ["run kind filter", filters.kind],
            ["search", trimText(filters.q, 60)],
            ["events loaded", events.length],
            ["more pages", feed.hasNextPage],
          ]),
          entities: rowEntities(events, (e) => ({
            kind: "event",
            id: e.id,
            label: e.sensor_id,
            detail: e.flow_runs.length
              ? `runs: ${countBy(e.flow_runs, (r) => r.status)}`
              : "no flow runs",
          })),
        },
  );

  return (
    <>
      <PageHeader
        title="Timeline"
        subtitle="Events and the flow runs they started, newest first"
        actions={
          <>
            {feed.isFetching && !feed.isPending && <Spinner label="Updating" />}
            <Button icon="plus" onClick={() => setInjecting(true)}>
              Inject event
            </Button>
          </>
        }
      />
      <FilterBar
        filters={filters}
        onChange={setFilters}
        sensorIds={sensors.data?.map((s) => s.id) ?? []}
        flowIds={flows.data?.map((f) => f.id) ?? []}
      />
      {feed.isPending ? (
        <LoadingState label="Loading events" />
      ) : feed.isError && !feed.data ? (
        <ErrorState error={feed.error} onRetry={() => void feed.refetch()} />
      ) : events.length === 0 ? (
        <EmptyState
          title={
            hasActiveFilters(filters) ? "No matching events" : "No events yet"
          }
          hint={
            hasActiveFilters(filters)
              ? "Nothing matches these filters."
              : "Events appear when a sensor finds new items. You can also inject one."
          }
          action={
            hasActiveFilters(filters) ? (
              <Button onClick={() => setFilters(NO_FILTERS)}>
                Clear filters
              </Button>
            ) : (
              <Button icon="plus" onClick={() => setInjecting(true)}>
                Inject event
              </Button>
            )
          }
        />
      ) : (
        <>
          <ul className="m-0 list-none p-0">
            {events.map((e) => (
              <EventRow key={e.id} event={e} />
            ))}
          </ul>
          <div className="flex justify-center p-3">
            {feed.hasNextPage ? (
              <Button
                disabled={feed.isFetchingNextPage}
                onClick={() => void feed.fetchNextPage()}
              >
                {feed.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            ) : (
              <span className="text-xs text-muted">End of timeline</span>
            )}
          </div>
          {feed.isFetchNextPageError && <ErrorState error={feed.error} />}
        </>
      )}
      {injecting && (
        <InjectDialog
          defaultSensor={filters.sensor}
          onClose={() => setInjecting(false)}
        />
      )}
    </>
  );
}

function FilterBar({
  filters,
  onChange,
  sensorIds,
  flowIds,
}: {
  filters: TimelineFilters;
  onChange: (f: TimelineFilters) => void;
  sensorIds: string[];
  flowIds: string[];
}) {
  // Search text is debounced into the URL so typing doesn't refetch per key.
  const [text, setText] = useState(filters.q);
  const [seenQ, setSeenQ] = useState(filters.q);
  if (seenQ !== filters.q) {
    setSeenQ(filters.q);
    setText(filters.q);
  }
  useEffect(() => {
    if (text === filters.q) return;
    const t = setTimeout(() => onChange({ ...filters, q: text }), 300);
    return () => clearTimeout(t);
  }, [text, filters, onChange]);

  const set = <K extends keyof TimelineFilters>(
    key: K,
    value: TimelineFilters[K],
  ) => onChange({ ...filters, [key]: value });

  return (
    <div className="flex flex-wrap items-center gap-2 border-b border-line px-4 py-2">
      <TextInput
        className="w-56"
        type="search"
        placeholder="Search payload text"
        aria-label="Search payload text"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <Select
        className="w-36"
        aria-label="Sensor"
        value={filters.sensor}
        onChange={(e) => set("sensor", e.target.value)}
      >
        <option value="">All sensors</option>
        {withCurrent(sensorIds, filters.sensor).map((id) => (
          <option key={id} value={id}>
            {id}
          </option>
        ))}
      </Select>
      <Select
        className="w-36"
        aria-label="Flow"
        value={filters.flow}
        onChange={(e) => set("flow", e.target.value)}
      >
        <option value="">All flows</option>
        {withCurrent(flowIds, filters.flow).map((id) => (
          <option key={id} value={id}>
            {id}
          </option>
        ))}
      </Select>
      <Select
        className="w-36"
        aria-label="Run status"
        value={filters.status}
        onChange={(e) =>
          set("status", e.target.value as TimelineFilters["status"])
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
        className="w-36"
        aria-label="Decision"
        value={filters.decision}
        onChange={(e) =>
          set("decision", e.target.value as TimelineFilters["decision"])
        }
      >
        <option value="">Any decision</option>
        {DECISIONS.map((d) => (
          <option key={d} value={d}>
            {d}
          </option>
        ))}
      </Select>
      <Select
        className="w-40"
        aria-label="Run kind"
        value={filters.kind}
        onChange={(e) => set("kind", e.target.value as TimelineFilters["kind"])}
      >
        <option value="">Live and replay</option>
        {KINDS.map((k) => (
          <option key={k} value={k}>
            {KIND_LABEL[k]} only
          </option>
        ))}
      </Select>
      {hasActiveFilters(filters) && (
        <Button
          variant="ghost"
          onClick={() => {
            setText("");
            onChange(NO_FILTERS);
          }}
        >
          Clear
        </Button>
      )}
      <span className="ml-auto text-[11px] text-muted">
        All filters are applied by the server.
      </span>
    </div>
  );
}

/** Keeps a URL-supplied id selectable even if it is absent from the loaded list. */
function withCurrent(ids: string[], current: string): string[] {
  return current && !ids.includes(current) ? [current, ...ids] : ids;
}

function EventRow({ event }: { event: EventSummary }) {
  return (
    <li className="flex items-start gap-3 border-b border-line px-4 py-2 hover:bg-raised/60">
      <div className="w-20 shrink-0 pt-0.5 text-xs">
        <Time ts={event.created_at} />
      </div>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex items-center gap-2 text-xs">
          <Link
            to={`/sensors/${event.sensor_id}`}
            className="font-medium hover:underline"
          >
            {event.sensor_id}
          </Link>
          {event.injected && <Badge tone="info">injected</Badge>}
          <Link
            to={`/events/${event.id}`}
            className="ml-auto text-accent hover:underline"
          >
            Event
          </Link>
        </div>
        <Link
          to={`/events/${event.id}`}
          className="block truncate font-mono text-[11px] text-muted hover:text-fg"
          title={event.payload_preview}
        >
          {event.payload_preview}
        </Link>
        {event.flow_runs.length > 0 ? (
          <div className="flex flex-wrap gap-1.5">
            {event.flow_runs.map((r) => (
              <RunChip key={r.id} run={r} />
            ))}
          </div>
        ) : (
          <div className="text-[11px] text-muted">No flow runs</div>
        )}
      </div>
    </li>
  );
}

function RunChip({ run }: { run: FlowRunBrief }) {
  return (
    <Link
      to={`/runs/${run.id}`}
      className="inline-flex items-center gap-1.5 rounded border border-line bg-panel px-1.5 py-0.5 text-xs hover:bg-raised"
    >
      <span>{run.flow_id}</span>
      <StatusBadge status={run.status} />
      {run.kind !== "live" && <Badge>{run.kind}</Badge>}
      {run.dry_run && <Badge tone="warn">dry run</Badge>}
      {run.replay_of && <Badge>replay of earlier run</Badge>}
    </Link>
  );
}
