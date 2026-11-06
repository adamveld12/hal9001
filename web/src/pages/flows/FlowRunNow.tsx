import { useRunFlow, type FlowRunNowResult } from "../../api/flows";
import { useStatus } from "../../api/status";
import { RunNowButton } from "../../components/RunNowButton";
import { flowRerunKey, flowRunNowState } from "../../lib/runNow";
import { runFlowErrorMessage } from "../sensors/view";

/**
 * "Run now" of a saved flow: polls each sensor it subscribes to once and runs
 * the flow on the result. The remembered re-run option is per flow.
 */
export function FlowRunNow({
  flowId,
  enabled,
  subscribeCount,
  dirty = false,
  onResult,
}: {
  flowId: string;
  enabled: boolean;
  subscribeCount: number;
  /** Unsaved editor changes: Run now runs the saved version, so it waits for a save. */
  dirty?: boolean;
  onResult: (result: FlowRunNowResult, note: string | undefined) => void;
}) {
  const run = useRunFlow();
  const status = useStatus();
  const state = flowRunNowState({
    dirty,
    subscribeCount,
    enabled,
    killSwitch: status.data?.kill_switch.paused ?? false,
  });
  return (
    <span className="inline-flex flex-col items-end gap-1">
      <RunNowButton
        storageKey={flowRerunKey(flowId)}
        pending={run.isPending}
        disabled={state.disabled}
        title={state.reason ?? state.note ?? "Run the saved flow once"}
        onRun={(rerunSeen) =>
          run.mutate(
            { id: flowId, rerunSeen },
            { onSuccess: (r) => onResult(r, state.note) },
          )
        }
      />
      {run.isError && (
        <span role="alert" className="max-w-64 text-xs text-bad">
          {runFlowErrorMessage(run.error)}
        </span>
      )}
    </span>
  );
}
