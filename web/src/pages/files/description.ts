import { yamlScalar } from "./yaml";

/**
 * Returns the YAML text with its top-level `description:` set to `text` (or
 * removed when `text` is blank). The rest of the file, comments included, stays
 * as it is; a new entry goes below the leading comment block.
 */
export function withDescription(yaml: string, text: string): string {
  const lines = yaml.split("\n");
  const at = lines.findIndex((l) => /^description\s*:/.test(l));
  if (at >= 0) {
    let end = at + 1;
    while (end < lines.length && /^[ \t]+\S/.test(lines[end] ?? "")) end++;
    lines.splice(at, end - at);
  }
  const value = text.trim();
  if (value !== "") {
    let top = 0;
    while (top < lines.length && /^(#.*)?$/.test(lines[top] ?? "")) {
      if (lines[top] === "" && top === lines.length - 1) break;
      top++;
    }
    lines.splice(at >= 0 ? at : top, 0, `description: ${yamlScalar(value)}`);
  }
  return lines.join("\n");
}
