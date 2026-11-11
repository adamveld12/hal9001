import type { AgentNode } from "./model";

/** Prompt text of a new agent node, so it validates right away. */
export const DEFAULT_INSTRUCTIONS =
  "Look at this item and say what needs attention.\n\n{{.input}}\n";

export type PromptSource = "inline" | "library";

/** Where the node's prompt lives: a saved prompt only when `prompt` is set. */
export function promptSource(node: AgentNode): PromptSource {
  return node.instructions === undefined && node.prompt ? "library" : "inline";
}

/** Switch the node to inline instructions (keeps earlier text, else the default). */
export function toInlinePrompt(node: AgentNode, text?: string): AgentNode {
  const { prompt: _p, ...rest } = node;
  return {
    ...rest,
    instructions: text ?? (node.instructions || DEFAULT_INSTRUCTIONS),
    ...(node.ports ? {} : { output: node.output ?? "note" }),
  };
}

/** Switch the node to a saved prompt (`name` may be empty until one is chosen). */
export function toSavedPrompt(node: AgentNode, name = ""): AgentNode {
  const { instructions: _i, output: _o, ...rest } = node;
  return name === "" ? { ...rest } : { ...rest, prompt: name };
}

/** `{{.input.x}}` hints for the fields of a sample (paths from discoverFields). */
export function templateHints(paths: readonly string[], max = 12): string[] {
  return paths
    .filter((p) => p !== "." && !p.includes("[]"))
    .slice(0, max)
    .map((p) => `{{.input.${p}}}`);
}
