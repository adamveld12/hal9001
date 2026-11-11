import { useEffect } from "react";
import { Link, Navigate, useLocation, useNavigate } from "react-router";
import { useAssistSession, useCancelAssistTurn } from "../../api/assist";
import { ApiError, errorMessage } from "../../api/client";
import {
  useConversation,
  useConversationMessages,
  useForkConversation,
  type ConversationDetail,
} from "../../api/conversations";
import { useConversationSandboxLog } from "../../api/runs";
import {
  Button,
  EmptyState,
  ErrorState,
  LoadingState,
} from "../../components/ui";
import { noFocusOnClick } from "../../lib/focus";
import { ChatStart } from "../assist/ChatStart";
import { ChatThread } from "../assist/ChatThread";
import { useAssist } from "../assist/context";
import { GENERAL_TARGET } from "../assist/drawerState";
import { MessageList } from "../assist/MessageList";
import { isBusy } from "../assist/sessions";
import { useAssistLive } from "../assist/useAssistLive";
import { ConversationHeader } from "./ConversationHeader";
import { chatPath, NEW_CHAT_ID } from "./path";
import { canContinue, isLive } from "./status";
import { useConversationLive } from "./useConversationLive";

/** Nothing is selected: say what to do. */
export function NothingSelected() {
  const { search } = useLocation();
  return (
    <EmptyState
      title="Select a conversation"
      hint="Pick one on the left to read it. Finished flow runs can be continued in a chat."
      action={
        <Link
          to={{ pathname: chatPath(NEW_CHAT_ID), search }}
          className="inline-flex h-7 items-center rounded-md border border-line bg-panel px-2.5 text-xs font-medium hover:bg-raised"
        >
          Start a chat
        </Link>
      }
    />
  );
}

/** The start view of a new chat; the chat opens here once its first message was accepted. */
export function NewChatPane() {
  const navigate = useNavigate();
  const { search } = useLocation();
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ChatStart
        target={GENERAL_TARGET}
        onStarted={(id) =>
          void navigate({ pathname: chatPath(id), search }, { replace: true })
        }
        onNewChat={() => undefined}
      />
    </div>
  );
}

/** One conversation: its header, transcript and, for a chat, the message box. */
export function ConversationPane({
  id,
  onLoaded,
}: {
  id: string;
  /** The conversation once it has loaded (for the page context). */
  onLoaded: (c: ConversationDetail | null) => void;
}) {
  const q = useConversation(id);
  const { search } = useLocation();
  const c = q.data;
  useEffect(() => {
    onLoaded(c ?? null);
    return () => onLoaded(null);
  }, [c, onLoaded]);

  if (q.isPending) return <LoadingState label="Loading conversation" />;
  if (!c) {
    const gone = q.error instanceof ApiError && q.error.status === 404;
    return gone ? (
      <EmptyState
        title="This conversation no longer exists"
        hint="It may have been ended or its run deleted."
      />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  }
  // A propose-phase run resolves to the conversation of its execute phase.
  if (c.id !== id) {
    return <Navigate to={{ pathname: chatPath(c.id), search }} replace />;
  }
  return c.source === "chat" ? (
    <ChatConversation c={c} />
  ) : (
    <FlowConversation c={c} />
  );
}

function ChatConversation({ c }: { c: ConversationDetail }) {
  const assist = useAssist();
  const { registerChatPage, returnToDrawer } = assist;
  const navigate = useNavigate();
  const { search } = useLocation();
  const id = c.id;
  const q = useAssistSession(id);
  const cancel = useCancelAssistTurn(id);
  useAssistLive(id);
  useEffect(() => {
    registerChatPage(id);
    return () => registerChatPage(null);
  }, [registerChatPage, id]);

  const s = q.data;
  if (q.isPending) return <LoadingState label="Loading chat" />;
  if (!s) {
    return q.error instanceof ApiError && q.error.status === 404 ? (
      <EmptyState title="This chat no longer exists" />
    ) : (
      <ErrorState error={q.error} onRetry={() => void q.refetch()} />
    );
  }
  const busy = isBusy(s.status);
  return (
    <>
      <ConversationHeader
        c={c}
        status={s.status}
        model={s.model}
        actions={
          <>
            {busy && (
              <Button
                variant="danger"
                icon="stop"
                disabled={cancel.isPending}
                {...noFocusOnClick}
                onClick={() => cancel.mutate()}
              >
                Stop
              </Button>
            )}
            {s.status !== "closed" && (
              <Button
                icon="agent"
                title="Show this chat in the Ask AI drawer and go back to the page you came from"
                onClick={() => returnToDrawer(s)}
              >
                Back to drawer
              </Button>
            )}
          </>
        }
      />
      <ChatThread
        session={s}
        placement="page"
        onNewChat={() =>
          void navigate({ pathname: chatPath(NEW_CHAT_ID), search })
        }
      />
    </>
  );
}

function FlowConversation({ c }: { c: ConversationDetail }) {
  const navigate = useNavigate();
  const { search } = useLocation();
  const messages = useConversationMessages(c.id);
  const sandbox = useConversationSandboxLog(c.id);
  const fork = useForkConversation();
  useConversationLive(c.id, {
    agentRunId: c.agent_run_id,
    nodeRunId: c.node_run_id,
  });
  const live = isLive(c.status);
  const list = messages.data;
  const continuable = canContinue(c);
  const cont = continuable ? (
    <Button
      variant="primary"
      icon="plus"
      disabled={fork.isPending}
      onClick={() =>
        fork.mutate(c.id, {
          onSuccess: (chat) =>
            void navigate({ pathname: chatPath(chat.id), search }),
        })
      }
    >
      {fork.isPending ? "Continuing…" : "Continue in chat"}
    </Button>
  ) : null;

  return (
    <>
      <ConversationHeader c={c} actions={cont} />
      {messages.isPending ? (
        <LoadingState label="Loading transcript" />
      ) : !list ? (
        <ErrorState
          error={messages.error}
          onRetry={() => void messages.refetch()}
        />
      ) : (
        <>
          {list.truncated && (
            <div className="border-b border-line bg-raised px-3 py-1.5 text-xs text-muted">
              Older entries are left out.
              {c.agent_run_id && (
                <>
                  {" "}
                  <Link
                    className="text-accent hover:underline"
                    to={`/agent-runs/${c.agent_run_id}`}
                  >
                    The agent run page
                  </Link>{" "}
                  has the whole transcript.
                </>
              )}
            </div>
          )}
          <MessageList
            messages={list.items}
            runtime={c.runtime}
            source="flow"
            sandbox={sandbox.data}
            onLoadEarlierSandbox={sandbox.loadEarlier}
          />
        </>
      )}
      <div className="space-y-2 border-t border-line p-3 text-xs">
        <p className="m-0 text-muted">
          {live
            ? "This run is still going. You can watch it here and continue it in a chat once it has finished."
            : continuable
              ? "A flow run cannot be changed. Continue it in a chat to ask follow-up questions."
              : "This run cannot be continued."}
        </p>
        {cont}
        {fork.isError && (
          <div role="alert" className="text-bad">
            Could not continue the run: {errorMessage(fork.error)}
          </div>
        )}
      </div>
    </>
  );
}
