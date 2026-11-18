import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { usePutAssistDraft, type AssistSessionDetail } from "../../api/assist";
import { qk } from "../../api/keys";
import {
  createSensorFromYaml,
  fetchSensor,
  updateSensorFromYaml,
} from "../../api/sensors";
import type { FlowSaveApi } from "./flowSave";

/** The sensor calls of Save, shared by the combined Save and a card's Save sensor. */
export const sensorSaveApi: Pick<
  FlowSaveApi,
  "createSensor" | "updateSensor" | "currentSensorHash"
> = {
  createSensor: createSensorFromYaml,
  updateSensor: updateSensorFromYaml,
  currentSensorHash: async (id) => (await fetchSensor(id)).hash,
};

/** Makes the Sensors pages show sensors that were just written. */
export function invalidateSensorQueries(qc: QueryClient) {
  for (const root of ["sensors", "sensor"] as const) {
    void qc.invalidateQueries({ queryKey: qk(root) });
  }
}

/**
 * After files were saved: pushes the session's draft unchanged, so the daemon
 * re-checks the draft's sensors and prompts against what is saved now (a saved
 * sensor is no longer new, nor in conflict) and the session shows the result.
 * If the push fails (the draft moved meanwhile) the session is read again.
 */
export function useDraftRefresh(sessionId: string) {
  const qc = useQueryClient();
  const put = usePutAssistDraft(sessionId);
  return async () => {
    const draft = qc.getQueryData<AssistSessionDetail>(
      qk("assist-session", sessionId),
    )?.draft;
    if (!draft) return;
    await put
      .mutateAsync({ yaml: draft.yaml, expected_hash: draft.hash })
      .catch(() =>
        qc.invalidateQueries({ queryKey: qk("assist-session", sessionId) }),
      );
  };
}
