import { useState } from "react";
import { useNavigate } from "react-router";
import { errorMessage } from "../../api/client";
import { useFlowOptions, useReplayEvent } from "../../api/events";
import { Button, Field, Select, Toggle } from "../../components/ui";
import { Modal } from "./Modal";

export function ReplayDialog({
  eventId,
  onClose,
}: {
  eventId: string;
  onClose: () => void;
}) {
  const navigate = useNavigate();
  const flows = useFlowOptions();
  const replay = useReplayEvent(eventId);
  const [flow, setFlow] = useState("");
  // `undefined` keeps the flow's own dry_run setting.
  const [dryRun, setDryRun] = useState(false);
  const [overrideDryRun, setOverrideDryRun] = useState(false);

  const options = flows.data?.filter((f) => f.valid) ?? [];

  const submit = () => {
    replay.mutate(
      { flow, ...(overrideDryRun ? { dry_run: dryRun } : {}) },
      { onSuccess: (run) => void navigate(`/runs/${run.id}`) },
    );
  };

  return (
    <Modal
      title="Replay into flow"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            icon="play"
            disabled={!flow || replay.isPending}
            onClick={submit}
          >
            {replay.isPending ? "Replaying…" : "Replay"}
          </Button>
        </>
      }
    >
      <p className="text-xs text-muted">
        Runs the stored event through the current version of the flow and
        creates a new replay run.
      </p>
      <Field label="Flow">
        {flows.isError ? (
          <span className="text-xs text-bad">{errorMessage(flows.error)}</span>
        ) : (
          <Select
            value={flow}
            disabled={flows.isPending}
            onChange={(e) => setFlow(e.target.value)}
          >
            <option value="">
              {flows.isPending
                ? "Loading…"
                : options.length === 0
                  ? "No valid flows"
                  : "Choose a flow"}
            </option>
            {options.map((f) => (
              <option key={f.id} value={f.id}>
                {f.id}
                {f.enabled === false ? " (disabled)" : ""}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <div className="space-y-1.5">
        <Toggle
          checked={overrideDryRun}
          onChange={setOverrideDryRun}
          label="Override the flow's dry-run setting"
        />
        {overrideDryRun && (
          <div className="pl-9">
            <Toggle checked={dryRun} onChange={setDryRun} label="Dry run" />
          </div>
        )}
      </div>
      {replay.isError && (
        <div role="alert" className="text-xs text-bad">
          {errorMessage(replay.error)}
        </div>
      )}
    </Modal>
  );
}
