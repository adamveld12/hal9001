import type { InboxBulkFilter } from "../../api/inbox";
import type { InboxFilters } from "./filters";

/** The `filter` of a bulk request for exactly what the list currently shows. */
export function filtersToBulkFilter(f: InboxFilters): InboxBulkFilter {
  const out: InboxBulkFilter = { archived: f.archived ? "true" : "false" };
  if (f.unread) out.read = false;
  if (f.starred) out.starred = true;
  if (f.priority) out.priority = f.priority;
  if (f.dryRun !== "any") out.dry_run = f.dryRun === "only";
  if (f.q.trim()) out.q = f.q.trim();
  return out;
}

export type ClearPresetId = "read" | "dry-run" | "low" | "older-7d" | "shown";

export interface ClearPreset {
  id: ClearPresetId;
  label: string;
  /** What the confirmation says is archived. */
  what: string;
}

export const CLEAR_PRESETS: readonly ClearPreset[] = [
  { id: "read", label: "Archive everything read", what: "read items" },
  { id: "dry-run", label: "Archive all dry-run items", what: "dry-run items" },
  { id: "low", label: "Archive low priority", what: "low priority items" },
  {
    id: "older-7d",
    label: "Archive older than 7 days",
    what: "items older than 7 days",
  },
  {
    id: "shown",
    label: "Archive everything shown",
    what: "items matching the current filters",
  },
];

/**
 * Bulk filter of a preset. Presets cover unarchived items only; "shown" uses
 * the list's filters, so it covers every match, not just the loaded rows.
 */
export function presetFilter(
  id: ClearPresetId,
  shown: InboxFilters,
): InboxBulkFilter {
  switch (id) {
    case "read":
      return { read: true, archived: "false" };
    case "dry-run":
      return { dry_run: true, archived: "false" };
    case "low":
      return { max_priority: "low", archived: "false" };
    case "older-7d":
      return { older_than: "7d", archived: "false" };
    case "shown":
      return filtersToBulkFilter(shown);
  }
}

/** Confirmation text for a preset from a dry count. */
export function confirmText(
  preset: ClearPreset,
  changed: number,
  skippedStarred: number,
): string {
  const n = `${changed} ${changed === 1 ? "item" : "items"}`;
  const head =
    changed === 0
      ? `No ${preset.what} to archive.`
      : `Archive ${n} (${preset.what})? Archived items stay linked to their runs and can be restored.`;
  const kept =
    skippedStarred > 0
      ? ` ${skippedStarred} starred ${skippedStarred === 1 ? "item is" : "items are"} kept.`
      : " Starred items are kept.";
  return head + kept;
}

/** Selection helpers: the selection is a set of item ids. */

export function toggleSelected(
  selected: ReadonlySet<string>,
  id: string,
): Set<string> {
  const next = new Set(selected);
  if (!next.delete(id)) next.add(id);
  return next;
}

export type PageSelection = "none" | "some" | "all";

export function pageSelection(
  selected: ReadonlySet<string>,
  pageIds: readonly string[],
): PageSelection {
  const n = pageIds.filter((id) => selected.has(id)).length;
  return n === 0 ? "none" : n === pageIds.length ? "all" : "some";
}

/** Select-all checkbox: selects every row on the page, or clears them when all were selected. */
export function toggleAllOnPage(
  selected: ReadonlySet<string>,
  pageIds: readonly string[],
): Set<string> {
  const next = new Set(selected);
  if (pageSelection(selected, pageIds) === "all") {
    for (const id of pageIds) next.delete(id);
  } else {
    for (const id of pageIds) next.add(id);
  }
  return next;
}

/** Drop ids that left the list (archived, filtered out), keeping the same set when nothing changed. */
export function pruneSelection(
  selected: ReadonlySet<string>,
  visibleIds: readonly string[],
): ReadonlySet<string> {
  const visible = new Set(visibleIds);
  let kept = 0;
  for (const id of selected) if (visible.has(id)) kept++;
  if (kept === selected.size) return selected;
  return new Set([...selected].filter((id) => visible.has(id)));
}
