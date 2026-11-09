import type { Approval } from "../../api/approvals";
import type { InboxItem } from "../../api/inbox";
import { approvalTitle, comparePending } from "../approvals/queue";
import type { InboxFilters } from "./filters";

/** One line of the Inbox list: a note or a pending approval. */
export type InboxRow =
  | { kind: "note"; key: string; item: InboxItem }
  | { kind: "approval"; key: string; approval: Approval };

export type Selection = { kind: "note" | "approval"; id: string };

export const noteKey = (id: string) => `n:${id}`;
export const approvalKey = (id: string) => `a:${id}`;

export function selectionKey(s: Selection): string {
  return s.kind === "approval" ? approvalKey(s.id) : noteKey(s.id);
}

export function selectionPath(s: Selection): string {
  return s.kind === "approval" ? `/inbox/approval/${s.id}` : `/inbox/${s.id}`;
}

/** Which item a URL path opens: `/inbox/<note>` or `/inbox/approval/<approval>`. */
export function selectionFromPath(pathname: string): Selection | null {
  const parts = pathname.split("/").filter(Boolean).map(decodeURIComponent);
  if (parts[0] !== "inbox") return null;
  if (parts.length === 2 && parts[1] !== "approval" && parts[1]) {
    return { kind: "note", id: parts[1] };
  }
  if (parts.length === 3 && parts[1] === "approval" && parts[2]) {
    return { kind: "approval", id: parts[2] };
  }
  return null;
}

/**
 * Whether a pending approval belongs in the list under `f`. Approvals are
 * never archived or starred, count as unread and urgent.
 */
export function approvalMatches(a: Approval, f: InboxFilters): boolean {
  if (f.kind === "notes") return false;
  if (f.archived || f.starred) return false;
  // Only request approvals can belong to a dry run.
  const dry = a.type === "request" && a.request?.dry_run === true;
  if (f.dryRun === "only" && !dry) return false;
  if (f.dryRun === "hide" && dry) return false;
  const q = f.q.trim().toLowerCase();
  if (q) {
    const hay =
      `${approvalTitle(a)} ${a.proposal_md ?? ""} ${a.request?.body_md ?? ""}`.toLowerCase();
    if (!hay.includes(q)) return false;
  }
  return true;
}

function ms(ts: string): number {
  const t = Date.parse(ts);
  return Number.isNaN(t) ? 0 : t;
}

/**
 * Notes arrive in server order. By default pending approvals come first
 * (soonest deadline, then oldest), then the notes; with time sort both are
 * interleaved newest first. Under "Needs action" only approvals remain.
 */
export function mergeInboxRows(
  notes: readonly InboxItem[],
  approvals: readonly Approval[],
  f: InboxFilters,
): InboxRow[] {
  const seen = new Set<string>();
  const acts = approvals
    .filter((a) => {
      if (a.status !== "pending" || seen.has(a.id)) return false;
      seen.add(a.id);
      return approvalMatches(a, f);
    })
    .sort(comparePending)
    .map((approval): InboxRow => ({
      kind: "approval",
      key: approvalKey(approval.id),
      approval,
    }));
  const ns =
    f.kind === "action"
      ? []
      : notes.map((item): InboxRow => ({
          kind: "note",
          key: noteKey(item.id),
          item,
        }));
  if (f.sort === "priority") return [...acts, ...ns];
  const at = (r: InboxRow) =>
    ms(r.kind === "note" ? r.item.created_at : r.approval.created_at);
  return [...acts, ...ns].sort((a, b) => at(b) - at(a));
}

/** Inverse of `selectionKey`. */
export function selectionFromKey(key: string): Selection | null {
  if (key.startsWith("n:")) return { kind: "note", id: key.slice(2) };
  if (key.startsWith("a:")) return { kind: "approval", id: key.slice(2) };
  return null;
}
