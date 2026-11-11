// JSON-only editing of a port's JSON Schema: the editor keeps text, the model keeps an object.
export type SchemaParse =
  | { ok: true; value: Record<string, unknown> | undefined }
  | { ok: false; error: string };

/** Parse editor text: empty means "no schema" (the inbox shortcut); otherwise a JSON object. */
export function parsePortSchema(text: string): SchemaParse {
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
