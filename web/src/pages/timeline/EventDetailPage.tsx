import { useState } from "react";
import { Link, useParams } from "react-router";
import { useEventDetail } from "../../api/events";
import type { EventDetail } from "../../api/events";
import { CopyButton } from "../../components/CopyButton";
import { JsonView } from "../../components/JsonView";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
  PageHeader,
  Panel,
  StatusBadge,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList, rowEntities } from "../../lib/contextSummary";
import { durationBetween } from "../../lib/format";
import { ApiError } from "../../api/client";
import { ReplayDialog } from "./ReplayDialog";
import { decisionCounts, decisionPathText, flattenBranch } from "./tree";

type Branch = EventDetail["fan_out"][number];

export function EventDetailPage() {
  const { id = "" } = useParams();
  const q = useEventDetail(id);
  const [replaying, setReplaying] = useState(false);
  const detail = q.data;
  usePageContext(
    detail
      ? {
          title: "Event",
          facts: factList([
            ["event id", detail.event.id],
            ["sensor", detail.event.sensor_id],
            ["injected", detail.event.injected],
            ["sensor run status", detail.sensor_run?.status],
            ["flow runs", detail.fan_out.length],
          ]),
          entities: rowEntities(detail.fan_out, (b) => ({
            kind: "flow_run",
            id: b.flow_run.id,
            label: b.flow_run.flow_id,
            detail: b.flow_run.status,
          })),
        }
      : null,
  );

  if (q.isPending) return <LoadingState label="Loading event" />;
  if (q.isError) {
    if (q.error instanceof ApiError && q.error.status === 404) {
      return (
        <EmptyState
          title="Event not found"
          hint="It may have been removed by retention."
          action={
            <Link className="text-accent hover:underline" to="/timeline">
              Back to timeline
            </Link>
          }
        />
      );
    }
    return <ErrorState error={q.error} onRetry={() => void q.refetch()} />;
  }

  const { event, sensor_run: sensorRun, fan_out: fanOut } = q.data;

  return (
    <>
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            Event <code className="font-mono text-xs">{event.id}</code>
            {event.injected && <Badge tone="info">injected</Badge>}
          </span>
        }
        subtitle={
          <>
            <Link
              to={`/sensors/${event.sensor_id}`}
              className="hover:underline"
            >
              {event.sensor_id}
            </Link>
            {" · "}
            <Time ts={event.created_at} />
          </>
        }
        actions={
          <>
            <Link
              to="/timeline"
              className="text-xs text-accent hover:underline"
            >
              Timeline
            </Link>
            <Button icon="play" onClick={() => setReplaying(true)}>
              Replay into flow…
            </Button>
          </>
        }
      />
      <div className="space-y-4 p-4">
        <Panel
          title="Payload"
          actions={<CopyPayload payload={event.payload} />}
        >
          <JsonView value={event.payload} maxHeight="28rem" />
        </Panel>

        <Panel title="Origin">
          <dl className="m-0 grid grid-cols-[8rem_1fr] gap-x-3 gap-y-1.5 text-xs">
            <dt className="text-muted">Dedup key</dt>
            <dd className="m-0 flex items-center gap-2 break-all font-mono">
              {event.dedup_key}
            </dd>
            <dt className="text-muted">Sensor run</dt>
            <dd className="m-0">
              {sensorRun ? (
                <span className="flex flex-wrap items-center gap-2">
                  <Link
                    to={`/sensors/${sensorRun.sensor_id}`}
                    className="font-mono text-accent hover:underline"
                  >
                    {sensorRun.id}
                  </Link>
                  <StatusBadge status={sensorRun.status} />
                  <Badge>{sensorRun.trigger}</Badge>
                  <Time ts={sensorRun.started_at} />
                </span>
              ) : (
                <span className="text-muted">
                  {event.injected
                    ? "None (injected without a poll)"
                    : "Not recorded"}
                </span>
              )}
            </dd>
          </dl>
        </Panel>

        <Panel title={`Flow runs (${fanOut.length})`}>
          {fanOut.length === 0 ? (
            <div className="text-xs text-muted">
              No flow subscribed to this sensor handled this event.
            </div>
          ) : (
            <div className="space-y-3">
              {fanOut.map((b) => (
                <BranchView key={b.flow_run.id} branch={b} />
              ))}
            </div>
          )}
        </Panel>
      </div>
      {replaying && (
        <ReplayDialog eventId={event.id} onClose={() => setReplaying(false)} />
      )}
    </>
  );
}

function CopyPayload({ payload }: { payload: unknown }) {
  return <CopyButton text={JSON.stringify(payload, null, 2)} />;
}

function BranchView({ branch }: { branch: Branch }) {
  const run = branch.flow_run;
  const steps = flattenBranch(branch.node_runs);
  const counts = Object.entries(decisionCounts(steps));
  return (
    <div className="rounded-md border border-line">
      <div className="flex flex-wrap items-center gap-2 border-b border-line bg-raised/50 px-3 py-1.5 text-xs">
        <Link
          to={`/flows/${run.flow_id}`}
          className="font-medium hover:underline"
        >
          {run.flow_id}
        </Link>
        <StatusBadge status={run.status} />
        {run.kind !== "live" && <Badge>{run.kind}</Badge>}
        {run.dry_run && <Badge tone="warn">dry run</Badge>}
        <Time ts={run.created_at} />
        {run.started_at && (
          <span className="text-muted">
            {durationBetween(run.started_at, run.ended_at)}
          </span>
        )}
        {run.replay_of && (
          <span className="text-muted">
            replay of{" "}
            <Link
              to={`/runs/${run.replay_of}`}
              className="font-mono text-accent hover:underline"
            >
              {run.replay_of}
            </Link>
          </span>
        )}
        <Link
          to={`/runs/${run.id}`}
          className="ml-auto text-accent hover:underline"
        >
          Open run
        </Link>
      </div>
      {run.error && (
        <div className="border-b border-line px-3 py-1.5 text-xs text-bad">
          {run.error}
        </div>
      )}
      {steps.length === 0 ? (
        <div className="px-3 py-2 text-xs text-muted">
          No node runs yet ({run.status}).
        </div>
      ) : (
        <div className="space-y-1.5 px-3 py-2">
          <div className="flex flex-wrap gap-1.5">
            {counts.map(([k, n]) => (
              <Badge key={k}>
                {k} {n}
              </Badge>
            ))}
          </div>
          <div
            className="truncate font-mono text-[11px] text-muted"
            title={decisionPathText(steps)}
          >
            {decisionPathText(steps)}
          </div>
          <ul className="m-0 list-none p-0">
            {steps.map((s) => (
              <li
                key={s.nodeRunId}
                className="flex items-center gap-2 py-0.5 text-xs"
                style={{ paddingLeft: `${s.depth * 1.25}rem` }}
              >
                <span className="text-muted">{s.depth > 0 ? "└" : "•"}</span>
                <span className="font-medium">{s.nodeId}</span>
                <Badge>{s.nodeType}</Badge>
                <StatusBadge status={s.decision ?? s.status} />
                {s.ports.length > 0 && (
                  <span className="text-muted">port {s.ports.join(", ")}</span>
                )}
                {s.reason && (
                  <span className="truncate text-muted" title={s.reason}>
                    {s.reason}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
