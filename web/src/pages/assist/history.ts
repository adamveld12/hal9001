import type { AssistSession } from "../../api/assist";
import { targetConfig } from "./targets";

/** How many chats the history list shows. */
export const HISTORY_LIMIT = 20;

/**
 * What a chat is called in the history: the start of its first message, or,
 * before there is one, what it is about.
 */
export function sessionRowLabel(s: Pick<AssistSession, "title" | "target">): {
  title: string;
  target: string;
} {
  const cfg = targetConfig(s.target.kind);
  const target = cfg.sessionTitle(s.target.id);
  if (s.title !== null && s.title.trim() !== "") {
    return { title: s.title, target };
  }
  if (s.target.kind === "general") return { title: "New chat", target };
  return { title: `${target} draft`, target };
}

/** Open chats, most recently active first, at most `limit`. */
export function recentSessions(
  sessions: readonly AssistSession[],
  limit: number = HISTORY_LIMIT,
): AssistSession[] {
  return sessions
    .filter((s) => s.status !== "closed")
    .toSorted((a, b) =>
      a.updated_at < b.updated_at ? 1 : a.updated_at > b.updated_at ? -1 : 0,
    )
    .slice(0, limit);
}
