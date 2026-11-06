import type { AssistMessage } from "../../api/assist";

/** How close to the end (px) the list counts as "at the bottom" and keeps following new content. */
export const FOLLOW_THRESHOLD = 80;

/** Whether the list shows its end (within `threshold` px), so new content scrolls into view. */
export function shouldFollow(
  scrollHeight: number,
  scrollTop: number,
  clientHeight: number,
  threshold = FOLLOW_THRESHOLD,
): boolean {
  return scrollHeight - scrollTop - clientHeight <= threshold;
}

/** The newest tool call that waits for a decision, or undefined. */
export function pendingApprovalMessage(
  messages: readonly AssistMessage[],
): AssistMessage | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i];
    const s = m.tool_call?.status;
    if (m.approval_id !== null && (s === "pending" || s === "in_progress")) {
      return m;
    }
  }
  return undefined;
}

/** Whether an element whose top edge is at `itemTop` lies below the visible part of a box ending at `boxBottom`. */
export function isBelowView(itemTop: number, boxBottom: number): boolean {
  return itemTop >= boxBottom - 8;
}
