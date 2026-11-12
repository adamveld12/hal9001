import { describe, expect, it } from "vitest";
import type { InboxItem } from "../../api/inbox";
import type { InboxInteraction, InboxQuestion } from "../../api/inboxActions";
import {
  answerSummary,
  approvalPath,
  buildAnswerRequest,
  canSend,
  controlsFor,
  EMPTY_SELECTION,
  sendLabel,
  setOtherText,
  statusChip,
  statusDetail,
  toggleChoice,
  toggleOther,
  type Selections,
} from "./interactions";

const opts = ["Merge commit", "Squash", "Rebase"].map((label) => ({
  label,
  description: "",
}));

function q(id: string, over: Partial<InboxQuestion> = {}): InboxQuestion {
  return {
    id,
    question: id,
    header: "",
    options: opts,
    recommended: null,
    multi: false,
    answer: null,
    ...over,
  };
}

function inter(over: Partial<InboxInteraction> = {}): InboxInteraction {
  return {
    id: "01",
    kind: "reply",
    action_index: null,
    summary: "Reply",
    message: "hi",
    status: "done",
    approval_id: null,
    result: null,
    error: null,
    created_at: "",
    updated_at: "",
    ...over,
  } as InboxInteraction;
}

function item(over: Partial<InboxItem> = {}): InboxItem {
  return {
    questions: [],
    actions: [],
    interactions: [],
    can_reply: true,
    ...over,
  } as unknown as InboxItem;
}

describe("selection", () => {
  it("single choice: an option replaces the previous one and Other", () => {
    const s = q("a");
    let sel = toggleChoice(s, EMPTY_SELECTION, "Squash");
    sel = toggleChoice(s, sel, "Rebase");
    expect(sel.choices).toEqual(["Rebase"]);
    sel = setOtherText(s, sel, "custom");
    expect(sel).toMatchObject({ choices: [], otherOn: true });
    sel = toggleChoice(s, sel, "Squash");
    expect(sel).toMatchObject({ choices: ["Squash"], otherOn: false });
  });

  it("multi choice: options toggle in listed order and Other is independent", () => {
    const m = q("a", { multi: true });
    let sel = toggleChoice(m, EMPTY_SELECTION, "Rebase");
    sel = toggleChoice(m, sel, "Merge commit");
    expect(sel.choices).toEqual(["Merge commit", "Rebase"]);
    sel = toggleOther(m, sel);
    expect(sel.otherOn).toBe(true);
    sel = toggleChoice(m, sel, "Rebase");
    expect(sel).toMatchObject({ choices: ["Merge commit"], otherOn: true });
  });
});

describe("buildAnswerRequest", () => {
  const questions = [
    q("one"),
    q("two", { multi: true }),
    q("three", {
      answer: { question_id: "three", choices: ["Squash"], other: null },
    }),
    q("four"),
  ];

  it("includes valid answers of unanswered questions in question order", () => {
    const selections: Selections = {
      two: { choices: ["Squash", "Rebase"], otherOn: true, otherText: "  x  " },
      one: { choices: ["Squash"], otherOn: false, otherText: "" },
      three: { choices: ["Rebase"], otherOn: false, otherText: "" },
    };
    expect(buildAnswerRequest(questions, selections)).toEqual({
      answers: [
        { question_id: "one", choices: ["Squash"] },
        { question_id: "two", choices: ["Squash", "Rebase"], other: "x" },
      ],
    });
  });

  it("skips a blank Other and an empty selection", () => {
    const selections: Selections = {
      one: { choices: [], otherOn: true, otherText: "   " },
    };
    expect(buildAnswerRequest(questions, selections).answers).toEqual([]);
  });

  it("sends only Other text for a question answered with Other", () => {
    const selections: Selections = {
      one: { choices: [], otherOn: true, otherText: "Fast-forward" },
    };
    expect(buildAnswerRequest(questions, selections).answers).toEqual([
      { question_id: "one", other: "Fast-forward" },
    ]);
  });

  it("ignores an unchecked Other box that still has text", () => {
    const selections: Selections = {
      one: { choices: ["Squash"], otherOn: false, otherText: "stale" },
    };
    expect(buildAnswerRequest(questions, selections).answers).toEqual([
      { question_id: "one", choices: ["Squash"] },
    ]);
  });
});

describe("send button", () => {
  const questions = [
    q("a"),
    q("b"),
    q("c", { answer: { question_id: "c", choices: ["Squash"], other: null } }),
  ];
  const one: Selections = {
    a: { choices: ["Squash"], otherOn: false, otherText: "" },
  };

  it("counts ready answers out of the unanswered questions", () => {
    expect(sendLabel(questions, {})).toBe("Send answers");
    expect(sendLabel(questions, one)).toBe("Send answers (1 of 2)");
  });

  it("is enabled only with an answer and when not busy", () => {
    expect(canSend(questions, {}, false)).toBe(false);
    expect(canSend(questions, one, false)).toBe(true);
    expect(canSend(questions, one, true)).toBe(false);
  });
});

describe("answerSummary", () => {
  it("lists labels then the Other text", () => {
    expect(
      answerSummary({ question_id: "a", choices: ["X", "Y"], other: "z" }),
    ).toBe("X, Y, Other: z");
    expect(answerSummary({ question_id: "a", choices: [], other: "z" })).toBe(
      "Other: z",
    );
    expect(
      answerSummary({ question_id: "a", choices: ["X"], other: null }),
    ).toBe("X");
  });
});

describe("status", () => {
  it("maps each status to a chip", () => {
    expect(statusChip(inter({ status: "running" }))).toEqual({
      label: "Running…",
      tone: "info",
    });
    expect(statusChip(inter({ status: "waiting_for_approval" })).label).toBe(
      "Waiting for approval",
    );
    expect(statusChip(inter({ status: "done" })).tone).toBe("ok");
    expect(statusChip(inter({ status: "failed" })).tone).toBe("bad");
  });

  it("shows the result when done and the error when failed", () => {
    expect(statusDetail(inter({ status: "done", result: "Merged." }))).toBe(
      "Merged.",
    );
    expect(statusDetail(inter({ status: "failed", error: "boom" }))).toBe(
      "boom",
    );
    expect(statusDetail(inter({ status: "running", result: "x" }))).toBe("");
  });

  it("links the approval only while waiting for it", () => {
    expect(
      approvalPath(
        inter({ status: "waiting_for_approval", approval_id: "AP1" }),
      ),
    ).toBe("/inbox/approval/AP1");
    expect(
      approvalPath(inter({ status: "done", approval_id: "AP1" })),
    ).toBeNull();
  });
});

describe("controlsFor", () => {
  it("enables everything for an idle agent note", () => {
    expect(controlsFor(item(), false)).toEqual({
      answers: true,
      actions: true,
      reply: true,
    });
  });

  it("an active interaction or a pending request disables everything", () => {
    for (const status of ["running", "waiting_for_approval"] as const) {
      expect(
        controlsFor(item({ interactions: [inter({ status })] }), false),
      ).toEqual({ answers: false, actions: false, reply: false });
    }
    expect(controlsFor(item(), true).reply).toBe(false);
  });

  it("finished interactions do not block; reply needs can_reply", () => {
    const done = item({
      interactions: [inter({ status: "done" }), inter({ status: "failed" })],
      can_reply: false,
    });
    expect(controlsFor(done, false)).toEqual({
      answers: true,
      actions: true,
      reply: false,
    });
  });
});
