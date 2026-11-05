import { describe, expect, it } from "vitest";
import {
  buildPromptMarkdown,
  EMPTY_PROMPT_FORM,
  validatePromptForm,
  type PromptForm,
} from "./skeleton";

const form = (over: Partial<PromptForm>): PromptForm => ({
  ...EMPTY_PROMPT_FORM,
  name: "pr-review",
  ...over,
});

describe("buildPromptMarkdown", () => {
  it("writes the §7.1 layout: front matter, then the body", () => {
    expect(
      buildPromptMarkdown(
        form({
          description: "Review a PR and leave comments",
          output: "note",
          body: "You are reviewing {{.input.subject.url}}.\n\n",
        }),
      ),
    ).toBe(
      [
        "---",
        "name: pr-review",
        'description: "Review a PR and leave comments"',
        "output: note",
        "---",
        "You are reviewing {{.input.subject.url}}.",
        "",
      ].join("\n"),
    );
  });

  it("omits an empty description and an empty body", () => {
    expect(buildPromptMarkdown(form({}))).toBe(
      "---\nname: pr-review\noutput: none\n---\n",
    );
  });

  it("quotes descriptions that YAML would misread", () => {
    expect(
      buildPromptMarkdown(form({ description: "Triage: urgent" })),
    ).toContain('description: "Triage: urgent"');
  });
});

describe("validatePromptForm", () => {
  it("accepts a slug", () => {
    expect(validatePromptForm(form({}))).toEqual({});
  });

  it("rejects empty, malformed, and taken names", () => {
    expect(validatePromptForm(form({ name: "" })).name).toBe("Required.");
    expect(validatePromptForm(form({ name: "a/b" })).name).toMatch(/letters/);
    expect(validatePromptForm(form({}), ["pr-review"]).name).toMatch(/exists/);
  });
});
