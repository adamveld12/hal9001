import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import {
  fetchAssistSession,
  useAssistSession,
  useCreateAssistSession,
  useEndAssistSession,
  useSendAssistMessage,
  type AssistSession,
  type AssistStartDraft,
} from "../../api/assist";
import { ApiError, errorMessage } from "../../api/client";
import { useStreamEvents } from "../../api/streamHooks";
import { usePageContextRegistry } from "../../layout/pageContext";
import { capPageContext } from "../../lib/pageContext";
import { defaultTitle } from "../../lib/routeTitle";
import { AssistContext, type Assist, type EditorBridge } from "./context";
import {
  drawerReducer,
  GENERAL_TARGET,
  loadDrawer,
  saveDrawer,
  sameTarget,
  type DrawerTarget,
} from "./drawerState";
import { startSession } from "./startSession";
import { targetConfig, targetPath } from "./targets";
import { chatPath, isChatPath, NEW_CHAT_ID } from "../chat/path";
import { loadOrigin, safeOrigin } from "../chat/origin";

const LEAVE_PAGE = "Leave this page? Unsaved changes will be lost.";
/** Stream events remembered so a replay after a reconnect does not act twice. */
const SEEN_EVENTS = 200;

const targetOf = (e: EditorBridge): DrawerTarget => ({
  kind: e.kind,
  id: e.id,
});

/**
 * Owns the global Ask AI drawer's state (open, session, target; persisted in
 * sessionStorage), the editor that is mounted, and what the assistant asks the
 * UI to do: `assist.navigate` opens a page, `assist.start_draft` starts a draft
 * session. Mounted by the app shell, above the routed page and the drawer.
 */
