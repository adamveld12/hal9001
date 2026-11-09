/** What starting a conversation needs: create the session, send the first message, end a session, point the drawer at one. */
export interface StartDeps<S extends { id: string }> {
  create: () => Promise<S>;
  send: (sessionId: string) => Promise<unknown>;
  end: (sessionId: string) => Promise<unknown>;
  bind: (sessionId: string) => void;
}

/**
 * Create a session and send its first message. The drawer is pointed at the
 * session only once the message was accepted, so the start composer (with the
 * text and the error) stays on screen when it is not. A session whose first
 * message failed is ended instead of left empty; the original error is rethrown.
 */
export async function startSession<S extends { id: string }>(
  deps: StartDeps<S>,
): Promise<S> {
  const session = await deps.create();
  try {
    await deps.send(session.id);
  } catch (e) {
    await deps.end(session.id).catch(() => undefined);
    throw e;
  }
  deps.bind(session.id);
  return session;
}
