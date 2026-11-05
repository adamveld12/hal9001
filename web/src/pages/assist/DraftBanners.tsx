import type { AssistDraft } from "../../api/assist";
import { Button } from "../../components/ui";

/**
 * Above the editor while a session is open: an agent update that waits for the
 * user's decision, and a note on the lines the last AI change touched.
 */
export function DraftBanners({
  pending,
  markedLines,
  onTakeAi,
  onKeepMine,
  onClearMarks,
}: {
  pending: AssistDraft | null;
  markedLines: readonly number[];
  onTakeAi: () => void;
  onKeepMine: () => void;
  onClearMarks: () => void;
}) {
  return (
    <>
      {pending && (
        <div
          role="alert"
          className="flex flex-wrap items-center gap-2 rounded-md border border-warn/40 bg-warn/5 px-3 py-2 text-xs"
        >
          <span className="min-w-0 flex-1 font-medium text-warn">
            AI updated the draft while you were typing.
          </span>
          <Button variant="primary" onClick={onTakeAi}>
            Take AI version
          </Button>
          <Button onClick={onKeepMine}>Keep mine</Button>
        </div>
      )}
      {markedLines.length > 0 && (
        <div className="flex items-center gap-2 text-xs text-muted">
          <span aria-hidden="true" className="h-3 w-1 rounded-sm bg-ok" />
          <span>
            Highlighted: {markedLines.length} line
            {markedLines.length === 1 ? "" : "s"} changed by the AI.
          </span>
          <button
            type="button"
            className="underline hover:text-fg"
            onClick={onClearMarks}
          >
            Clear
          </button>
        </div>
      )}
    </>
  );
}
