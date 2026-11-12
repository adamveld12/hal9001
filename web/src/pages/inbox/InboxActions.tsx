import { useState } from "react";
import { errorMessage } from "../../api/client";
import type { InboxItem } from "../../api/inbox";
import {
  useAnswerInboxItem,
  useReplyInboxItem,
  useRunInboxAction,
} from "../../api/inboxActions";
import { Button } from "../../components/ui";
import { useAssist } from "../assist/context";
import { ActionButtons } from "./ActionButtons";
import {
  buildAnswerRequest,
  controlsFor,
  type Selection,
  type Selections,
} from "./interactions";
import { InteractionStatus } from "./InteractionStatus";
import { QuestionCards } from "./QuestionCards";
import { ReplyBox } from "./ReplyBox";

/** Questions, next steps, reply box and interaction status of a note an agent wrote. Render with `key={item.id}`. */
export function InboxActions({ item }: { item: InboxItem }) {
  const { openSession } = useAssist();
  const answer = useAnswerInboxItem();
  const run = useRunInboxAction();
  const reply = useReplyInboxItem();
  const [selections, setSelections] = useState<Selections>({});

  if (
    !item.can_reply &&
    item.questions.length === 0 &&
    item.actions.length === 0 &&
    item.interactions.length === 0
  ) {
    return null;
  }

  const pending = answer.isPending || run.isPending || reply.isPending;
  const controls = controlsFor(item, pending);
  const error = answer.error ?? run.error ?? reply.error;
  const conversationId = item.conversation_id;
  const openChat = () => {
    if (conversationId) openSession(conversationId);
  };

  const onChange = (id: string, next: Selection) =>
    setSelections((s) => ({ ...s, [id]: next }));

  const sendAnswers = () => {
    reply.reset();
    run.reset();
    answer.mutate(
      { id: item.id, body: buildAnswerRequest(item.questions, selections) },
      { onSuccess: () => setSelections({}) },
    );
  };

  const runAction = (index: number) => {
    answer.reset();
    reply.reset();
    run.mutate({ id: item.id, index });
  };

  const sendReply = (text: string) => {
    answer.reset();
    run.reset();
    return reply.mutateAsync({ id: item.id, text }).then(
      () => true,
      () => false,
    );
  };

  return (
    <section className="space-y-3" aria-label="Agent follow-up">
      <QuestionCards
        questions={item.questions}
        selections={selections}
        onChange={onChange}
        onSend={sendAnswers}
        disabled={!controls.answers}
        pending={answer.isPending}
      />
      <ActionButtons
        actions={item.actions}
        disabled={!controls.actions}
        canOpenChat={conversationId !== null}
        onRun={runAction}
        onOpenChat={openChat}
      />
      {item.can_reply && (
        <ReplyBox
          disabled={!controls.reply}
          pending={reply.isPending}
          onSend={sendReply}
        />
      )}
      {error && (
        <p role="alert" className="m-0 text-xs text-bad">
          {errorMessage(error)}
        </p>
      )}
      <InteractionStatus interactions={item.interactions} />
      {conversationId && (
        <Button icon="chat" onClick={openChat}>
          Open chat
        </Button>
      )}
    </section>
  );
}
