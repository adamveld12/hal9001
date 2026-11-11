// Recently used commands, most recent first. Stored in localStorage by command id.

export const MAX_RECENT = 8;
export const RECENT_ITEM = "hal9001.palette.recent";

/** `ids` with `id` moved to the front, at most `max` long. */
export function pushRecent(
  ids: readonly string[],
  id: string,
  max = MAX_RECENT,
): string[] {
  return [id, ...ids.filter((x) => x !== id)].slice(0, max);
}

export function parseRecent(raw: string | null): string[] {
  if (raw === null) return [];
  try {
    const v: unknown = JSON.parse(raw);
    if (!Array.isArray(v)) return [];
    return v
      .filter((x): x is string => typeof x === "string" && x !== "")
      .slice(0, MAX_RECENT);
  } catch {
    return [];
  }
}

export function loadRecent(): string[] {
  try {
    return parseRecent(localStorage.getItem(RECENT_ITEM));
  } catch {
    return [];
  }
}

export function saveRecent(ids: readonly string[]): void {
  try {
    localStorage.setItem(RECENT_ITEM, JSON.stringify(ids));
  } catch {
    // not persisted
  }
}
