import { useQueryClient } from "@tanstack/react-query";
import type { AssistSessionDetail } from "../../api/assist";
import { qk } from "../../api/keys";
import { useStreamEvents } from "../../api/streamHooks";
import { applyMessageEvent, applySessionEvent } from "./messages";
import { isBusy } from "./sessions";

/**
 * Keep one open session's cached copy current from `assist.updated` and
 * `assist.message`. When a turn ends the session is refetched once, so a
 * missed event cannot leave the conversation incomplete (`stream.resync`
 * already refetches everything).
 */
export function useAssistLive(sessionId: string | null): void {
  const qc = useQueryClient();
  useStreamEvents(["assist.updated", "assist.message"], (e) => {
    if (sessionId === null || !e.data) return;
    const key = qk("assist-session", sessionId);
    if (e.type === "assist.updated") {
      const session = e.data;
      if (session.id !== sessionId) return;
      qc.setQueryData<AssistSessionDetail>(key, (d) =>
        d ? applySessionEvent(d, session) : d,
      );
      if (!isBusy(session.status)) void qc.invalidateQueries({ queryKey: key });
    } else if (e.type === "assist.message") {
      const msg = e.data;
      if (msg.session_id !== sessionId) return;
      qc.setQueryData<AssistSessionDetail>(key, (d) =>
        d ? applyMessageEvent(d, msg) : d,
      );
    }
  });
}
