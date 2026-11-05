import type { PromptSummary } from "../../api/prompts";

export interface PromptRow {
  name: string;
  description: string;
  /** `none` when the file does not parse or does not say. */
  output: "note" | "none";
  errors: number;
  warnings: number;
  /** The file does not parse as a prompt. */
  broken: boolean;
}

export function promptRow(p: PromptSummary): PromptRow {
  return {
    name: p.name,
    description: p.prompt?.description ?? "",
    output: p.prompt?.output ?? "none",
    errors: p.diagnostics.filter((d) => d.severity === "error").length,
    warnings: p.diagnostics.filter((d) => d.severity === "warning").length,
    broken: p.prompt === null,
  };
}
