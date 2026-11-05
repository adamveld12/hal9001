// Pure helpers for the assist conversation: merging live messages into a
// session, ordering, and where each turn's "View trace" link goes.

import type {
  AssistMessage,
  AssistStreamMessage,
  AssistSession,
  AssistSessionDetail,
} from "../../api/assist";

function before(a: AssistMessage, b: AssistMessage): boolean {
  if (a.created_at !== b.created_at) return a.created_at < b.created_at;
  return a.id < b.id;
}

/**
 * Insert or replace one message. A message already present (same id) is
 * replaced where it stands: tool calls and assistant text are updated in
 * place. New ones go to their place by creation time, then id.
 */
export function mergeMessage(
  list: readonly AssistMessage[],
  msg: AssistMessage,
): AssistMessage[] {
  const at = list.findIndex((m) => m.id === msg.id);
  if (at >= 0) return list.map((m, i) => (i === at ? msg : m));
  let pos = list.length;
  while (pos > 0 && before(msg, list[pos - 1]!)) pos--;
  return [...list.slice(0, pos), msg, ...list.slice(pos)];
}

/** Apply an `assist.message` event to a cached session; events for other sessions change nothing. */
export function applyMessageEvent(
  detail: AssistSessionDetail,
  ev: AssistStreamMessage,
): AssistSessionDetail {
  if (ev.session_id !== detail.id) return detail;
  const { session_id: _sessionId, ...msg } = ev;
  return { ...detail, messages: mergeMessage(detail.messages, msg) };
}

/** Apply an `assist.updated` event; an older snapshot than the cached one is ignored. */
export function applySessionEvent(
  detail: AssistSessionDetail,
  session: AssistSession,
): AssistSessionDetail {
  if (session.id !== detail.id || session.updated_at < detail.updated_at) {
    return detail;
  }
  return { ...session, messages: detail.messages };
}

/**
 * Message id -> agent run id, for the last message of each turn. A turn is a
 * run of consecutive agent-side messages (assistant, tool, system) of one agent
 * run; the trace link sits under its last message.
 */
export function traceLinks(
  messages: readonly AssistMessage[],
): Map<string, string> {
  const out = new Map<string, string>();
  let last: AssistMessage | null = null;
  const close = () => {
    if (last?.agent_run_id) out.set(last.id, last.agent_run_id);
    last = null;
  };
  for (const m of messages) {
    if (m.role === "user" || m.agent_run_id === null) {
      close();
      continue;
    }
    if (last && last.agent_run_id !== m.agent_run_id) close();
    last = m;
  }
  close();
  return out;
}
