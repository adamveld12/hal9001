import { Fragment } from "react";
import { useSensorRun, useSensorRuns, type SensorRun } from "../../api/sensors";
import { BlobView } from "../../components/BlobView";
import { SensorStdout } from "../../components/SensorStdout";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  StatusBadge,
  Table,
  Td,
  Th,
} from "../../components/ui";
import { cx } from "../../lib/cx";
import { durationBetween, formatTime } from "../../lib/format";
import { isFailedRun } from "./view";

function RunDetails({ run }: { run: SensorRun }) {
  // List responses cut output to 4 KiB; the single-run response has up to 64 KiB.
  const full = useSensorRun(run.id, true);
  const r = full.data ?? run;
  return (
    <div className="w-0 min-w-full space-y-3 p-3">
      <dl className="grid grid-cols-[repeat(auto-fill,minmax(9rem,1fr))] gap-x-4 gap-y-2 text-xs">
        <Fact label="Started" value={formatTime(r.started_at)} />
        <Fact label="Ended" value={r.ended_at ? formatTime(r.ended_at) : "–"} />
        <Fact label="Exit code" value={r.exit_code ?? "–"} />
        <Fact label="Items" value={r.item_count ?? "–"} />
        <Fact label="New" value={r.new_count ?? "–"} />
        <Fact label="Trigger" value={r.trigger} />
        {r.error && <Fact label="Error" value={r.error} bad />}
      </dl>
      {r.baseline && (
        <div className="text-xs text-muted">
          Baseline poll: items were marked seen without firing flows.
        </div>
      )}
      {full.isError && (
        <ErrorState error={full.error} onRetry={() => void full.refetch()} />
      )}
      <div className="space-y-1">
        <div className="text-[11px] font-medium text-muted">stdout (items)</div>
        <SensorStdout blob={r.stdout} />
      </div>
      <div className="space-y-1">
        <div className="text-[11px] font-medium text-muted">stderr</div>
        <BlobView blob={r.stderr} />
      </div>
    </div>
  );
}

function Fact({
  label,
  value,
  bad,
}: {
  label: string;
  value: string | number;
  bad?: boolean;
}) {
  return (
    <div>
      <dt className="text-[11px] text-muted">{label}</dt>
      <dd className={cx("m-0 font-mono", bad && "text-bad")}>{value}</dd>
    </div>
  );
}

/** Recent polls of one sensor; click a row for exit code, counts, stdout and stderr. */
export function SensorRuns({
  sensorId,
  expandedId,
  onToggle,
}: {
  sensorId: string;
  expandedId: string | null;
  onToggle: (runId: string) => void;
}) {
  const runs = useSensorRuns(sensorId);
  if (runs.isPending) return <LoadingState label="Loading polls" />;
  if (runs.isError) {
    return (
      <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
    );
  }
  const items = runs.data.pages.flatMap((p) => p.items);
  if (items.length === 0) {
    return (
      <EmptyState
        title="No polls yet"
        hint="Polls appear here after the first scheduled poll or Run now."
      />
    );
  }
  return (
    <div>
      <Table>
        <thead>
          <tr>
            <Th>Status</Th>
            <Th>Started</Th>
            <Th>Duration</Th>
            <Th>Exit</Th>
            <Th>Items</Th>
            <Th>New</Th>
            <Th>Failure</Th>
          </tr>
        </thead>
        <tbody>
          {items.map((r) => {
            const open = expandedId === r.id;
            const failed = isFailedRun(r);
            return (
              <Fragment key={r.id}>
                <tr className={cx(failed && "bg-bad/5", "hover:bg-raised")}>
                  <Td>
                    <button
                      type="button"
                      aria-expanded={open}
                      onClick={() => onToggle(r.id)}
                      className="inline-flex items-center gap-2"
                    >
                      <span aria-hidden="true" className="w-3 text-muted">
                        {open ? "▾" : "▸"}
                      </span>
                      <StatusBadge status={r.status} />
                    </button>
                    {r.trigger === "manual" && (
                      <Badge className="ml-1.5">manual</Badge>
                    )}
                    {r.baseline && <Badge className="ml-1.5">baseline</Badge>}
                  </Td>
                  <Td>
                    <Time ts={r.started_at} />
                  </Td>
                  <Td className="tabular-nums">
                    {durationBetween(r.started_at, r.ended_at)}
                  </Td>
                  <Td className="font-mono">{r.exit_code ?? "–"}</Td>
                  <Td className="tabular-nums">{r.item_count ?? "–"}</Td>
                  <Td className="tabular-nums">{r.new_count ?? "–"}</Td>
                  <Td
                    className={failed ? "font-medium text-bad" : "text-muted"}
                  >
                    {failed ? (r.error ?? r.status) : "–"}
                  </Td>
                </tr>
                {open && (
                  <tr>
                    <td colSpan={7} className="border-b border-line bg-panel">
                      <RunDetails run={r} />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </Table>
      {runs.hasNextPage && (
        <div className="p-2">
          <Button
            onClick={() => void runs.fetchNextPage()}
            disabled={runs.isFetchingNextPage}
          >
            {runs.isFetchingNextPage ? "Loading…" : "Load older polls"}
          </Button>
        </div>
      )}
    </div>
  );
}
