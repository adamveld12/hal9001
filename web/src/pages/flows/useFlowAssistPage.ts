import { useCallback, useMemo, useRef } from "react";
import { useSearchParams } from "react-router";
import type { AssistPage, EditorBridge } from "../assist/context";

const NEW = "new";

/**
 * What the drawer needs from a flows page: the `?assist=` parameter ("new"
 * before a session exists, else a session id), and the editor that is mounted.
 * `targetId` is the flow the page edits, null on the list page where a new
 * flow is drafted.
 */
export function useFlowAssistPage(targetId: string | null): AssistPage {
  const [params, setParams] = useSearchParams();
  const param = params.get("assist");
  const sessionId = param !== null && param !== NEW ? param : null;
  const editorRef = useRef<EditorBridge | null>(null);

  const setAssist = useCallback(
    (value: string | null, replace: boolean) =>
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (value === null) next.delete("assist");
          else next.set("assist", value);
          return next;
        },
        { replace },
      ),
    [setParams],
  );

  return useMemo<AssistPage>(
    () => ({
      kind: "flow",
      param,
      sessionId,
      targetId,
      open: () => setAssist(NEW, false),
      close: () => setAssist(null, false),
      bind: (sid) => setAssist(sid, true),
      registerEditor: (e) => {
        editorRef.current = e;
      },
      editor: () => editorRef.current,
    }),
    [param, sessionId, targetId, setAssist],
  );
}
