import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { AssistDraft } from "../../api/assist";
import { errorMessage } from "../../api/client";
import { useAssist } from "./context";
import {
  describeSensorSaveFailure,
  saveSensorDraft,
  type SaveFailure,
} from "./flowSave";
import {
  invalidateSensorQueries,
  sensorSaveApi,
  useDraftRefresh,
} from "./useDraftRefresh";

/**
 * Save of one draft sensor of a flow session, alone: the card's "Save sensor".
 * It makes the same calls as the combined Save does for that sensor (create when
 * new, replace with `expected_hash` = the draft's `base_hash` otherwise) and
 * writes neither the flow nor anything else. Afterwards the draft is pushed
 * unchanged so the daemon re-checks it: the sensor then exists with its saved
 * hash as base, and the combined Save will not create it again. `run(true)`
 * overwrites a file that changed on disk since the draft copied it.
 */
export function useSensorSave(
  sessionId: string,
  sensor: Pick<
    AssistDraft["sensors"][number],
    "id" | "yaml" | "exists" | "base_hash"
  >,
) {
  const assist = useAssist();
  const qc = useQueryClient();
  const refreshDraft = useDraftRefresh(sessionId);
  const [isPending, setPending] = useState(false);
  const [failure, setFailure] = useState<SaveFailure | null>(null);
  /** The YAML that was last saved from this card, to tell that the card still shows it. */
  const [savedYaml, setSavedYaml] = useState<string | null>(null);

  /** Resolves true when the sensor was saved. */
  const run = async (overwrite: boolean): Promise<boolean> => {
    setPending(true);
    setFailure(null);
    try {
      // The cached draft must be the latest before it is pushed back.
      await assist.editor()?.flush();
      const r = await saveSensorDraft(sensorSaveApi, sensor, { overwrite });
      if (!r.ok) {
        setFailure(describeSensorSaveFailure(sensor.id, r));
        return false;
      }
      invalidateSensorQueries(qc);
      setSavedYaml(sensor.yaml);
      await refreshDraft();
      return true;
    } catch (e) {
      setFailure({
        message: errorMessage(e),
        diagnostics: [],
        conflict: false,
      });
      return false;
    } finally {
      setPending(false);
    }
  };

  return {
    run,
    isPending,
    failure,
    /** The card shows what was saved from it. */
    saved: savedYaml !== null && savedYaml === sensor.yaml,
  };
}
