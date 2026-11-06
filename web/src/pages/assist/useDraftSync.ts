import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  usePutAssistDraft,
  useAssistSession,
  type AssistSessionDetail,
} from "../../api/assist";
import { useQueryClient } from "@tanstack/react-query";
import { qk } from "../../api/keys";
import { isConflict } from "../files/diagnostics";
import { useAssistPage } from "./context";
import {
  changedLines,
  initSync,
  keepMine,
  pushed,
  reconcileDraft,
  shouldPush,
  takeAi,
  type AiChange,
  type Reconciled,
  type SyncState,
} from "./draftSync";
import { bindSession } from "./sessions";

/** User edits are pushed to the draft once typing has paused this long. */
const PUSH_DELAY_MS = 600;

/**
 * The session the open editor belongs to: the one in the URL, provided it edits
 * this target (`targetId`) or one that had no file yet and is still open (see
 * `bindSession`). Anything else leaves the editor on its own.
 */
export function useBoundSession(targetId: string | null): {
  session: AssistSessionDetail | undefined;
  sessionId: string | null;
  /** The URL names a session of another target. */
  otherTarget: string | null;
} {
  const page = useAssistPage();
  const q = useAssistSession(page.sessionId);
  const session = q.data;
  const b = bindSession(session, page.kind, targetId);
  return { session, sessionId: b.sessionId, otherTarget: b.otherTarget };
}

/**
 * Keeps an editor and the session draft in step (see draftSync.ts). The editor
 * owns `text`; this hook replaces it with agent updates the user has not
 * typed over, pushes the user's edits, and exposes the undecided case.
 */
export function useDraftSync({
  sessionId,
  text,
  setText,
  dirty,
}: {
  sessionId: string | null;
  text: string;
  setText: (t: string) => void;
  /** The editor holds unsaved edits (relevant when the session is first bound). */
  dirty: boolean;
}) {
  const page = useAssistPage();
  const qc = useQueryClient();
  const session = useAssistSession(sessionId);
  const draft = session.data?.draft ?? null;
  const put = usePutAssistDraft(sessionId);

  const [state, setState] = useState<{
    sessionId: string | null;
    sync: SyncState;
    ai: AiChange | null;
  }>(() => ({ sessionId, sync: initSync(text, dirty), ai: null }));
  let cur = state;
  if (state.sessionId !== sessionId) {
    cur = { sessionId, sync: initSync(text, dirty), ai: null };
    setState(cur);
  }
  const { sync, ai } = cur;

  const latest = useRef({ sync, text, put });
  useEffect(() => {
    latest.current = { sync, text, put };
  });

  const apply = useCallback(
    (r: Reconciled) => {
      setState((s) => ({ ...s, sync: r.sync, ai: r.aiChange ?? s.ai }));
      if (r.text !== latest.current.text) setText(r.text);
    },
    [setText],
  );

  // A draft arrived from the server. Reconciling the same draft again changes nothing.
  useEffect(() => {
    if (sessionId === null || !draft) return;
    const { sync: s, text: t } = latest.current;
    const r = reconcileDraft(s, draft, t);
    if (r.sync !== s || r.text !== t || r.aiChange) apply(r);
  }, [sessionId, draft, apply]);

  const push = useCallback(async () => {
    const { sync: s, text: t, put: p } = latest.current;
    if (sessionId === null || s.syncedHash === null) return;
    try {
      const d = await p.mutateAsync({ yaml: t, expected_hash: s.syncedHash });
      setState((st) => ({ ...st, sync: pushed(d, t) }));
    } catch (e) {
      // Someone (the agent) changed the draft first: learn the new one and let the user decide.
      if (isConflict(e)) {
        void qc.invalidateQueries({
          queryKey: qk("assist-session", sessionId),
        });
      }
    }
  }, [sessionId, qc]);

  // Push the user's edits, debounced. Not while a push is in flight or an agent update awaits a decision.
  const pushing = put.isPending;
  useEffect(() => {
    if (sessionId === null || pushing || !shouldPush(sync, text)) return;
    const t = setTimeout(() => void push(), PUSH_DELAY_MS);
    return () => clearTimeout(t);
  }, [sessionId, sync, text, pushing, push]);

  const flush = useCallback(async () => {
    const { sync: s, text: t } = latest.current;
    if (shouldPush(s, t)) await push();
  }, [push]);

  // Offer the editor to the drawer.
  const { registerEditor } = page;
  useEffect(() => {
    registerEditor({ getText: () => latest.current.text, flush });
    return () => registerEditor(null);
  }, [registerEditor, flush]);

  const marks = useMemo(
    () => (ai ? changedLines(ai.before, ai.after) : []),
    [ai],
  );
  const pending =
    sync.pending && sync.pending.yaml !== text ? sync.pending : null;

  return {
    draft,
    pending,
    marks,
    takeAi: () => apply(takeAi(sync, text)),
    keepMine: () => setState((s) => ({ ...s, sync: keepMine(s.sync) })),
    clearMarks: () => setState((s) => ({ ...s, ai: null })),
  };
}
