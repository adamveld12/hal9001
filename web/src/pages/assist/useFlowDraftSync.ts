import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { useAssistSession, usePutAssistDraft } from "../../api/assist";
import { errorMessage } from "../../api/client";
import { flowDraftParseQuery, renderFlowYaml } from "../../api/flows";
import { qk } from "../../api/keys";
import type { Flow } from "../../flow/model";
import { isConflict } from "../files/diagnostics";
import { useAssist } from "./context";
import {
  afterPush,
  applyDraft,
  initFlowSync,
  keepMineVersion,
  needsPush,
  pendingUpdate,
  takeAiVersion,
  type FlowSyncState,
} from "./flowDraftSync";

/** Edits of the canvas are pushed to the draft once editing has paused this long. */
const PUSH_DELAY_MS = 600;

/**
 * Keeps the flow canvas and the session draft in step (see flowDraftSync.ts):
 * agent updates the user has not edited over become the canvas's content,
 * edits of the canvas are written to the draft as YAML, and an undecided
 * update is offered through `pending`. `editorFlow` is what the canvas
 * reports through `FlowEditor.onChange`.
 */
export function useFlowDraftSync({
  targetId,
  sessionId,
  editorFlow,
}: {
  /** The flow the canvas edits; null for a new one. The drawer feeds the canvas only while it is on this flow. */
  targetId: string | null;
  sessionId: string | null;
  editorFlow: Flow | null;
}) {
  const assist = useAssist();
  const qc = useQueryClient();
  const session = useAssistSession(sessionId);
  const draft = session.data?.draft ?? null;
  const put = usePutAssistDraft(sessionId);

  const [held, setHeld] = useState<{
    sessionId: string | null;
    sync: FlowSyncState;
  }>(() => ({ sessionId, sync: initFlowSync() }));
  let cur = held;
  if (held.sessionId !== sessionId) {
    cur = { sessionId, sync: initFlowSync() };
    setHeld(cur);
  }
  const sync = cur.sync;
  const setSync = useCallback(
    (f: (s: FlowSyncState) => FlowSyncState) =>
      setHeld((h) => ({ ...h, sync: f(h.sync) })),
    [],
  );

  const latest = useRef({ sync, editorFlow, put });
  useEffect(() => {
    latest.current = { sync, editorFlow, put };
  });

  // A draft the canvas has not seen is parsed by the daemon, then reconciled with the canvas.
  const unseen =
    draft !== null &&
    draft.hash !== sync.sync.syncedHash &&
    draft.hash !== sync.sync.pending?.hash;
  const parsed = useQuery({
    ...flowDraftParseQuery(draft?.hash ?? "", draft?.yaml ?? ""),
    enabled: unseen,
  });
  const parsedFlow = parsed.data;
  useEffect(() => {
    if (sessionId === null || draft === null || !unseen || !parsedFlow) return;
    setSync((s) =>
      applyDraft(s, draft, parsedFlow.flow, latest.current.editorFlow),
    );
  }, [sessionId, draft, unseen, parsedFlow, setSync]);

  const [pushing, setPushing] = useState(false);
  const [pushError, setPushError] = useState<string | null>(null);
  const inflight = useRef<Promise<void> | null>(null);

  const push = useCallback(async () => {
    const { sync: s, editorFlow: sent, put: p } = latest.current;
    if (sessionId === null || sent === null || s.sync.syncedHash === null) {
      return;
    }
    setPushing(true);
    try {
      const yaml = await renderFlowYaml(sent);
      if (yaml === null) return;
      const d = await p.mutateAsync({
        yaml,
        expected_hash: s.sync.syncedHash,
      });
      setPushError(null);
      setSync((c) => afterPush(c, d, sent));
    } catch (e) {
      // The agent changed the draft first: learn the new one and let the user decide.
      if (isConflict(e)) {
        void qc.invalidateQueries({
          queryKey: qk("assist-session", sessionId),
        });
      } else {
        setPushError(errorMessage(e));
      }
    } finally {
      setPushing(false);
    }
  }, [sessionId, qc, setSync]);

  const pushOnce = useCallback(() => {
    inflight.current ??= push().finally(() => {
      inflight.current = null;
    });
    return inflight.current;
  }, [push]);

  // Push the canvas's edits, debounced. Not while a push is in flight or an agent update awaits a decision.
  const wants = needsPush(sync, editorFlow);
  useEffect(() => {
    if (sessionId === null || pushing || !wants) return;
    const t = setTimeout(() => void pushOnce(), PUSH_DELAY_MS);
    return () => clearTimeout(t);
  }, [sessionId, pushing, wants, editorFlow, pushOnce]);

  const flush = useCallback(async () => {
    await inflight.current;
    const { sync: s, editorFlow: f } = latest.current;
    if (needsPush(s, f)) await pushOnce();
  }, [pushOnce]);

  // Offer the canvas to the drawer: edits it holds that the draft lacks travel with the message.
  const { registerEditor } = assist;
  useEffect(() => {
    registerEditor({
      kind: "flow",
      id: targetId,
      flush,
      undecided: () =>
        pendingUpdate(latest.current.sync, latest.current.editorFlow) !== null,
      getText: async () => {
        const { sync: s, editorFlow: f } = latest.current;
        return f !== null && needsPush(s, f)
          ? ((await renderFlowYaml(f)) ?? undefined)
          : undefined;
      },
    });
    return () => registerEditor(null);
  }, [registerEditor, flush, targetId]);

  const pendingFlow = pendingUpdate(sync, editorFlow);

  return {
    draft,
    /** The last draft flow that parsed; null until the first one has. */
    shown: sync.shown,
    /** Changes whenever the canvas must load `shown` anew. */
    rev: sync.rev,
    /** The newest draft does not parse; the canvas shows the last good one. */
    invalid: sync.invalid,
    /** An agent update waiting for "Take AI version / Keep mine". */
    pending: pendingFlow === null ? null : sync.sync.pending,
    pushError,
    takeAi: () => setSync((s) => takeAiVersion(s, latest.current.editorFlow)),
    keepMine: () => setSync(keepMineVersion),
  };
}
