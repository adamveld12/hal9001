import type { PageContextEntity, PageContextFact } from "./pageContext";

/** Rows a page lists as entities; the shell caps the whole context anyway. */
export const MAX_ROWS = 20;

/** One line of at most `max` characters: whitespace collapsed, "…" when cut. */
export function trimText(s: string | null | undefined, max = 200): string {
  const one = (s ?? "").replace(/\s+/g, " ").trim();
  return one.length > max ? `${one.slice(0, max - 1)}…` : one;
}

/** `ok 3, failed 1`: the count of each distinct key, most frequent first. */
export function countBy<T>(
  items: readonly T[],
  key: (item: T) => string,
): string {
  const counts = new Map<string, number>();
  for (const it of items) {
    const k = key(it);
    counts.set(k, (counts.get(k) ?? 0) + 1);
  }
  return [...counts]
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([k, n]) => `${k} ${n}`)
    .join(", ");
}

/** Facts from label/value pairs; pairs without a value are left out. */
export function factList(
  pairs: readonly (readonly [
    string,
    string | number | boolean | null | undefined,
  ])[],
): PageContextFact[] {
  const out: PageContextFact[] = [];
  for (const [label, value] of pairs) {
    if (value === null || value === undefined || value === "") continue;
    out.push({ label, value: String(value) });
  }
  return out;
}

/** The first `MAX_ROWS` rows as entities. */
export function rowEntities<T>(
  rows: readonly T[],
  toEntity: (row: T) => PageContextEntity,
  max = MAX_ROWS,
): PageContextEntity[] {
  return rows.slice(0, max).map(toEntity);
}
