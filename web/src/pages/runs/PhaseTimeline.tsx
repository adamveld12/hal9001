import { errorMessage } from "../../api/client";
import { usePhaseRuns } from "../../api/runs";
import { Time } from "../../components/Time";
import { Badge, Spinner, StatusBadge } from "../../components/ui";
import { cx } from "../../lib/cx";
import { useNow } from "../approvals/timeLeft";
import { durationBetween, shortId } from "../../lib/format";
import { Mono, TextLink } from "./Meta";
import { buildPhaseTimeline } from "./timeline";

/** propose -> approval -> execute for the node run this agent run belongs to. */
export function PhaseTimeline({
  phaseRunIds,
  currentRunId,
}: {
  phaseRunIds: readonly string[];
  currentRunId: string;
}) {
  const phases = usePhaseRuns(phaseRunIds);
  const now = useNow();
  if (phases.pending) return <Spinner label="Loading phases" />;
  if (phases.error) {
    return (
      <div role="alert" className="text-xs text-bad">
        Phases unavailable: {errorMessage(phases.error)}
      </div>
    );
  }
  const steps = buildPhaseTimeline(
    phases.runs.map((r) => ({ run: r, approvals: r.approvals })),
    currentRunId,
  );
  return (
    <ol className="m-0 list-none space-y-2 p-0 text-xs">
      {steps.map((s) =>
        s.kind === "run" ? (
          <li
            key={s.runId}
            className={cx(
              "flex flex-wrap items-center gap-2 rounded-md border px-2 py-1.5",
              s.current ? "border-accent" : "border-line",
            )}
          >
            <span className="font-medium">{s.phase}</span>
            <StatusBadge status={s.status} />
            {s.resumedVia && (
              <Badge tone="info">
                resumed via {s.resumedVia.replace("_", " ")}
              </Badge>
            )}
            <span className="ml-auto text-muted">
              {durationBetween(s.startedAt, s.endedAt, now)}
            </span>
            {s.resumeFallback && (
              <div className="w-full text-muted">
                session/load not used: {s.resumeFallback}
              </div>
            )}
            <div className="flex w-full items-center gap-2 text-muted">
              {s.current ? (
                <span>this run</span>
              ) : (
                <TextLink to={`/agent-runs/${s.runId}`}>
                  <Mono>{shortId(s.runId)}</Mono>
                </TextLink>
              )}
              <Time ts={s.startedAt} />
            </div>
          </li>
        ) : (
          <li
            key={s.approvalId}
            className="flex flex-wrap items-center gap-2 px-2 text-muted"
          >
            <span aria-hidden="true">↳</span>
            <TextLink to={`/approvals/${s.approvalId}`}>
              proposal approval
            </TextLink>
            <StatusBadge status={s.status} />
            {s.decidedBy && <span>by {s.decidedBy}</span>}
            <Time ts={s.decidedAt ?? s.createdAt} />
          </li>
        ),
      )}
    </ol>
  );
}
