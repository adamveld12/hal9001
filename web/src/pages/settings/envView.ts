/** One env key as the editor sees it. `value` is non-null only for plain (non-secret) keys. */
export interface EnvKeyRow {
  key: string;
  secret: boolean;
  value: string | null;
  /** Shown as a badge: the key also exists in the global env and this one wins. */
  overridesGlobal?: boolean;
}

/** Request for the set mutation; mirrors EnvSetRequest plus the key. */
export interface EnvSetInput {
  key: string;
  value?: string;
  secret?: boolean;
}

/** Flag-only flip: no value, so the stored value is kept and need not be retyped. */
export function toggleInput(row: EnvKeyRow): EnvSetInput {
  return { key: row.key, secret: !row.secret };
}

/** Add/replace: always sends `secret` explicitly because the API defaults it to true. */
export function saveInput(
  key: string,
  value: string,
  secret: boolean,
): EnvSetInput {
  return { key, value, secret };
}

/** The form's Secret toggle for `key`: the existing key's state on a name match, else `fallback`. */
export function secretForKey(
  rows: readonly EnvKeyRow[],
  key: string,
  fallback: boolean,
): boolean {
  return rows.find((r) => r.key === key)?.secret ?? fallback;
}

export type RowDisplay =
  { kind: "secret" } | { kind: "plain"; text: string; empty: boolean };

/** How a row's value cell renders: secrets never show a value, plain keys show theirs. */
export function rowDisplay(row: EnvKeyRow): RowDisplay {
  if (row.secret || row.value === null) return { kind: "secret" };
  return { kind: "plain", text: row.value, empty: row.value === "" };
}
