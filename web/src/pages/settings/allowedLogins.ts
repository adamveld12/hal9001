// The `allowed_logins` list: which Tailscale logins `tailscale-identity` accepts.
// The server checks the same rules (PUT /api/auth/allowed-logins); these keep
// obvious mistakes from making a request.

export const MAX_LOGINS = 100;
const MAX_LOGIN_BYTES = 254;

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/;
const SHAPE = /^[^\s@,]+@[^\s@,]+$/;

/** Why `raw` is not a login (`name@domain`), or null when it is. Surrounding whitespace is ignored. */
export function loginError(raw: string): string | null {
  const login = raw.trim();
  if (login === "") return "Enter a login such as name@example.com.";
  if (CONTROL.test(login)) return "A login has no control characters.";
  if (/[\s,]/.test(login)) {
    return "Add one login at a time: no spaces or commas.";
  }
  if (!SHAPE.test(login)) return "A login looks like name@domain.";
  if (new TextEncoder().encode(login).length > MAX_LOGIN_BYTES) {
    return `A login is at most ${MAX_LOGIN_BYTES} bytes.`;
  }
  return null;
}

/** Logins are compared without regard to case. */
export function sameLogin(a: string, b: string): boolean {
  return a.toLowerCase() === b.toLowerCase();
}

export interface AddResult {
  logins: string[];
  /** Why nothing was added; null when `raw` was added or was already listed. */
  error: string | null;
}

/** `list` plus the login in `raw`: trimmed, and left out when already listed (any case). */
export function addLogin(list: readonly string[], raw: string): AddResult {
  const error = loginError(raw);
  if (error) return { logins: [...list], error };
  const login = raw.trim();
  if (list.some((l) => sameLogin(l, login))) {
    return { logins: [...list], error: null };
  }
  if (list.length >= MAX_LOGINS) {
    return {
      logins: [...list],
      error: `At most ${MAX_LOGINS} logins can be listed.`,
    };
  }
  return { logins: [...list, login], error: null };
}

/** `list` without `login` (any case). */
export function removeLogin(list: readonly string[], login: string): string[] {
  return list.filter((l) => !sameLogin(l, login));
}

/** Whether two lists name the same logins in the same order. */
export function sameLogins(
  a: readonly string[],
  b: readonly string[],
): boolean {
  return a.length === b.length && a.every((l, i) => sameLogin(l, b[i] ?? ""));
}

/** What an empty list means. */
export function emptyListNote(owner: string | null): string {
  return owner
    ? `Nobody is listed: only the owner of this Mac (${owner}) can sign in over the tailnet.`
    : "Nobody is listed: only the owner of this Mac can sign in over the tailnet, and Tailscale has not said who that is.";
}
