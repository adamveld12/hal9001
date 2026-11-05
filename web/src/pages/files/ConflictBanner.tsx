import { Button } from "../../components/ui";
import type { Conflict } from "./editState";

/** Offered when the file on disk differs from the version the draft started from. */
export function ConflictBanner({
  conflict,
  noun,
  busy,
  onReload,
  onOverwrite,
}: {
  conflict: Conflict;
  /** What the file is, for the message: "sensor", "prompt". */
  noun: string;
  busy: boolean;
  onReload: () => void;
  onOverwrite: () => void;
}) {
  const gone = conflict.currentHash === null && conflict.kind === "save";
  return (
    <div
      role="alert"
      className="space-y-2 rounded-md border border-warn/40 bg-warn/5 p-3 text-xs"
    >
      <div className="font-medium text-warn">
        {conflict.kind === "save"
          ? `Not saved: this ${noun} changed on disk after you opened it.`
          : `This ${noun} changed on disk while you were editing.`}
      </div>
      <div className="text-muted">
        {gone
          ? "The file may have been deleted. Reload to check, or overwrite to write your version."
          : "Reload to take the disk version and drop your edits, or overwrite the disk version with your edits."}
      </div>
      <div className="flex gap-2">
        <Button onClick={onReload} disabled={busy} icon="refresh">
          Reload from disk
        </Button>
        <Button variant="danger" onClick={onOverwrite} disabled={busy}>
          Overwrite with my edits
        </Button>
      </div>
    </div>
  );
}
