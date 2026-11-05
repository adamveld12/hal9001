export type ParsedItem =
  { ok: true; value: unknown } | { ok: false; error: string };

/** Parse the inject textarea: any JSON value is a valid item; empty input is not. */
export function parseInjectBody(text: string): ParsedItem {
  if (text.trim() === "") return { ok: false, error: "Enter a JSON value." };
  try {
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch (e) {
    return {
      ok: false,
      error: e instanceof Error ? e.message : "Invalid JSON.",
    };
  }
}
