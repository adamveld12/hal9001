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
import { useAssistPage } from "./context";
import {
  describeSaveFailure,
  saveFlowDraft,
  type FlowSaveApi,
  type SaveFailure,
} from "./flowSave";
import { targetConfig } from "./targets";

/**
 * Save of the open flow session's draft. A new flow (`page.targetId` null) is
 * created and its editor opens with the session attached; an existing flow is
 * replaced when its file still has the hash it had when the session began, or
 * after `run(true)` when the user chose to overwrite a newer file.
 */
export function useFlowSave(sessionId: string) {
  const page = useAssistPage();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const put = usePutAssistDraft(sessionId);
  const { targetId } = page;

  // The file's hash when the session began (or was last saved), for conflict detection.
  const [baseHash, setBaseHash] = useState<string | null>(() =>
    targetId === null
      ? null
      : (qc.getQueryData<FlowDetail>(qk("flow", targetId))?.hash ?? null),
  );
  const [isPending, setPending] = useState(false);
  const [failure, setFailure] = useState<SaveFailure | null>(null);

  const saveApi: FlowSaveApi = {
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
      await page.editor()?.flush();
      if (page.editor()?.undecided?.() === true) {
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

      const result = await saveFlowDraft(saveApi, draft, target);
      if (result.createdPrompts.length > 0) {
        for (const root of ["prompts", "prompt"] as const) {
          void qc.invalidateQueries({ queryKey: qk(root) });
        }
        // Pushing the draft unchanged makes the daemon re-check the prompts and diagnostics.
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
        void navigate(
          targetConfig("flow").editorPath(result.flow.id, sessionId),
        );
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
