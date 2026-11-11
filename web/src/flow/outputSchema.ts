/** Default output schema (mirrors Go internal/outschema NoteDoc): the inbox note. */
export const NOTE_SCHEMA: Record<string, unknown> = {
  type: "object",
  required: ["title", "summary_md"],
  properties: {
    title: {
      type: "string",
      pattern: "\\S",
      description: "Short headline of the inbox item.",
    },
    priority: {
      enum: ["low", "normal", "high", "urgent"],
      description: "How soon a person should look. Default normal.",
    },
    summary_md: {
      type: "string",
      description: "Markdown for a reader who has not seen the item.",
    },
    suggested_reply: {
      type: "string",
      description: "Optional reply the person can send.",
    },
    links: {
      type: "array",
      description: "Optional links to the sources.",
      items: {
        type: "object",
        required: ["label", "url"],
        additionalProperties: false,
        properties: { label: { type: "string" }, url: { type: "string" } },
      },
    },
  },
  additionalProperties: false,
};

export const NOTE_SCHEMA_TEXT = JSON.stringify(NOTE_SCHEMA, null, 2);

export type SchemaParse =
  | { ok: true; value: Record<string, unknown> | undefined }
  | { ok: false; error: string };

/** Parse editor text: empty means "unset" (default note schema); otherwise a JSON object. */
export function parseOutputSchema(text: string): SchemaParse {
  if (text.trim() === "") return { ok: true, value: undefined };
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch (e) {
    return { ok: false, error: `Invalid JSON: ${(e as Error).message}` };
  }
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    return { ok: false, error: "The schema must be a JSON object." };
  }
  return { ok: true, value: v as Record<string, unknown> };
}

/** Editor text for a stored schema ("" when unset). */
export function schemaText(
  schema: Record<string, unknown> | undefined,
): string {
  return schema === undefined ? "" : JSON.stringify(schema, null, 2);
}

/**
 * `{{.input.agent.output.x}}` hints for the top-level properties of the schema
 * (the default note schema when unset).
 */
export function outputFieldHints(
  schema: Record<string, unknown> | undefined,
  max = 12,
): string[] {
  const props = (schema ?? NOTE_SCHEMA).properties;
  if (typeof props !== "object" || props === null || Array.isArray(props)) {
    return [];
  }
  return Object.keys(props)
    .slice(0, max)
    .map((k) => `{{.input.agent.output.${k}}}`);
}
