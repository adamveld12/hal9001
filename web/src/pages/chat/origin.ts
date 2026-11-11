// Where "Back to drawer" returns to: the last page that was not the Chat page.
// Remembered in sessionStorage so a reload on /chat keeps it.

import { isChatPath } from "./path";

export const DEFAULT_ORIGIN = "/inbox";
export const ORIGIN_STORAGE_ITEM = "hal9001.chat.origin";

/**
 * The origin after the app moved to `loc`: the page itself unless it is a chat
 * page (then the previous origin stays).
 */
export function rememberOrigin(
  prev: string | null,
  loc: { pathname: string; search: string },
): string | null {
  return isChatPath(loc.pathname) ? prev : loc.pathname + loc.search;
}

/** A stored origin that is an in-app path outside the Chat page, else the default. */
export function safeOrigin(raw: string | null | undefined): string {
  if (
    !raw ||
    !raw.startsWith("/") ||
    raw.startsWith("//") ||
    raw.includes("\\") ||
    isChatPath(raw.split(/[?#]/)[0] ?? raw)
  ) {
    return DEFAULT_ORIGIN;
  }
  return raw;
}

export function loadOrigin(): string | null {
  try {
    return sessionStorage.getItem(ORIGIN_STORAGE_ITEM);
  } catch {
    return null;
  }
}

export function saveOrigin(path: string): void {
  try {
    sessionStorage.setItem(ORIGIN_STORAGE_ITEM, path);
  } catch {
    // not persisted
  }
}
