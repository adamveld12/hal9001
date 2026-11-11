import { Link } from "react-router";
import {
  useSendAssistMessage,
  type AssistSessionDetail,
} from "../../api/assist";
import { useConversationSandboxLog } from "../../api/runs";
import { Button } from "../../components/ui";
import { Composer } from "./Composer";
import { StarterPrompts } from "./ChatParts";
import { useAssist } from "./context";
import { FlowSavePanel } from "./FlowSavePanel";
import { FlowTestPanel } from "./FlowTestPanel";
import { MessageList } from "./MessageList";
import { SensorDraftCards } from "./SensorDraftCards";
import { isBusy } from "./sessions";
import { targetConfig, targetPath } from "./targets";
import { TestPanel } from "./TestPanel";

/**
 * The conversation of one assist session and its message box: the part of a
 * chat that the Ask AI drawer and the Chat page share. The caller shows the
 * header. `placement` says where it is shown: the drawer follows the target it
 * was opened for (a draft that was just saved) and has the draft's Save and
 * Test cards; the page uses the session's own target and, for a draft session,
 * points to the editor and the drawer instead of showing those cards (they act
 * on the drawer's target and the open editor).
 * `onNewChat` is what `New chat` and `/new` do there.
 */
export function ChatThread({
  session: s,
  placement,
  onNewChat,
}: {
  session: AssistSessionDetail;
  placement: "drawer" | "page";
  onNewChat: () => void;
}) {
  const assist = useAssist();
  const send = useSendAssistMessage();
  const sandbox = useConversationSandboxLog(s.id);
  const id = s.id;
  const inDrawer = placement === "drawer";
  const busy = isBusy(s.status);
  const closed = s.status === "closed";
  const kind = s.target.kind;
  const cfg = targetConfig(kind);
  // The saved id once a new target was saved; the session's own until then.
  const targetId =
    inDrawer && assist.target.kind === kind ? assist.target.id : s.target.id;
  const editorPath = targetPath(kind, targetId);
  const elsewhere = !closed && !assist.editorActive ? editorPath : null;
  const draftCards = inDrawer && !closed && kind !== "general";

  return (
    <>
      {elsewhere !== null && (
        <div
          role="status"
          className="border-b border-line bg-raised px-3 py-1.5 text-xs"
        >
          This session edits{" "}
          {targetId === null ? `a new ${cfg.noun}` : `${cfg.noun} ${targetId}`}.{" "}
          <Link className="text-accent underline" to={elsewhere}>
            Open {targetId === null ? `the new ${cfg.noun} draft` : targetId}
          </Link>{" "}
          to see its draft in the editor
          {inDrawer ? "." : ", or go back to the drawer to save and test it."}
        </div>
      )}
      <MessageList
        messages={s.messages}
        conversationId={id}
        sandbox={sandbox.data}
        onLoadEarlierSandbox={sandbox.loadEarlier}
        tail={
          !draftCards ? null : kind === "flow" ? (
            <>
              <SensorDraftCards sessionId={id} />
              <FlowTestPanel sessionId={id} />
            </>
          ) : (
            <TestPanel sessionId={id} kind={kind} />
          )
        }
      />
      {s.messages.length === 0 && !closed && (
        <div className="px-3 pb-3">
          <StarterPrompts kind={kind} targetId={s.target.id} />
        </div>
      )}
      {s.status === "failed" && s.error && (
        <div
          role="alert"
          className="border-t border-bad/30 bg-bad/5 px-3 py-2 text-xs text-bad"
        >
          The last turn failed: {s.error}
        </div>
      )}
      {closed ? (
        <div className="space-y-2 border-t border-line p-3 text-xs text-muted">
          This session has ended.
          <div>
            <Button onClick={onNewChat}>Start a new chat</Button>
          </div>
        </div>
      ) : (
        <>
          {kind === "flow" && inDrawer && (
            <div className="max-h-48 overflow-auto">
              <FlowSavePanel sessionId={id} />
            </div>
          )}
          <div className="border-t border-line p-3" data-chat-composer="">
            <Composer
              disabled={busy}
              placeholder={
                busy ? "Waiting for the agent…" : "Message the agent…"
              }
              prefill={assist.seed}
              onPrefillApplied={assist.consumeSeed}
              autoFocus
              onNewChat={onNewChat}
              onSend={async (text) => {
                await assist.editor()?.flush();
                await send.mutateAsync({
                  id,
                  text,
                  yaml: await assist.editor()?.getText(),
                  page_context: assist.pageContext(),
                });
              }}
            />
          </div>
        </>
      )}
    </>
  );
}
