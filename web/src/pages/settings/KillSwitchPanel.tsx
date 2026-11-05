import { useState } from "react";
import { errorMessage } from "../../api/client";
import {
  usePauseKillSwitch,
  useResumeKillSwitch,
  useStatus,
} from "../../api/status";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  TextInput,
} from "../../components/ui";

export function KillSwitchPanel() {
  const status = useStatus();
  const pause = usePauseKillSwitch();
  const resume = useResumeKillSwitch();
  const [reason, setReason] = useState("");

  if (status.isPending) {
    return (
      <Panel title="Kill switch">
        <LoadingState />
      </Panel>
    );
  }
  if (status.isError) {
    return (
      <Panel title="Kill switch">
        <ErrorState
          error={status.error}
          onRetry={() => void status.refetch()}
        />
      </Panel>
    );
  }

  const ks = status.data.kill_switch;
  const error = pause.error ?? resume.error;
  const busy = pause.isPending || resume.isPending;

  return (
    <Panel
      title="Kill switch"
      actions={
        <Badge tone={ks.paused ? "warn" : "ok"}>
          {ks.paused ? "paused" : "running"}
        </Badge>
      }
    >
      {ks.paused ? (
        <div className="space-y-3">
          <p className="m-0 text-xs text-muted">
            Sensors and new nodes are stopped
            {ks.kill_running ? "; running processes were killed" : ""}.
            {ks.by ? ` Paused by ${ks.by}` : " Paused"} <Time ts={ks.since} />
            {ks.reason ? `: ${ks.reason}` : "."}
          </p>
          <Button
            variant="primary"
            icon="play"
            disabled={busy}
            onClick={() => resume.mutate()}
          >
            Resume
          </Button>
        </div>
      ) : (
        <div className="space-y-3">
          <Field label="Reason (optional)">
            <TextInput
              value={reason}
              maxLength={200}
              disabled={busy}
              onChange={(e) => setReason(e.target.value)}
            />
          </Field>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              disabled={busy}
              icon="stop"
              onClick={() =>
                pause.mutate({
                  kill_running: false,
                  ...(reason.trim() && { reason: reason.trim() }),
                })
              }
            >
              Pause
            </Button>
            <Button
              variant="danger"
              disabled={busy}
              icon="stop"
              onClick={() =>
                pause.mutate({
                  kill_running: true,
                  ...(reason.trim() && { reason: reason.trim() }),
                })
              }
            >
              Pause and kill running
            </Button>
            <span className="text-[11px] text-muted">
              Pause lets running work finish; the second option also sends
              SIGTERM to running processes.
            </span>
          </div>
        </div>
      )}
      {error && (
        <p role="alert" className="mb-0 mt-3 text-xs text-bad">
          {errorMessage(error)}
        </p>
      )}
    </Panel>
  );
}
