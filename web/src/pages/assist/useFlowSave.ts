import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { usePutAssistDraft, type AssistSessionDetail } from "../../api/assist";
import { api, errorMessage, unwrap } from "../../api/client";
import {
  createFlowFromYaml,
  fetchFlow,
  updateFlowFromYaml,
  type FlowDetail,
} from "../../api/flows";
import { qk } from "../../api/keys";
import {
  createSensorFromYaml,
  fetchSensor,
  updateSensorFromYaml,
} from "../../api/sensors";
import { useAssist } from "./context";
import {
  describeSaveFailure,
  saveFlowDraft,
  type FlowSaveApi,
  type SaveFailure,
} from "./flowSave";

/**
 * Save of the open flow session's draft: its sensors, its new prompts, then the
 * flow (see flowSave.ts). A new flow (the drawer's target id null) is created and its
 * editor opens with the session attached; an existing flow is replaced when its
 * file still has the hash it had when the session began, and an edited sensor
 * when its file still has the hash the draft copied it at. `run(true)`
 * overwrites newer files instead.
 */
export function useFlowSave(sessionId: string) {
  const assist = useAssist();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const put = usePutAssistDraft(sessionId);
  const { id: targetId } = assist.target;

  // The file's hash when the session began (or was last saved), for conflict detection.
  const [baseHash, setBaseHash] = useState<string | null>(() =>
    targetId === null
      ? null
      : (qc.getQueryData<FlowDetail>(qk("flow", targetId))?.hash ?? null),
  );
  const [isPending, setPending] = useState(false);
  const [failure, setFailure] = useState<SaveFailure | null>(null);

  const saveApi: FlowSaveApi = {
    createSensor: createSensorFromYaml,
    updateSensor: updateSensorFromYaml,
    currentSensorHash: async (id) => (await fetchSensor(id)).hash,
    createPrompt: (markdown) =>
      unwrap(api.POST("/api/prompts", { body: { markdown } })),
    createFlow: async (yaml) => {
      const d = await createFlowFromYaml(yaml);
      qc.setQueryData(qk("flow", d.id), d);
      return d;
    },
    updateFlow: async (id, expectedHash, yaml) => {
      const d = await updateFlowFromYaml(id, expectedHash, yaml);
      qc.setQueryData(qk("flow", d.id), d);
      return d;
    },
  };

  const fail = (message: string): false => {
    setFailure({ message, diagnostics: [], conflict: false });
    return false;
  };

  /** Resolves true when the flow was saved. */
  const run = async (overwrite: boolean): Promise<boolean> => {
    setPending(true);
    setFailure(null);
    try {
      await assist.editor()?.flush();
      if (assist.editor()?.undecided?.() === true) {
        return fail(
          "An AI update is waiting on the canvas. Choose Take AI version or Keep mine first.",
        );
      }
      const draft = qc.getQueryData<AssistSessionDetail>(
        qk("assist-session", sessionId),
      )?.draft;
      if (!draft) return fail("The session has no draft.");

      let target;
      if (targetId === null) {
        target = { mode: "create" } as const;
      } else {
        let expectedHash = baseHash;
        if (overwrite || expectedHash === null) {
          expectedHash = (await fetchFlow(targetId)).hash;
        }
        target = { mode: "update", id: targetId, expectedHash } as const;
      }

      const result = await saveFlowDraft(saveApi, draft, target, {
        overwrite,
      });
      const wroteSensors =
        result.createdSensors.length + result.updatedSensors.length > 0;
      if (wroteSensors) {
        for (const root of ["sensors", "sensor"] as const) {
          void qc.invalidateQueries({ queryKey: qk(root) });
        }
      }
      if (result.createdPrompts.length > 0) {
        for (const root of ["prompts", "prompt"] as const) {
          void qc.invalidateQueries({ queryKey: qk(root) });
        }
      }
      if (wroteSensors || result.createdPrompts.length > 0) {
        // Pushing the draft unchanged makes the daemon re-check the sensors, the prompts and the diagnostics.
        await put
          .mutateAsync({ yaml: draft.yaml, expected_hash: draft.hash })
          .catch(() => undefined);
      }
      if (!result.ok) {
        setFailure(describeSaveFailure(result));
        return false;
      }
      setBaseHash(result.flow.hash);
      void qc.invalidateQueries({ queryKey: qk("flows") });
      void qc.invalidateQueries({ queryKey: qk("flow-versions") });
      if (targetId === null) {
        // The session stays attached: the drawer's target moves to the saved flow.
        assist.retarget({ kind: "flow", id: result.flow.id });
        void navigate(`/flows/${result.flow.id}`);
      }
      return true;
    } catch (e) {
      return fail(errorMessage(e));
    } finally {
      setPending(false);
    }
  };

  return { run, isPending, failure };
}
