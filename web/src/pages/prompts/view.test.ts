import { describe, expect, it } from "vitest";
import type { PromptSummary } from "../../api/prompts";
import { promptRow } from "./view";

const base: PromptSummary = {
  name: "pr-review",
  path: "prompts/pr-review.md",
  hash: "a".repeat(64),
  prompt: {
    name: "pr-review",
    description: "Review a PR",
    output: "note",
    body: "x",
  },
  diagnostics: [],
};

describe("promptRow", () => {
  it("takes description and output from the parsed prompt", () => {
    expect(promptRow(base)).toMatchObject({
      description: "Review a PR",
      output: "note",
      errors: 0,
      broken: false,
    });
  });

  it("defaults output to none and counts findings by severity", () => {
    const row = promptRow({
      ...base,
      prompt: { name: "pr-review", body: "x" },
      diagnostics: [
        { severity: "error", code: "invalid_template", message: "m", line: 4 },
        { severity: "warning", code: "x", message: "w" },
        { severity: "warning", code: "x", message: "w" },
      ],
    });
    expect(row).toMatchObject({ output: "none", errors: 1, warnings: 2 });
  });

  it("marks a file that does not parse", () => {
    expect(promptRow({ ...base, prompt: null })).toMatchObject({
      broken: true,
      description: "",
      output: "none",
    });
  });
});
