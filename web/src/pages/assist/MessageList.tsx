import { useEffect, useRef, useState, type ReactNode } from "react";
import type { AssistMessage } from "../../api/assist";
import { Button } from "../../components/ui";
import type { SandboxLogData } from "../runs/sandboxEntries";
import { MessageView } from "./MessageView";
import { traceLinks } from "./messages";
import { SandboxGroup } from "./SandboxGroup";
import {
  isBelowView,
  pendingApprovalMessage,
  shouldFollow,
} from "./scrollFollow";
import { threadItems } from "./threadItems";

/**
 * The transcript of a conversation, scrolled to the end while the user is near
 * it. The content also grows without a new message (a pending approval loads
 * its prompt after the tool call appeared, a test result arrives), so the end
 * is followed by observing both sizes. Approvals render inline; when one is
 * out of view a pill jumps to it. Sandbox log entries sit between the messages
 * by time. `tail` renders after the last item (draft sensor and test cards).
 */
export function MessageList({
  messages,
  runtime,
  source,
  sandbox,
  onLoadEarlierSandbox,
  tail,
}: {
  messages: readonly AssistMessage[];
  runtime: string;
  /** What the messages belong to; a flow run's system messages are notes. Default: a chat. */
  source?: "chat" | "flow";
  /** The conversation's sandbox log, when it has loaded. */
  sandbox?: SandboxLogData;
  onLoadEarlierSandbox?: () => void;
  tail?: ReactNode;
}) {
  const box = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const [pillVisible, setPillVisible] = useState(false);
  const pendingId = pendingApprovalMessage(messages)?.id ?? null;

  const updatePill = () => {
    const el = box.current;
    const target = el?.querySelector("[data-pending-approval]");
    setPillVisible(
      !!el &&
        !!target &&
        isBelowView(
          target.getBoundingClientRect().top,
          el.getBoundingClientRect().bottom,
        ),
    );
  };

  useEffect(() => {
    const el = box.current;
    const inner = content.current;
    if (!el || !inner) return;
    const follow = () => {
      if (atBottom.current) el.scrollTop = el.scrollHeight;
      updatePill();
    };
    const ro = new ResizeObserver(follow);
    ro.observe(inner);
    ro.observe(el);
    follow();
    return () => ro.disconnect();
  }, []);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(updatePill, [pendingId]);

  const traces = traceLinks(messages);
  const items = threadItems(messages, sandbox?.entries ?? []);
  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={box}
        role="log"
        aria-label="Conversation"
        className="min-h-0 flex-1 overflow-auto p-3"
        onScroll={(e) => {
          const el = e.currentTarget;
          atBottom.current = shouldFollow(
            el.scrollHeight,
            el.scrollTop,
            el.clientHeight,
          );
          updatePill();
        }}
      >
        <div ref={content} className="space-y-3">
          {sandbox?.hasMore && onLoadEarlierSandbox && (
            <div className="flex items-center gap-2 text-xs text-muted">
              <span>Older sandbox entries are not shown.</span>
              <Button onClick={onLoadEarlierSandbox}>Load earlier</Button>
            </div>
          )}
          {items.map((item) =>
            item.type === "sandbox" ? (
              <SandboxGroup key={item.key} entries={item.entries} />
            ) : (
              <div
                key={item.message.id}
                data-pending-approval={
                  item.message.id === pendingId ? "" : undefined
                }
              >
                <MessageView
                  message={item.message}
                  runtime={runtime}
                  source={source}
                  traceRunId={traces.get(item.message.id)}
                />
              </div>
            ),
          )}
          {tail}
        </div>
      </div>
      {pillVisible && (
        <button
          type="button"
          className="absolute bottom-2 left-1/2 -translate-x-1/2 rounded-full border border-warn/50 bg-panel px-3 py-1 text-xs font-medium text-warn shadow-md hover:bg-raised"
          onClick={() =>
            box.current
              ?.querySelector("[data-pending-approval]")
              ?.scrollIntoView({ block: "center", behavior: "smooth" })
          }
        >
          Pending approval ↓
        </button>
      )}
    </div>
  );
}
