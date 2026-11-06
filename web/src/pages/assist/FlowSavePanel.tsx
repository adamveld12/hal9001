import { useState } from "react";
import { useAssistSession } from "../../api/assist";
import { Button } from "../../components/ui";
import { hasErrors } from "../files/diagnostics";
import { isBusy } from "./sessions";
import { useFlowSave } from "./useFlowSave";

/**
 * "Save flow" in the drawer: writes the prompts the agent drafted that do not
 * exist yet, then the flow file. A new flow opens in its editor with this
 * session still attached. Disabled while the draft has errors or the agent is
 * working on it.
 */
export function FlowSavePanel({ sessionId }: { sessionId: string }) {
  const session = useAssistSession(sessionId).data;
  const save = useFlowSave(sessionId);
  const [notice, setNotice] = useState<string | null>(null);
  const draft = session?.draft;
  const newPrompts = (draft?.prompts ?? []).filter((p) => !p.exists);

  if (!session || !draft) return null;
  const errors = hasErrors(draft.diagnostics);
  const busy = isBusy(session.status);
  const blocker = errors
    ? "Fix the errors in the draft first."
    : busy
      ? "Wait for the agent to finish its turn."
      : null;

  const run = async (overwrite: boolean) => {
    setNotice(null);
    const ok = await save.run(overwrite);
    if (ok) setNotice("Saved.");
  };

  return (
    <div className="space-y-2 border-t border-line px-3 py-2 text-xs">
      <div className="flex items-center gap-2">
        <Button
          variant="primary"
          icon="save"
          className="shrink-0 whitespace-nowrap"
          disabled={blocker !== null || save.isPending}
          title={blocker ?? "Write the draft to flows/"}
          onClick={() => void run(false)}
        >
          {save.isPending ? "Saving…" : "Save flow"}
        </Button>
        <span className="text-[11px] text-muted">
          {blocker ??
            (newPrompts.length > 0
              ? `Creates prompt${newPrompts.length === 1 ? "" : "s"} ${newPrompts.map((p) => p.name).join(", ")}, then the flow.`
              : "The flow stays disabled until you enable it.")}
        </span>
      </div>
      {notice !== null && !save.failure && (
        <div role="status" className="text-ok">
          {notice}
        </div>
      )}
      {save.failure && (
        <div role="alert" className="space-y-1 text-bad">
          <div>{save.failure.message}</div>
          {save.failure.diagnostics.length > 0 && (
            <ul className="m-0 list-disc space-y-0.5 pl-4">
              {save.failure.diagnostics.map((d) => (
                <li
                  key={`${d.severity}|${d.node_id ?? ""}|${d.path ?? ""}|${d.message}`}
                >
                  {d.line ? `line ${d.line}: ` : ""}
                  {d.message}
                </li>
              ))}
            </ul>
          )}
          {save.failure.conflict && (
            <Button disabled={save.isPending} onClick={() => void run(true)}>
              Overwrite
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
