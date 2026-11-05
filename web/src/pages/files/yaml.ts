// Minimal YAML emitters for the skeleton builders. Strings that could be read
// as another type are double-quoted (JSON string syntax is valid YAML).

const RESERVED = /^(true|false|yes|no|on|off|y|n|null|~)$/i;
const PLAIN = /^[A-Za-z0-9][A-Za-z0-9_.-]*$/;
const NUMBER_LIKE =
  /^[-+]?(0x[0-9a-f]+|0o[0-7]+|[0-9][0-9_.]*(e[-+]?[0-9]+)?)$/i;

/** A single-line scalar: plain when unambiguous, else double-quoted. */
export function yamlScalar(value: string): string {
  if (PLAIN.test(value) && !RESERVED.test(value) && !NUMBER_LIKE.test(value)) {
    return value;
  }
  return JSON.stringify(value);
}

/**
 * `key: |` block scalar lines for multi-line text. Trailing blank lines are
 * dropped (one final newline is kept by YAML's clip rule). Empty text becomes
 * an empty quoted string.
 */
export function yamlBlock(key: string, text: string): string[] {
  const lines = text.replaceAll("\r\n", "\n").replace(/\n+$/, "").split("\n");
  if (lines.every((l) => l.trim() === "")) return [`${key}: ""`];
  const first = lines.find((l) => l.trim() !== "") ?? "";
  // Content that starts indented needs an explicit indentation indicator.
  const header = /^[ \t]/.test(first) ? "|2" : "|";
  return [`${key}: ${header}`, ...lines.map((l) => (l === "" ? "" : `  ${l}`))];
}

/** `key:` followed by a `- item` per entry. */
export function yamlList(key: string, items: readonly string[]): string[] {
  return [`${key}:`, ...items.map((i) => `  - ${yamlScalar(i)}`)];
}
