import { useCallback, useMemo, useState } from "react";
import { useAssistTestResult } from "../../api/assist";
import { usePromptNames } from "../../api/prompts";
import { useSensorIds } from "../../api/sensors";
import { useRuntimeNames } from "../../api/settings";
import { LoadingState } from "../../components/ui";
import { FlowEditor } from "../../flow/FlowEditor";
import type { Flow } from "../../flow/model";
import { DraftBanners } from "../assist/DraftBanners";
import { overlayFromTestResult } from "../assist/flowTest";
import { canvasDiagnostics, findingLabel } from "../assist/sensorDrafts";
import { useFlowDraftSync } from "../assist/useFlowDraftSync";

const NO_SAVE = () => Promise.reject(new Error("saved from the assistant"));

/**
 * The canvas while an assist session is open: it shows the session's draft,
 * follows the agent's updates, and writes the user's edits back to the draft.
 * The flow is saved from the drawer, so the editor has no Save of its own.
 */
export function FlowDraftView({ sessionId }: { sessionId: string }) {
  const [editorFlow, setEditorFlow] = useState<Flow | null>(null);
  const onChange = useCallback((flow: Flow) => setEditorFlow(flow), []);
  const sync = useFlowDraftSync({ sessionId, editorFlow });
  const sensors = useSensorIds();
  const prompts = usePromptNames();
  const runtimes = useRuntimeNames();
  const test = useAssistTestResult(sessionId);

  const testResult = test.data;
  const overlay = useMemo(
    () =>
      testResult?.kind === "flow" && editorFlow
        ? overlayFromTestResult(editorFlow, testResult)
        : undefined,
    [testResult, editorFlow],
  );
  const external = useMemo(
    () => (sync.shown ? { flow: sync.shown, rev: sync.rev } : undefined),
    [sync.shown, sync.rev],
  );

  const draft = sync.draft;
  // Prompts the agent drafted are valid choices before Save creates them.
  const promptNames = useMemo(
    () => [
      ...new Set([
        ...(prompts.data ?? []),
        ...(draft?.prompts ?? []).map((p) => p.name),
      ]),
    ],
    [prompts.data, draft?.prompts],
  );
  // Sensors the agent drafted are valid choices before Save creates them.
  const sensorIds = useMemo(
    () => [
      ...new Set([
        ...(sensors.data ?? []),
        ...(draft?.sensors ?? []).map((s) => s.id),
      ]),
    ],
    [sensors.data, draft?.sensors],
  );
  const diagnostics = useMemo(
    () => (draft ? canvasDiagnostics(draft.diagnostics) : undefined),
    [draft],
  );
  const newPrompts = (draft?.prompts ?? []).filter((p) => !p.exists);
  const parseErrors = (draft?.diagnostics ?? []).filter(
    (d) => d.severity === "error",
  );

  const banners = (
    <>
      <DraftBanners
        pending={sync.pending}
        markedLines={[]}
        onTakeAi={sync.takeAi}
        onKeepMine={sync.keepMine}
        onClearMarks={() => undefined}
      />
      {sync.invalid && (
        <div
          role="alert"
          className="space-y-1 rounded-md border border-bad/40 bg-bad/5 px-3 py-2 text-xs text-bad"
        >
          <div className="font-medium">
            The draft YAML does not parse, so the canvas keeps the last valid
            version.
          </div>
          {parseErrors.length > 0 && (
            <ul className="m-0 list-disc space-y-0.5 pl-4">
              {parseErrors.map((d) => (
                <li
                  key={`${d.file ?? ""}|${d.line ?? ""}|${d.path ?? ""}|${d.message}`}
                >
                  {findingLabel(d)}
                  {d.line ? `line ${d.line}: ` : ""}
                  {d.message}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {sync.pushError && (
        <div role="alert" className="text-xs text-bad">
          Your edit could not be written to the draft: {sync.pushError}
        </div>
      )}
      {newPrompts.length > 0 && (
        <div role="status" className="text-xs text-muted">
          New prompt{newPrompts.length === 1 ? "" : "s"} in the draft:{" "}
          {newPrompts.map((p) => p.name).join(", ")}. Save creates them.
        </div>
      )}
    </>
  );

  if (sync.shown === null) {
    return (
      <div className="space-y-3 p-4">
        {sync.invalid ? banners : <LoadingState label="Loading draft" />}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="space-y-2 px-3 empty:hidden [&:not(:empty)]:py-2">
        {banners}
      </div>
      <div className="min-h-0 flex-1">
        <FlowEditor
          key={sessionId}
          flow={sync.shown}
          external={external}
          overlay={overlay}
          saveHidden
          sensors={sensorIds}
          prompts={promptNames}
          runtimes={runtimes.data ?? []}
          serverDiagnostics={diagnostics}
          onSave={NO_SAVE}
          onChange={onChange}
        />
      </div>
    </div>
  );
}
