import type { Conversation } from "../../api/conversations";

/**
 * The loaded pages as one list. A conversation can move between pages while
 * the list is refetched (its activity changed), so a repeated id keeps its
 * first (newest) place.
 */
export function mergePages(
  pages: readonly { items: readonly Conversation[] }[] | undefined,
): Conversation[] {
  const seen = new Set<string>();
  const out: Conversation[] = [];
  for (const page of pages ?? []) {
    for (const c of page.items) {
      if (seen.has(c.id)) continue;
      seen.add(c.id);
      out.push(c);
    }
  }
  return out;
}
