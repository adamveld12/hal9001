import { yamlScalar } from "../files/yaml";

export type PromptOutput = "note" | "none";

export interface PromptForm {
  name: string;
  description: string;
  /** `note`: structured inbox note (§7.4); `none`: free text. */
  output: PromptOutput;
  /** Go text/template body. */
  body: string;
}

export const EMPTY_PROMPT_FORM: PromptForm = {
  name: "",
  description: "",
  output: "none",
  body: "",
};

const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

export type PromptFormErrors = Partial<Record<"name", string>>;

export function validatePromptForm(
  f: PromptForm,
  existingNames: readonly string[] = [],
): PromptFormErrors {
  if (f.name === "") return { name: "Required." };
  if (!SLUG.test(f.name)) {
    return { name: "Use letters, digits, - and _ (max 64)." };
  }
  if (existingNames.includes(f.name)) {
    return { name: "A prompt with this name exists." };
  }
  return {};
}

/** Prompt file text (§7.1): front matter, then the body. An empty description is left out. */
export function buildPromptMarkdown(f: PromptForm): string {
  const front = ["---", `name: ${yamlScalar(f.name)}`];
  const description = f.description.trim();
  if (description !== "") front.push(`description: ${yamlScalar(description)}`);
  front.push(`output: ${f.output}`, "---");
  const body = f.body.replaceAll("\r\n", "\n").replace(/\n+$/, "");
  return `${front.join("\n")}\n${body === "" ? "" : `${body}\n`}`;
}
