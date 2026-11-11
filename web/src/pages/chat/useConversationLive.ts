import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { qk } from "../../api/keys";
import { useStreamEvents } from "../../api/streamHooks";
import {
  concernsConversation,
  LIVE_EVENT_TYPES,
  LIVE_REFETCH_MS,
  type WatchedRun,
} from "./live";

/**
 * Keep the transcript of a flow conversation current while its run goes on:
 * an event of the run refetches it, once per `LIVE_REFETCH_MS` at most. (A
 * chat's transcript is updated in place by pages/assist/useAssistLive.ts.)
 * `stream.resync` already refetches everything.
 */
export function useConversationLive(
  conversationId: string,
  run: WatchedRun,
): void {
  const qc = useQueryClient();
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  useStreamEvents(LIVE_EVENT_TYPES, (e) => {
    if (timer.current !== undefined || !concernsConversation(e, run)) return;
    timer.current = setTimeout(() => {
      timer.current = undefined;
      void qc.invalidateQueries({
        queryKey: qk("conversation-messages", conversationId),
      });
    }, LIVE_REFETCH_MS);
  });
}
