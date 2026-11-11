import { describe, expect, it } from "vitest";
import {
  DEFAULT_INSTRUCTIONS,
  promptSource,
  templateHints,
  toInlinePrompt,
  toSavedPrompt,
} from "./agentPrompt";
import { newNode, type AgentNode } from "./model";

const fresh = () => newNode("agent", "a", { x: 0, y: 0 }) as AgentNode;

describe("agent prompt source", () => {
  it("new nodes start inline with the default text and a note output", () => {
    const n = fresh();
    expect(promptSource(n)).toBe("inline");
    expect(n).toMatchObject({
      instructions: DEFAULT_INSTRUCTIONS,
      output: "note",
    });
    expect(n.prompt).toBeUndefined();
  });

  it("switching to a saved prompt drops instructions and output", () => {
    const n = toSavedPrompt({ ...fresh(), extra_prompt: "x" }, "pr-review");
    expect(promptSource(n)).toBe("library");
    expect(n).toMatchObject({ prompt: "pr-review", extra_prompt: "x" });
    expect(n.instructions).toBeUndefined();
    expect(n.output).toBeUndefined();
  });

  it("switching back to inline removes the prompt and restores the text", () => {
    const inline = { ...fresh(), instructions: "mine" };
    const lib = toSavedPrompt(inline, "p");
    const back = toInlinePrompt(lib);
    expect(back.prompt).toBeUndefined();
    expect(back).toMatchObject({
      instructions: DEFAULT_INSTRUCTIONS,
      output: "note",
    });
    expect(toInlinePrompt(lib, "saved text").instructions).toBe("saved text");
  });

  it("hints list scalar-path input fields only", () => {
    expect(
      templateHints([".", "title", "labels[]", "labels[].name", "user.login"]),
    ).toEqual(["{{.input.title}}", "{{.input.user.login}}"]);
  });

  it("inline prompts of a node with ports get no output", () => {
    const n = { ...fresh(), ports: { done: {} } } as AgentNode;
    delete n.output;
    expect(toInlinePrompt(toSavedPrompt(n, "p")).output).toBeUndefined();
  });
});
