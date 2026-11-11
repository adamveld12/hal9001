import type {
  Approval,
  ApprovalList,
  ApprovalsQuery,
  ApprovalStatus,
  ApprovalType,
} from "../../api/approvals";
import { originText } from "./card";

export type ApprovalsTab = "pending" | "history";

/** Approvals page state as kept in the URL search params. */
export interface ApprovalsView {
  tab: ApprovalsTab;
  /** History only; null shows every decided approval. */
  status: Exclude<ApprovalStatus, "pending"> | null;
  type: ApprovalType | null;
}

export const DEFAULT_VIEW: ApprovalsView = {
  tab: "pending",
  status: null,
  type: null,
};

export const HISTORY_STATUSES = ["approved", "denied", "expired"] as const;

export function viewFromParams(p: URLSearchParams): ApprovalsView {
  const status = p.get("status");
  const type = p.get("type");
  return {
    tab: p.get("tab") === "history" ? "history" : "pending",
    status: HISTORY_STATUSES.find((s) => s === status) ?? null,
    type:
      type === "proposal" || type === "tool_call" || type === "request"
        ? type
        : null,
  };
}

export function viewToParams(v: ApprovalsView): URLSearchParams {
  const p = new URLSearchParams();
  if (v.tab === "history") {
    p.set("tab", "history");
    if (v.status) p.set("status", v.status);
  }
  if (v.type) p.set("type", v.type);
  return p;
}

/** The queue asks for `pending`; history asks for every decided status (or one of them). */
export function viewToQuery(v: ApprovalsView): ApprovalsQuery {
  const q: ApprovalsQuery = {};
  if (v.tab === "pending") q.status = ["pending"];
  else q.status = v.status ? [v.status] : [...HISTORY_STATUSES];
  if (v.type) q.type = v.type;
  return q;
}

function ms(ts: string | null): number {
  const t = ts ? Date.parse(ts) : NaN;
  return Number.isNaN(t) ? 0 : t;
}

/**
 * Queue order: approvals with a deadline first, soonest deadline first (a
 * tool call blocks a running agent); then those without, oldest first.
 */
export function comparePending(a: Approval, b: Approval): number {
  const da = a.expires_at ? ms(a.expires_at) : null;
  const db = b.expires_at ? ms(b.expires_at) : null;
  if (da !== null && db !== null && da !== db) return da - db;
  if (da !== null && db === null) return -1;
  if (da === null && db !== null) return 1;
  return ms(a.created_at) - ms(b.created_at) || (a.id < b.id ? -1 : 1);
}

/** Newest decision first. */
export function compareHistory(a: Approval, b: Approval): number {
  return (
    ms(b.decided_at ?? b.created_at) - ms(a.decided_at ?? a.created_at) ||
    (a.id < b.id ? 1 : -1)
  );
}

/** Flatten loaded pages into the list shown for a tab, dropping duplicates. */
export function mergeApprovalPages(
  pages: readonly ApprovalList[],
  tab: ApprovalsTab,
): Approval[] {
  const seen = new Set<string>();
  const out: Approval[] = [];
  for (const page of pages) {
    for (const a of page.items) {
      if (seen.has(a.id)) continue;
      seen.add(a.id);
      out.push(a);
    }
  }
  return out.sort(tab === "pending" ? comparePending : compareHistory);
}

/** One-line label for a row. */
export function approvalTitle(a: Approval): string {
  return a.title;
}

/** An external MCP client's approval (`docs/mcp.md`). */
export function isOperatorApproval(a: Approval): boolean {
  return a.origin.kind === "mcp";
}

/** The approval a node with `requires_approval` raised before running: approving runs the node. */
export function isGateApproval(a: Approval): boolean {
  return a.gate;
}

/** Where an approval comes from, for the muted label in list rows. */
export function approvalOrigin(a: Approval): string {
  return originText(a.origin);
}

const STEERED = "steering sent";

/** What was decided and how, for the decision panel and history rows. */
export function describeDecision(a: Approval): string {
  switch (a.status) {
    case "pending":
      return "Waiting for a decision";
    case "expired":
      if (isOperatorApproval(a)) return "Timed out; the MCP client was told";
      // A gate has no on_timeout: expiry counts as a denial.
      if (isGateApproval(a))
        return "Timed out; the node was denied and did not run";
      return a.type === "request" && a.on_timeout
        ? `Timed out; the flow continued on ${a.on_timeout === "error" ? "the error port" : `port ${a.on_timeout}`}`
        : "Timed out without a decision";
    case "approved":
      return `Approved by ${a.decided_by ?? "unknown"}`;
    case "denied":
      return `Denied by ${a.decided_by ?? "unknown"}${a.comment ? ` (${STEERED})` : ""}${a.cancel_run ? ", run stopped" : ""}${isGateApproval(a) ? "; the node did not run" : ""}`;
  }
}

/** Confirmation shown after a decision was recorded. */
export function decisionNotice(a: Approval): string {
  if (a.status === "approved") {
    return isGateApproval(a) ? "Approved. The node will run." : "Approved.";
  }
  if (a.cancel_run) return "Denied. The run was stopped.";
  if (isGateApproval(a)) {
    return a.comment
      ? "Denied with steering. The node did not run."
      : "Denied. The node did not run.";
  }
  return a.comment ? "Denied. Your steering was sent." : "Denied.";
}
