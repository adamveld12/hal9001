import { Link } from "react-router";
import { useResetSensor, type PollResult } from "../../api/sensors";
import { SensorStdout } from "../../components/SensorStdout";
import { Button, StatusBadge } from "../../components/ui";
import {
  RESET_ACTIONS,
  RESET_EXPLANATION,
  resetMessage,
  runSummary,
  type ResetMode,
} from "../../lib/runNow";
import { Modal } from "../timeline/Modal";
import { isFailedRun, resetErrorMessage } from "./view";

/** What the page shows after Run now or a reset. */
export interface RunNowOutcome {
  /** "Reset: N dedup keys cleared", for a reset. */
  message?: string;
  run: PollResult | null;
}

/** Summary line, links to the created flow runs, and the finished run's output. */
export function RunNowResult({
  outcome,
  onClose,
}: {
  outcome: RunNowOutcome;
  onClose: () => void;
}) {
  const { run, message } = outcome;
  return (
    <section
      aria-label="Run now result"
      className="space-y-2 rounded-md border border-line bg-panel p-3 text-xs"
    >
      <div className="flex flex-wrap items-center gap-2">
        {message && <span className="font-medium text-ok">{message}</span>}
        {run && (
          <>
            <StatusBadge status={run.status} />
            <span className="font-medium">{runSummary(run)}</span>
          </>
        )}
        <Button variant="ghost" className="ml-auto" onClick={onClose}>
          Dismiss
        </Button>
      </div>
      {run && (
        <>
          {isFailedRun(run) && (
            <div role="alert" className="text-bad">
              {run.error ?? run.status}
            </div>
          )}
          {run.flow_run_ids.length > 0 && (
            <ul className="m-0 flex list-none flex-wrap gap-x-3 gap-y-1 p-0">
              {run.flow_run_ids.map((id) => (
                <li key={id}>
                  <Link
                    to={`/runs/${id}`}
                    className="font-mono text-accent hover:underline"
                  >
                    {id}
                  </Link>
                </li>
              ))}
            </ul>
          )}
          <div className="space-y-1">
            <div className="text-[11px] font-medium text-muted">
              stdout (items)
            </div>
            <SensorStdout blob={run.stdout} />
          </div>
        </>
      )}
    </section>
  );
}

/** Confirmation for "Reset dedup state": Reset, or Reset and run now. */
export function ResetDialog({
  sensorId,
  onClose,
  onDone,
}: {
  sensorId: string;
  onClose: () => void;
  onDone: (outcome: RunNowOutcome) => void;
}) {
  const reset = useResetSensor();
  const go = (mode: ResetMode) =>
    reset.mutate(
      { id: sensorId, run: RESET_ACTIONS[mode].body.run },
      {
        onSuccess: (r) => {
          onDone({ message: resetMessage(r.keys_cleared), run: r.run });
          onClose();
        },
      },
    );
  return (
    <Modal
      title={`Reset dedup state of ${sensorId}`}
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            disabled={reset.isPending}
            onClick={() => go("reset")}
          >
            {RESET_ACTIONS.reset.label}
          </Button>
          <Button
            variant="danger"
            icon="play"
            disabled={reset.isPending}
            onClick={() => go("reset-and-run")}
          >
            {RESET_ACTIONS["reset-and-run"].label}
          </Button>
        </>
      }
    >
      <p className="m-0 text-xs">{RESET_EXPLANATION}</p>
      {reset.isPending && <p className="m-0 text-xs text-muted">Working…</p>}
      {reset.isError && (
        <div role="alert" className="text-xs text-bad">
          {resetErrorMessage(reset.error)}
        </div>
      )}
    </Modal>
  );
}
