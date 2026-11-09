// Logic behind the edit-before-approve form: the text of each editable box, and the edits an approval sends.

export interface Editable {
  key: string;
  value: unknown;
}

/** Strings are edited as plain text; everything else (objects, arrays, numbers, null) as JSON. */
export function editorKind(value: unknown): "text" | "json" {
  return typeof value === "string" ? "text" : "json";
}

/** The box text a value starts with: the string itself, or pretty-printed JSON. */
export function draftText(value: unknown): string {
  return typeof value === "string"
    ? value
    : (JSON.stringify(value ?? null, null, 2) ?? "null");
}

/** Box text for every editable field, keyed by path. */
export function initialDrafts(
  editable: readonly Editable[],
): Record<string, string> {
  return Object.fromEntries(editable.map((e) => [e.key, draftText(e.value)]));
}

export interface BuiltEdits {
  /** Only the paths whose value differs from the current one. */
  edits: Record<string, unknown>;
  /** Paths whose JSON box does not parse, with the parser's message. */
  errors: Record<string, string>;
}

/** The edits `drafts` make against `editable`; a box left as it was is not an edit. */
export function buildEdits(
  editable: readonly Editable[],
  drafts: Readonly<Record<string, string>>,
): BuiltEdits {
  const edits: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  for (const { key, value } of editable) {
    const text = drafts[key];
    if (text === undefined || text === draftText(value)) continue;
    if (editorKind(value) === "text") {
      edits[key] = text;
      continue;
    }
    try {
      const parsed: unknown = JSON.parse(text);
      if (JSON.stringify(parsed) !== JSON.stringify(value ?? null)) {
        edits[key] = parsed;
      }
    } catch (e) {
      errors[key] = e instanceof Error ? e.message : "not valid JSON";
    }
  }
  return { edits, errors };
}
