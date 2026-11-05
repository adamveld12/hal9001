import type { InboxQuery, InboxSort, Priority } from "../../api/inbox";
import { PRIORITIES } from "../../api/inbox";

export type DryRunFilter = "any" | "hide" | "only";

/** Inbox list filters as the user sees them; the URL search params are the source of truth. */
export interface InboxFilters {
  unread: boolean;
  starred: boolean;
  /** Show only archived items. */
  archived: boolean;
  /** Minimum priority. */
  priority: Priority | null;
  dryRun: DryRunFilter;
  q: string;
  sort: InboxSort;
}

export const DEFAULT_FILTERS: InboxFilters = {
  unread: false,
  starred: false,
  archived: false,
  priority: null,
  dryRun: "any",
  q: "",
  sort: "priority",
};

export function filtersFromParams(p: URLSearchParams): InboxFilters {
  const priority = p.get("priority");
  const dry = p.get("dry");
  return {
    unread: p.get("unread") === "1",
    starred: p.get("starred") === "1",
    archived: p.get("archived") === "1",
    priority: PRIORITIES.find((x) => x === priority) ?? null,
    dryRun: dry === "only" || dry === "hide" ? dry : "any",
    q: p.get("q") ?? "",
    sort: p.get("sort") === "time" ? "time" : "priority",
  };
}

/** Only non-default values are written, so the default view has a clean URL. */
export function filtersToParams(f: InboxFilters): URLSearchParams {
  const p = new URLSearchParams();
  if (f.unread) p.set("unread", "1");
  if (f.starred) p.set("starred", "1");
  if (f.archived) p.set("archived", "1");
  if (f.priority) p.set("priority", f.priority);
  if (f.dryRun !== "any") p.set("dry", f.dryRun);
  if (f.q.trim()) p.set("q", f.q.trim());
  if (f.sort !== DEFAULT_FILTERS.sort) p.set("sort", f.sort);
  return p;
}

/** Query parameters for GET /api/inbox (sort is passed separately). */
export function filtersToQuery(f: InboxFilters): InboxQuery {
  const q: InboxQuery = {};
  if (f.unread) q.read = false;
  if (f.starred) q.starred = true;
  q.archived = f.archived ? "true" : "false";
  if (f.priority) q.priority = f.priority;
  if (f.dryRun !== "any") q.dry_run = f.dryRun === "only";
  if (f.q.trim()) q.q = f.q.trim();
  return q;
}

/** Number of filters that differ from the default view (sort excluded). */
export function activeFilterCount(f: InboxFilters): number {
  return [
    f.unread,
    f.starred,
    f.archived,
    f.priority !== null,
    f.dryRun !== "any",
    f.q.trim() !== "",
  ].filter(Boolean).length;
}
