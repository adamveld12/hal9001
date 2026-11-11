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

/**
 * The message an approval's `anchor` points to: the last message whose id is not after it (ids sort in transcript
 * order, and the anchor is the newest message or event at the time the approval was requested). Undefined without
 * an anchor or when every message is after it.
 */
export function anchorMessage(
  messages: readonly Pick<AssistMessage, "id">[],
  anchor: string | null | undefined,
): string | undefined {
  if (!anchor) return undefined;
  let found: string | undefined;
  for (const m of messages) {
    if (m.id <= anchor && (found === undefined || m.id > found)) found = m.id;
  }
  return found;
}
