import type { InboxItem } from "../../api/inbox";
import type {
  InboxAnswerRequest,
  InboxInteraction,
  InboxQuestion,
} from "../../api/inboxActions";
import type { Tone } from "../../lib/status";

/** What the user has picked in one question card, not yet sent. */
export interface Selection {
  /** Chosen option labels, in the order the options are listed. */
  choices: string[];
  /** The "Other" choice is selected. */
  otherOn: boolean;
  otherText: string;
}

export const EMPTY_SELECTION: Selection = {
  choices: [],
  otherOn: false,
  otherText: "",
};

/** Selection state of all question cards, by question id. */
export type Selections = Record<string, Selection>;

export function selectionOf(s: Selections, id: string): Selection {
  return s[id] ?? EMPTY_SELECTION;
}

/** Pick an option: replaces the choice (and Other) of a single-choice question, toggles it in a multi question. */
export function toggleChoice(
  q: InboxQuestion,
  cur: Selection,
  label: string,
): Selection {
  if (!q.multi) return { ...cur, choices: [label], otherOn: false };
  const has = cur.choices.includes(label);
  const picked = has
    ? cur.choices.filter((c) => c !== label)
    : [...cur.choices, label];
  return {
    ...cur,
    choices: q.options.map((o) => o.label).filter((l) => picked.includes(l)),
  };
}

/** Select Other: it replaces the options of a single-choice question and toggles in a multi question. */
export function toggleOther(q: InboxQuestion, cur: Selection): Selection {
  if (!q.multi) return { ...cur, choices: [], otherOn: true };
  return { ...cur, otherOn: !cur.otherOn };
}

/** Typing in the Other box selects Other. */
export function setOtherText(
  q: InboxQuestion,
  cur: Selection,
  text: string,
): Selection {
  const base = q.multi ? cur : { ...cur, choices: [] };
  return { ...base, otherOn: true, otherText: text };
}

type Answer = InboxAnswerRequest["answers"][number];

/** The answer for one question, or null when the selection is not a valid answer yet. */
export function answerFor(q: InboxQuestion, sel: Selection): Answer | null {
  if (q.answer) return null;
  const other = sel.otherOn ? sel.otherText.trim() : "";
  const choices = sel.choices.filter((c) =>
    q.options.some((o) => o.label === c),
  );
  if (!q.multi && choices.length + (other ? 1 : 0) !== 1) return null;
  if (choices.length === 0 && other === "") return null;
  const a: Answer = { question_id: q.id };
  if (choices.length > 0) a.choices = choices;
  if (other) a.other = other;
  return a;
}

/** One answer per unanswered question that has a valid selection, in question order. */
export function buildAnswerRequest(
  questions: readonly InboxQuestion[],
  selections: Selections,
): InboxAnswerRequest {
  const answers: Answer[] = [];
  for (const q of questions) {
    const a = answerFor(q, selectionOf(selections, q.id));
    if (a) answers.push(a);
  }
  return { answers };
}

export function unansweredCount(questions: readonly InboxQuestion[]): number {
  return questions.filter((q) => !q.answer).length;
}

/** An interaction is still being worked on. */
export function isActive(i: InboxInteraction): boolean {
  return i.status === "running" || i.status === "waiting_for_approval";
}

export function hasActiveInteraction(item: InboxItem): boolean {
  return item.interactions.some(isActive);
}

export function canSend(
  questions: readonly InboxQuestion[],
  selections: Selections,
  busy: boolean,
): boolean {
  return !busy && buildAnswerRequest(questions, selections).answers.length > 0;
}

/** "Send answers", or "Send answers (2 of 3)" while some questions are unanswered and some are ready. */
export function sendLabel(
  questions: readonly InboxQuestion[],
  selections: Selections,
): string {
  const n = buildAnswerRequest(questions, selections).answers.length;
  if (n === 0) return "Send answers";
  return `Send answers (${n} of ${unansweredCount(questions)})`;
}

/** The locked answer of a question as text: chosen labels, then `Other: <text>`. */
export function answerSummary(a: NonNullable<InboxQuestion["answer"]>): string {
  const parts = [...a.choices];
  const other = a.other?.trim();
  if (other) parts.push(`Other: ${other}`);
  return parts.join(", ");
}

/** What the controls of a note may do right now. */
export interface Controls {
  /** Sending answers. */
  answers: boolean;
  /** Running an action that was not used yet (a used action opens the chat instead). */
  actions: boolean;
  reply: boolean;
}

/** An active interaction, or a request in flight, disables everything. */
export function controlsFor(item: InboxItem, pending: boolean): Controls {
  const free = !pending && !hasActiveInteraction(item);
  return { answers: free, actions: free, reply: free && item.can_reply };
}

export interface StatusChip {
  label: string;
  tone: Tone;
}

export function statusChip(i: InboxInteraction): StatusChip {
  switch (i.status) {
    case "running":
      return { label: "Running…", tone: "info" };
    case "waiting_for_approval":
      return { label: "Waiting for approval", tone: "warn" };
    case "done":
      return { label: "Done", tone: "ok" };
    case "failed":
      return { label: "Failed", tone: "bad" };
  }
}

/** The text after the chip: the result of a finished interaction, the error of a failed one. */
export function statusDetail(i: InboxInteraction): string {
  if (i.status === "done") return i.result ?? "";
  if (i.status === "failed") return i.error ?? "";
  return "";
}

/** Route of the approval an interaction waits for. */
export function approvalPath(i: InboxInteraction): string | null {
  return i.status === "waiting_for_approval" && i.approval_id
    ? `/inbox/approval/${i.approval_id}`
    : null;
}

/** The note has questions nobody answered yet (an optional list hint). */
export function needsAnswer(item: InboxItem): boolean {
  return item.questions.some((q) => !q.answer);
}
