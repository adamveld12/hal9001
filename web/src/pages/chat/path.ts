// Routes of the Chat page. Keep in step with App.tsx and internal/assist/uiroutes.go.

export const CHAT_ROOT = "/chat";

/** `/chat/new`: the start view of a new chat. It is not a conversation id. */
export const NEW_CHAT_ID = "new";

/** Whether `pathname` is the Chat page or one of its conversations. */
export function isChatPath(pathname: string): boolean {
  return pathname === CHAT_ROOT || pathname.startsWith(`${CHAT_ROOT}/`);
}

/** `/chat`, or `/chat/<id>` for a conversation (an assist session id or an agent run id). */
export function chatPath(id?: string | null): string {
  return id ? `${CHAT_ROOT}/${encodeURIComponent(id)}` : CHAT_ROOT;
}

/** The conversation id in a chat path; null for `/chat` and for the new-chat view. */
export function chatIdFromPath(pathname: string): string | null {
  const m = /^\/chat\/([^/]+)\/?$/.exec(pathname);
  if (!m?.[1]) return null;
  const id = decodeURIComponent(m[1]);
  return id === NEW_CHAT_ID ? null : id;
}

/** Whether the path is `/chat/new`. */
export function isNewChatPath(pathname: string): boolean {
  return /^\/chat\/new\/?$/.test(pathname);
}
