// Logic behind ApprovalCard: which actions a card offers, what a decision sends, and how an approval's origin, details and conversation link read.

import type { Approval } from "../../api/approvals";
import { lineDiff, type FileDiff } from "./diff";

export interface CardActions {
  approve: boolean;
  deny: boolean;
  /** Deny + stop: a tool call's run is cancelled instead of continuing. */
  denyStop: boolean;
  /** The `Additional steering` box: denials only, and not once the steering cap held the action. */
  steering: boolean;
}

const NONE: CardActions = {
  approve: false,
  deny: false,
  denyStop: false,
  steering: false,
};

/** The three actions (and the steering box) a card offers for an approval. */
export function cardActions(a: Approval): CardActions {
  if (a.status !== "pending") return NONE;
  return {
    approve: true,
    deny: true,
    denyStop: a.type === "tool_call",
    steering: !a.held,
  };
}

export type DecideAction = "approve" | "deny" | "deny_stop";

export interface DecisionInput {
  action: "approve" | "deny";
  /** Steering text: sent with a denial only. */
  comment?: string;
  cancelRun?: boolean;
}

/**
 * What a click sends. Steering text goes with Deny and Deny + stop, never
 * with Approve, and never for a held approval (the box is hidden). Deny + stop
 * is refused for anything but a tool call.
 */
export function decisionFor(
  a: Approval,
  action: DecideAction,
  steering: string,
): DecisionInput | null {
  const offered = cardActions(a);
  if (action === "approve") return offered.approve ? { action } : null;
  if (action === "deny_stop" && !offered.denyStop) return null;
  if (!offered.deny) return null;
  const text = offered.steering ? steering.trim() : "";
  return {
    action: "deny",
    ...(text ? { comment: text } : {}),
    ...(action === "deny_stop" ? { cancelRun: true } : {}),
  };
}

/** Name of an origin kind, as the SOURCE line shows it (never inferred from empty ids). */
export function originKindLabel(o: Approval["origin"]): string {
  switch (o.kind) {
    case "flow":
      return "Flow";
    case "chat":
      return "Chat";
    case "continued_chat":
      return "Continued chat";
    case "mcp":
      return o.client ? `MCP: ${o.client}` : "MCP client";
  }
}

/** The SOURCE text: the kind, then the server's label when it adds something. */
export function originText(o: Approval["origin"]): string {
  const kind = originKindLabel(o);
  const label = o.label.trim();
  if (!label || label === kind || label.startsWith(`${kind}:`)) {
    return label || kind;
  }
  return `${kind} · ${label}`;
}

/** The conversation the card links to; none for MCP approvals or approvals without an agent run. */
export function conversationPath(a: Approval): string | null {
  if (a.origin.kind === "mcp") return null;
  return a.conversation?.path ?? null;
}

/** Whether the open page is the conversation the approval links to (the link would go nowhere new). */
export function inConversation(a: Approval, pathname: string): boolean {
  const path = conversationPath(a);
  return path !== null && path.split("?")[0] === pathname;
}

/**
 * The pending tool call's own reason (policy rule, emit payload), unless it only restates the command the card shows.
 * A decided call's reason is the decision text, so it is not shown.
 */
export function reasonText(a: Approval): string | null {
  const reason = a.permission_request?.reason?.trim() ?? "";
  if (a.status !== "pending" || reason === "") return null;
  const command = a.details.command?.trim() ?? "";
  return command !== "" && reason.includes(command) ? null : reason;
}

/** Where an MCP approval links to instead of a conversation: its own request in the Inbox. */
export function requestPath(a: Approval): string | null {
  return a.origin.kind === "mcp" ? `/inbox/approval/${a.id}` : null;
}

/** The `details` diffs as the line diffs DiffView draws. */
export function detailDiffs(a: Approval): FileDiff[] {
  return a.details.diffs.map((d) => ({
    path: d.path,
    lines: lineDiff(d.old ?? "", d.new),
  }));
}

/** One line of what will happen, for the compact card: the command, else the first field. */
export function detailSummary(a: Approval): string | null {
  const { command, fields, diffs } = a.details;
  if (command) return command.split("\n")[0] ?? command;
  const f = fields[0];
  if (f) return `${f.key}: ${f.value}`;
  const d = diffs[0];
  if (d) return d.path ? `Edit ${d.path}` : "Edit";
  return null;
}

/** Whether the card has anything to show beside the description. */
export function hasDetails(a: Approval): boolean {
  const d = a.details;
  return (
    !!d.command ||
    d.fields.length > 0 ||
    d.diffs.length > 0 ||
    d.body_md.trim() !== "" ||
    a.permission_request !== null ||
    !!a.proposal_md
  );
}

export const agentWritten = (a: Approval): boolean =>
  a.description_by === "agent";

/** Pending approvals of a conversation that no message points to; they render at the end of the transcript. */
export function unlinkedApprovals(
  items: readonly Approval[],
  linkedIds: ReadonlySet<string>,
): Approval[] {
  return items.filter((a) => !linkedIds.has(a.id));
}
