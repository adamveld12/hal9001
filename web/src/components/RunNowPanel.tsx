import { Link, useNavigate } from "react-router";
import type { FlowRunNowResult } from "../api/flows";
import { useFlowRun } from "../api/runs";
import { runSummary } from "../lib/runNow";
import { Time } from "./Time";
import { Button, StatusBadge } from "./ui";

/** One created flow run with its live status (the stream keeps the query fresh). */
function FlowRunLine({ id }: { id: string }) {
  const detail = useFlowRun(id);
  const run = { ...detail, data: detail.data?.flow_run };
  return (
    <li className="flex items-center gap-2">
      <Link
        to={`/runs/${id}`}
        className="font-mono text-accent hover:underline"
      >
        {id}
      </Link>
      {run.data ? (
        <>
          <StatusBadge status={run.data.status} />
          {run.data.kind !== "live" && (
            <span className="text-muted">{run.data.kind}</span>
          )}
          {run.data.error && (
            <span className="truncate text-bad">{run.data.error}</span>
          )}
        </>
      ) : (
        <span className="text-muted">
          {run.isError ? "status unavailable" : "…"}
        </span>
      )}
    </li>
  );
}

/** Result of "Run now" on a flow: per-sensor outcome and the flow runs it created. */
export function RunNowPanel({
  result,
  note,
  onClose,
}: {
  result: FlowRunNowResult;
  /** Shown above the result, e.g. that the flow is disabled. */
  note?: string;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const first = result.flow_run_ids[0];
  return (
    <section
      aria-label="Run now result"
      className="space-y-2 border-b border-line bg-panel px-4 py-2 text-xs"
    >
      <div className="flex items-center gap-2">
        <span className="font-medium">
          Ran {result.flow_id}:{" "}
          {result.flow_run_ids.length === 0
            ? "no flow runs started"
            : `${result.flow_run_ids.length} flow run${result.flow_run_ids.length === 1 ? "" : "s"} started`}
        </span>
        {note && <span className="text-warn">{note}</span>}
        <span className="ml-auto flex items-center gap-2">
          {first && (
            <Button
              icon="external"
              onClick={() => void navigate(`/runs/${first}`)}
            >
              Open run
            </Button>
          )}
          <Button variant="ghost" aria-label="Dismiss" onClick={onClose}>
            Dismiss
          </Button>
        </span>
      </div>
      <ul className="m-0 list-none space-y-1 p-0">
        {result.sensors.map((s) => (
          <li key={s.sensor_id} className="flex flex-wrap items-center gap-2">
            <Link
              to={`/sensors/${s.sensor_id}`}
              className="font-mono text-accent hover:underline"
            >
              {s.sensor_id}
            </Link>
            {s.error ? (
              <span role="alert" className="text-bad">
                {s.error}
              </span>
            ) : (
              <span className="text-muted">{runSummary(s)}</span>
            )}
            {s.run && (
              <span className="text-muted">
                <StatusBadge status={s.run.status} />{" "}
                <Time ts={s.run.started_at} />
              </span>
            )}
          </li>
        ))}
      </ul>
      {result.flow_run_ids.length > 0 && (
        <ul className="m-0 list-none space-y-1 p-0">
          {result.flow_run_ids.map((id) => (
            <FlowRunLine key={id} id={id} />
          ))}
        </ul>
      )}
    </section>
  );
}