export function AssistProvider({ children }: { children: ReactNode }) {
  const [state, dispatch] = useReducer(drawerReducer, undefined, loadDrawer);
  const [notice, setNotice] = useState<string | null>(null);
  const navigate = useNavigate();
  const location = useLocation();
  const registry = usePageContextRegistry();
  const active = useAssistSession(state.sessionId);
  const create = useCreateAssistSession();
  const send = useSendAssistMessage();
  const end = useEndAssistSession();

  const latest = useRef({ state, location, active: active.data });
  useEffect(() => {
    latest.current = { state, location, active: active.data };
  });
  useEffect(() => saveDrawer(state), [state]);

  // The editor that is mounted. Its target is state too: the session view shows
  // a link to the editor while none is open for the drawer's target.
  const editorRef = useRef<EditorBridge | null>(null);
  const [editorTarget, setEditorTarget] = useState<DrawerTarget | null>(null);
  const registerEditor = useCallback((e: EditorBridge | null) => {
    editorRef.current = e;
    const next = e === null ? null : targetOf(e);
    setEditorTarget((prev) =>
      prev === next || sameTarget(prev, next) ? prev : next,
    );
  }, []);
  const editor = useCallback(() => {
    const e = editorRef.current;
    return e !== null && sameTarget(targetOf(e), latest.current.state.target)
      ? e
      : null;
  }, []);

  /**
   * Whether the drawer may move to `next` (null: close). An editor shown only
   * while the drawer is on its target (a new-sensor pane) asks first.
   */
  const mayLeave = useCallback((next: DrawerTarget | null): boolean => {
    const e = editorRef.current;
    const cur = latest.current.state;
    if (!e?.confirmLeave || !cur.open || !sameTarget(targetOf(e), cur.target)) {
      return true;
    }
    if (next !== null && sameTarget(next, cur.target)) return true;
    return e.confirmLeave();
  }, []);

  const seeds = useRef(0);
  const nextSeed = useCallback(
    (text: string) => ({ text, n: ++seeds.current }),
    [],
  );

  const openDrawer = useCallback(() => dispatch({ type: "open" }), []);
  const close = useCallback(() => {
    if (mayLeave(null)) dispatch({ type: "close" });
  }, [mayLeave]);
  const toggle = useCallback(() => {
    if (latest.current.state.open) close();
    else dispatch({ type: "open" });
  }, [close]);
  const openTarget = useCallback(
    (target: DrawerTarget, seed?: string) => {
      if (!mayLeave(target)) return;
      dispatch({
        type: "openTarget",
        target,
        seed: seed === undefined ? undefined : nextSeed(seed),
      });
    },
    [mayLeave, nextSeed],
  );
  const newChat = useCallback(() => {
    if (mayLeave(GENERAL_TARGET)) dispatch({ type: "newChat" });
  }, [mayLeave]);
  const select = useCallback(
    (s: Pick<AssistSession, "id" | "target">) => {
      if (!mayLeave(s.target)) return;
      dispatch({ type: "select", sessionId: s.id, target: s.target });
    },
    [mayLeave],
  );
  const bind = useCallback(
    (sessionId: string, target?: DrawerTarget) =>
      dispatch({ type: "bind", sessionId, target }),
    [],
  );
  const retarget = useCallback(
    (target: DrawerTarget) => dispatch({ type: "retarget", target }),
    [],
  );
  const seedText = useCallback(
    (text: string) => dispatch({ type: "seed", seed: nextSeed(text) }),
    [nextSeed],
  );
  const consumeSeed = useCallback(
    () => dispatch({ type: "seed", seed: null }),
    [],
  );
  const dismissNotice = useCallback(() => setNotice(null), []);

  // A persisted session that is gone or ended is dropped once, on the first load after a reload.
  const restored = useRef(state.sessionId);
  useEffect(() => {
    const r = restored.current;
    if (r === null) return;
    if (state.sessionId !== r) {
      restored.current = null;
      return;
    }
    if (active.isPending) return;
    restored.current = null;
    const gone =
      active.error instanceof ApiError && active.error.status === 404;
    if (gone || active.data?.status === "closed") dispatch({ type: "newChat" });
  }, [state.sessionId, active.isPending, active.error, active.data?.status]);

  // A session asked for by id: `?assist=<id>` in a link, or openSession. The
  // target comes from the session; the parameter is removed once it is taken.
  const qc = useQueryClient();
  const openSession = useCallback(
    (id: string) => {
      fetchAssistSession(qc, id).then(select, () => {
        setNotice("That chat no longer exists.");
        dispatch({ type: "open" });
      });
    },
    [qc, select],
  );
  const [params, setParams] = useSearchParams();
  const linkParam = params.get("assist");
  useEffect(() => {
    if (linkParam === null) return;
    const next = new URLSearchParams(params);
    next.delete("assist");
    if (isChatPath(latest.current.location.pathname)) {
      // On the Chat page the link selects the conversation there.
      void navigate(
        { pathname: chatPath(linkParam), search: next.toString() },
        { replace: true },
      );
      return;
    }
    openSession(linkParam);
    setParams(next, { replace: true });
  }, [linkParam, params, openSession, setParams, navigate]);

  const pageContext = useCallback(() => {
    const loc = latest.current.location;
    return capPageContext(
      loc.pathname + loc.search,
      registry.get() ?? { title: defaultTitle(loc.pathname) },
    );
  }, [registry]);

  /** Open a page the assistant asked for, after asking before unsaved edits are dropped. */
  const go = useCallback(
    (path: string): boolean => {
      const loc = latest.current.location;
      if (path === loc.pathname + loc.search) return true;
      if (
        editorRef.current?.dirty?.() === true &&
        !window.confirm(LEAVE_PAGE)
      ) {
        return false;
      }
      void navigate(path);
      return true;
    },
    [navigate],
  );

  // The chat the Chat page shows (null: none). While that page is open the
  // drawer is not, so the assistant's requests are taken for this chat.
  const chatPageSession = useRef<string | null>(null);
  const registerChatPage = useCallback((id: string | null) => {
    chatPageSession.current = id;
  }, []);

  /** Show the chat in the full page instead of the drawer; the drawer closes, the chat stays the same session. */
  const openFullPage = useCallback(() => {
    const id = latest.current.state.sessionId;
    if (!go(chatPath(id ?? NEW_CHAT_ID))) return;
    dispatch({ type: "close" });
  }, [go]);

  /** Back from the Chat page: the drawer shows `session` (or its start view) and the page that was open before returns. */
  const returnToDrawer = useCallback(
    (session: Pick<AssistSession, "id" | "target"> | null) => {
      if (session === null) dispatch({ type: "open" });
      else
        dispatch({
          type: "select",
          sessionId: session.id,
          target: session.target,
        });
      void navigate(safeOrigin(loadOrigin()));
    },
    [navigate],
  );

  /** The session a draft the assistant starts copies its runtime and model from: the one shown, in the drawer or on the Chat page. */
  const currentSession = async () => {
    const { location, active } = latest.current;
    if (!isChatPath(location.pathname)) return active;
    const id = chatPageSession.current;
    return id === null
      ? undefined
      : await fetchAssistSession(qc, id).catch(() => undefined);
  };

  const startDraft = async (d: AssistStartDraft) => {
    const target: DrawerTarget = { kind: d.kind, id: d.id };
    const path = targetPath(d.kind, d.id);
    const prompt = d.prompt?.trim() ?? "";
    if (prompt === "") {
      dispatch({ type: "openTarget", target });
      if (path !== null) go(path);
      return;
    }
    const cur = await currentSession();
    if (!cur) {
      setNotice("Could not start the draft: the chat is not loaded yet.");
      return;
    }
    const ed = editorRef.current;
    try {
      const yaml =
        ed !== null && sameTarget(targetOf(ed), target)
          ? await ed.getText()
          : undefined;
      await startSession({
        create: () =>
          create.mutateAsync({
            target:
              d.id === null ? { kind: d.kind } : { kind: d.kind, id: d.id },
            runtime: cur.runtime,
            model: cur.model ?? undefined,
            yaml,
          }),
        send: (id) =>
          send.mutateAsync({ id, text: prompt, page_context: pageContext() }),
        end: (id) => end.mutateAsync(id),
        bind: (id) => dispatch({ type: "bind", sessionId: id, target }),
      });
      if (path !== null) go(path);
    } catch (e) {
      setNotice(
        `Could not start the ${targetConfig(d.kind).noun} draft: ${errorMessage(e)}`,
      );
    }
  };

  // Each stream event acts once, and only for the session shown: the drawer's,
  // or the Chat page's while that page is open.
  const seen = useRef(new Set<string>());
  const accept = (eventId: string, sessionId: string): boolean => {
    const shown = isChatPath(latest.current.location.pathname)
      ? chatPageSession.current
      : latest.current.state.sessionId;
    if (sessionId !== shown) return false;
    if (seen.current.has(eventId)) return false;
    seen.current.add(eventId);
    if (seen.current.size > SEEN_EVENTS) {
      const [oldest] = seen.current;
      if (oldest !== undefined) seen.current.delete(oldest);
    }
    return true;
  };
  useStreamEvents(["assist.navigate", "assist.start_draft"], (e) => {
    if (e.type === "assist.navigate") {
      if (e.data && accept(e.id, e.data.session_id)) go(e.data.path);
    } else if (e.type === "assist.start_draft") {
      if (e.data && accept(e.id, e.data.session_id)) void startDraft(e.data);
    }
  });

  const editorActive = sameTarget(editorTarget, state.target);
  const value = useMemo<Assist>(
    () => ({
      open: state.open,
      sessionId: state.sessionId,
      target: state.target,
      seed: state.seed,
      notice,
      editorActive,
      openDrawer,
      close,
      toggle,
      openTarget,
      newChat,
      select,
      openSession,
      bind,
      retarget,
      seedText,
      consumeSeed,
      dismissNotice,
      registerEditor,
      editor,
      pageContext,
      openFullPage,
      returnToDrawer,
      registerChatPage,
    }),
    [
      state,
      notice,
      editorActive,
      openDrawer,
      close,
      toggle,
      openTarget,
      newChat,
      select,
      openSession,
      bind,
      retarget,
      seedText,
      consumeSeed,
      dismissNotice,
      registerEditor,
      editor,
      pageContext,
      openFullPage,
      returnToDrawer,
      registerChatPage,
    ],
  );

  return (
    <AssistContext.Provider value={value}>{children}</AssistContext.Provider>
  );
}
