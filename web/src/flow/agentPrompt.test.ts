import { describe, expect, it } from "vitest";
import {
  canSaveAsPrompt,
  DEFAULT_INSTRUCTIONS,
  promptSource,
  templateHints,
  toInlinePrompt,
  toSavedPrompt,
  withOutput,
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

  it("switching to a saved prompt drops output_schema", () => {
    const tool = {
      ...withOutput(fresh(), "tool"),
      output_schema: { type: "object" },
    };
    const n = toSavedPrompt(tool, "p");
    expect(n.output_schema).toBeUndefined();
    expect(n.output).toBeUndefined();
  });

  it("only the tool output keeps output_schema", () => {
    const tool = {
      ...withOutput(fresh(), "tool"),
      output_schema: { type: "object" },
    };
    expect(tool.output).toBe("tool");
    expect(withOutput(tool, "tool").output_schema).toEqual({ type: "object" });
    for (const o of ["note", "none"] as const) {
      const n = withOutput(tool, o);
      expect(n.output).toBe(o);
      expect("output_schema" in n).toBe(false);
    }
  });

  it("tool output cannot be saved as a library prompt", () => {
    expect(canSaveAsPrompt(fresh())).toBe(true);
    expect(canSaveAsPrompt(withOutput(fresh(), "none"))).toBe(true);
    expect(canSaveAsPrompt(withOutput(fresh(), "tool"))).toBe(false);
  });
});
