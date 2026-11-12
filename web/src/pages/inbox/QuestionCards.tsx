import type { InboxQuestion } from "../../api/inboxActions";
import { Badge, Button, Panel, TextInput } from "../../components/ui";
import {
  answerSummary,
  canSend,
  selectionOf,
  sendLabel,
  setOtherText,
  toggleChoice,
  toggleOther,
  type Selection,
  type Selections,
} from "./interactions";

function QuestionHead({ q }: { q: InboxQuestion }) {
  return (
    <div className="space-y-1">
      {q.header && <Badge tone="info">{q.header}</Badge>}
      <p className="m-0 text-[13px] font-medium">{q.question}</p>
    </div>
  );
}

function LockedQuestion({ q }: { q: InboxQuestion }) {
  return (
    <div className="space-y-1">
      <QuestionHead q={q} />
      <p className="m-0 text-xs">
        <span className="text-muted">Answered: </span>
        {q.answer ? answerSummary(q.answer) : ""}
      </p>
    </div>
  );
}

function OpenQuestion({
  q,
  selections,
  onChange,
  disabled,
}: {
  q: InboxQuestion;
  selections: Selections;
  onChange: (id: string, next: Selection) => void;
  disabled: boolean;
}) {
  const sel = selectionOf(selections, q.id);
  const type = q.multi ? "checkbox" : "radio";
  const row = "flex cursor-pointer items-start gap-2 text-xs";
  return (
    <fieldset className="m-0 space-y-1.5 border-0 p-0" disabled={disabled}>
      <legend className="p-0">
        <QuestionHead q={q} />
      </legend>
      {q.options.map((o, i) => (
        <label key={`${i}:${o.label}`} className={row}>
          <input
            type={type}
            name={`q-${q.id}`}
            className="mt-0.5"
            checked={sel.choices.includes(o.label)}
            onChange={() => onChange(q.id, toggleChoice(q, sel, o.label))}
          />
          <span className="min-w-0">
            <span className="font-medium">{o.label}</span>
            {q.recommended === i && (
              <Badge tone="ok" className="ml-1.5">
                Recommended
              </Badge>
            )}
            {o.description && (
              <span className="block text-muted">{o.description}</span>
            )}
          </span>
        </label>
      ))}
      <div className={row}>
        <input
          type={type}
          name={`q-${q.id}`}
          aria-label="Other"
          className="mt-1.5"
          checked={sel.otherOn}
          onChange={() => onChange(q.id, toggleOther(q, sel))}
        />
        <TextInput
          aria-label={`Other answer for ${q.question}`}
          placeholder="Other…"
          value={sel.otherText}
          onChange={(e) => onChange(q.id, setOtherText(q, sel, e.target.value))}
        />
      </div>
    </fieldset>
  );
}

/** One card per question. Answered questions are locked; the rest share one send button. */
export function QuestionCards({
  questions,
  selections,
  onChange,
  onSend,
  disabled,
  pending,
}: {
  questions: readonly InboxQuestion[];
  selections: Selections;
  onChange: (id: string, next: Selection) => void;
  onSend: () => void;
  /** An interaction is running or a request is in flight. */
  disabled: boolean;
  pending: boolean;
}) {
  if (questions.length === 0) return null;
  const open = questions.some((q) => !q.answer);
  return (
    <Panel title="Questions from the agent">
      <div className="flex flex-col gap-4">
        {questions.map((q) =>
          q.answer ? (
            <LockedQuestion key={q.id} q={q} />
          ) : (
            <OpenQuestion
              key={q.id}
              q={q}
              selections={selections}
              onChange={onChange}
              disabled={disabled}
            />
          ),
        )}
        {open && (
          <div className="pt-1">
            <Button
              variant="primary"
              icon="check"
              disabled={!canSend(questions, selections, disabled)}
              onClick={onSend}
            >
              {pending ? "Sending…" : sendLabel(questions, selections)}
            </Button>
          </div>
        )}
      </div>
    </Panel>
  );
}
