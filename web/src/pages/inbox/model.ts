import type { InboxItem, Priority } from "../../api/inbox";
import type { Tone } from "../../lib/status";

export const PRIORITY_TONE: Record<Priority, Tone> = {
  urgent: "bad",
  high: "warn",
  normal: "muted",
  low: "muted",
};

export const PRIORITY_LABEL: Record<Priority, string> = {
  urgent: "Urgent",
  high: "High",
  normal: "Normal",
  low: "Low",
};

/** DOM id of a list row, used to scroll the keyboard cursor into view. */
export function rowDomId(id: string): string {
  return `inbox-row-${id}`;
}

export interface PriorityGroup {
  priority: Priority;
  items: InboxItem[];
}

/** Split a priority-ordered list into consecutive runs of equal priority. */
export function groupByPriority(items: readonly InboxItem[]): PriorityGroup[] {
  const groups: PriorityGroup[] = [];
  for (const item of items) {
    const last = groups.at(-1);
    if (last?.priority === item.priority) last.items.push(item);
    else groups.push({ priority: item.priority, items: [item] });
  }
  return groups;
}

/**
 * Keep `pinned` (the open item) visible at roughly its old position after a
 * change takes it out of the list, such as marking it read under the unread
 * filter. The list is returned unchanged when it already contains the item.
 */
export function withPinned(
  items: readonly InboxItem[],
  pinned: InboxItem | null | undefined,
  index: number,
): InboxItem[] {
  if (!pinned || items.some((i) => i.id === pinned.id)) return [...items];
  const out = [...items];
  out.splice(Math.min(Math.max(index, 0), out.length), 0, pinned);
  return out;
}

/** Note links come from agent output: only open web and mail links. */
export function safeHref(url: string): string | null {
  try {
    const u = new URL(url);
    return ["http:", "https:", "mailto:"].includes(u.protocol) ? u.href : null;
  } catch {
    return null;
  }
}

/**
 * The note's "Agent chats (N)" link: the Chat list filtered to the agent
 * conversations of the note's flow run. Absent for a run without agents and for
 * notes that belong to no flow run (MCP notes), even when there is only one
 * conversation, so the link always lands on the filtered list.
 */
export function agentChatsLink(
  item: Pick<InboxItem, "source" | "flow_run_id" | "agent_chats">,
): { label: string; to: string } | null {
  if (
    item.source === "mcp" ||
    item.flow_run_id === "" ||
    item.agent_chats < 1
  ) {
    return null;
  }
  return {
    label: `Agent chats (${item.agent_chats})`,
    to: `/chat?flow_run=${encodeURIComponent(item.flow_run_id)}`,
  };
}

/** Where "open trace" goes: the agent run that wrote the note, else the flow run. */
export function tracePath(
  item: Pick<InboxItem, "source" | "agent_run_id" | "flow_run_id">,
): string {
  return item.source === "agent_run" && item.agent_run_id
    ? `/agent-runs/${item.agent_run_id}`
    : `/runs/${item.flow_run_id}`;
}
