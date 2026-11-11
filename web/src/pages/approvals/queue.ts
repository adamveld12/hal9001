import type {
  Approval,
  ApprovalList,
  ApprovalsQuery,
  ApprovalStatus,
  ApprovalType,
} from "../../api/approvals";

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

/** One-line label for a row: the tool call title, or the first line of the proposal. */
export function approvalTitle(a: Approval): string {
  if (a.type === "request") {
    const title = a.request?.title || "Approval";
    return a.request?.dry_run ? `[dry run] ${title}` : title;
  }
  if (a.type === "tool_call") {
    return a.permission_request?.title || "Tool call";
  }
  for (const raw of (a.proposal_md ?? "").split("\n")) {
    const line = raw
      .replace(/^[\s>#*\-+\d.]+/, "")
      .replaceAll(/[*_`]/g, "")
      .trim();
    if (line) return line.length > 100 ? `${line.slice(0, 99)}…` : line;
  }
  return "Proposal";
}

/**
 * An operator approval: an external MCP client's call to a tool with side
 * effects. It is a `request` with no flow or node (`docs/mcp.md`).
 */
export function isOperatorApproval(a: Approval): boolean {
  return a.type === "request" && a.flow_id === null && a.node_run_id === null;
}

/** The approval a node with `requires_approval` raised before running: approving runs the node. */
export function isGateApproval(a: Approval): boolean {
  return a.type === "request" && a.request?.gate === true;
}

/** Where an approval comes from, for the muted label in list rows. */
export function approvalOrigin(a: Approval): string {
  if (a.flow_id !== null) return `${a.flow_id} / ${a.node_id ?? ""}`;
  if (!isOperatorApproval(a)) return "sensor assist";
  return a.request?.client ? `MCP: ${a.request.client}` : "MCP client";
}

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
      return a.type === "request" && a.request?.on_timeout
        ? `Timed out; the flow continued on ${a.request.on_timeout === "error" ? "the error port" : `port ${a.request.on_timeout}`}`
        : "Timed out without a decision";
    case "approved":
      return a.type === "tool_call"
        ? `Allowed by ${a.decided_by ?? "unknown"}`
        : `Approved by ${a.decided_by ?? "unknown"}`;
    case "denied":
      return `Denied by ${a.decided_by ?? "unknown"}${a.cancel_run ? ", run cancelled" : ""}${isGateApproval(a) ? "; the node did not run" : ""}`;
  }
}

/** Confirmation shown after a decision was recorded. */
export function decisionNotice(a: Approval): string {
  if (a.status === "approved") {
    if (isGateApproval(a)) return "Approved. The node will run.";
    if (a.type === "request") return "Approved.";
    return a.type === "tool_call" ? "Tool call allowed." : "Proposal approved.";
  }
  if (a.cancel_run) return "Denied. The run was cancelled.";
  if (isGateApproval(a)) return "Denied. The node did not run.";
  if (a.type === "request") return "Denied.";
  return a.type === "tool_call" ? "Tool call denied." : "Proposal denied.";
}
