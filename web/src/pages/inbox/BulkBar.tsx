import { useEffect, useRef, useState } from "react";
import type { InboxBulkAction } from "../../api/inbox";
import { Button } from "../../components/ui";
import { CLEAR_PRESETS, type ClearPresetId, type PageSelection } from "./bulk";

/** Select-all checkbox, the actions for the selected rows and the "Clear…" menu. */
export function BulkBar({
  selection,
  count,
  archivedView,
  busy,
  onToggleAll,
  onAction,
  onDelete,
  onPreset,
}: {
  selection: PageSelection;
  count: number;
  archivedView: boolean;
  busy: boolean;
  onToggleAll: () => void;
  onAction: (action: InboxBulkAction) => void;
  onDelete: () => void;
  onPreset: (id: ClearPresetId) => void;
}) {
  const all = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (all.current) all.current.indeterminate = selection === "some";
  }, [selection]);
  const [menu, setMenu] = useState(false);
  const none = count === 0;
  return (
    <div
      role="toolbar"
      aria-label="Bulk actions"
      className="sticky top-0 z-20 flex flex-wrap items-center gap-2 border-b border-line bg-bg px-3 py-1.5 text-xs"
    >
      <label className="flex cursor-pointer items-center gap-2 text-muted">
        <input
          ref={all}
          type="checkbox"
          aria-label="Select all on this page"
          checked={selection === "all"}
          onChange={onToggleAll}
        />
        {none ? "Select all" : `${count} selected`}
      </label>
      {!none && (
        <>
          {archivedView ? (
            <Button disabled={busy} onClick={() => onAction("unarchive")}>
              Unarchive
            </Button>
          ) : (
            <Button disabled={busy} onClick={() => onAction("archive")}>
              Archive
            </Button>
          )}
          <Button disabled={busy} onClick={() => onAction("mark_read")}>
            Mark read
          </Button>
          <Button disabled={busy} onClick={() => onAction("mark_unread")}>
            Mark unread
          </Button>
          {archivedView && (
            <Button variant="danger" disabled={busy} onClick={onDelete}>
              Delete
            </Button>
          )}
        </>
      )}
      {!archivedView && (
        <div className="relative ml-auto">
          <Button
            aria-haspopup="menu"
            aria-expanded={menu}
            onClick={() => setMenu((m) => !m)}
          >
            Clear…
          </Button>
          {menu && (
            <div
              role="menu"
              className="absolute right-0 z-30 mt-1 w-56 rounded-md border border-line bg-panel py-1 shadow-lg"
            >
              {CLEAR_PRESETS.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  role="menuitem"
                  className="block w-full px-3 py-1.5 text-left text-xs hover:bg-raised"
                  onClick={() => {
                    setMenu(false);
                    onPreset(p.id);
                  }}
                >
                  {p.label}
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
