/** Keyboard cursor movement over an ordered list of ids, shared by inbox and approvals. */

/**
 * Id to move to from `current` in direction `dir` (1 = down, -1 = up).
 *
 * - `current` null: start at the first item.
 * - `current` still listed: move one step, stopping at the ends.
 * - `current` no longer listed (archived, decided, filtered out): `lastIndex`
 *   is where it used to be, so down lands on the item that took its place and
 *   up on the one before it.
 */
export function moveCursor(
  ids: readonly string[],
  current: string | null,
  dir: 1 | -1,
  lastIndex = 0,
): string | null {
  if (ids.length === 0) return null;
  if (current === null) return ids[0] ?? null;
  const at = ids.indexOf(current);
  const last = ids.length - 1;
  const target = at >= 0 ? at + dir : dir === 1 ? lastIndex : lastIndex - 1;
  return ids[Math.min(Math.max(target, 0), last)] ?? null;
}

/** Item to focus once `id` leaves the list: the one below it, else the one above, else none. */
export function neighborAfterRemoval(
  ids: readonly string[],
  id: string,
): string | null {
  const at = ids.indexOf(id);
  if (at < 0) return null;
  return ids[at + 1] ?? ids[at - 1] ?? null;
}
