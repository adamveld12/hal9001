import { usePauseSensor, useResumeSensor } from "../../api/sensors";
import { Button } from "../../components/ui";
import { pauseErrorMessage } from "./view";

/**
 * Pauses or resumes the scheduled polls of one sensor. Only for a sensor that
 * is paused on its own: the kill switch is released from the header.
 */
export function PauseToggle({
  sensorId,
  paused,
  disabled,
}: {
  sensorId: string;
  paused: boolean;
  disabled?: boolean;
}) {
  const pause = usePauseSensor();
  const resume = useResumeSensor();
  const action = paused ? resume : pause;
  return (
    <span className="inline-flex flex-col items-start gap-1">
      <Button
        icon={paused ? "play" : "pause"}
        disabled={disabled || action.isPending}
        title={
          paused
            ? "Resume scheduled polls of this sensor"
            : "Stop scheduled polls of this sensor. Poll now still works."
        }
        onClick={() => action.mutate(sensorId)}
      >
        {paused ? "Resume" : "Pause"}
      </Button>
      {action.isError && (
        <span role="alert" className="text-xs text-bad">
          {pauseErrorMessage(action.error, paused)}
        </span>
      )}
    </span>
  );
}
