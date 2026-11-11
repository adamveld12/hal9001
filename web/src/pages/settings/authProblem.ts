import { ApiError, errorMessage } from "../../api/client";

/** What a 409 `confirmation-required` says: why the change needs a second go, and whether it ends this session. */
export interface ConfirmationRequired {
  reasons: string[];
  endsSession: boolean;
}

/** The confirmation a 409 `confirmation-required` asks for; null for any other error. */
export function parseConfirmation(e: unknown): ConfirmationRequired | null {
  if (
    !(e instanceof ApiError) ||
    e.status !== 409 ||
    e.kind !== "confirmation-required"
  ) {
    return null;
  }
  const reasons: unknown = e.problem?.reasons;
  return {
    reasons: Array.isArray(reasons)
      ? (reasons as unknown[]).filter(
          (r): r is string => typeof r === "string" && r !== "",
        )
      : [],
    endsSession: e.problem?.ends_session === true,
  };
}

/** The `Retry-After` of a 429 `rate-limited`, in seconds; null for any other error. */
export function parseRateLimit(
  e: unknown,
): { retryAfter: number | null } | null {
  if (!(e instanceof ApiError) || e.status !== 429) return null;
  const n: unknown = e.problem?.retry_after;
  return {
    retryAfter: typeof n === "number" && Number.isFinite(n) && n > 0 ? n : null,
  };
}

/** "5 seconds", "1 minute", "15 minutes", "2 hours": rounded up to the unit, so the wait is never understated. */
export function waitText(seconds: number): string {
  const unit = (n: number, name: string) => `${n} ${name}${n === 1 ? "" : "s"}`;
  if (seconds < 60) return unit(Math.max(1, Math.ceil(seconds)), "second");
  if (seconds < 3600) return unit(Math.ceil(seconds / 60), "minute");
  return unit(Math.ceil(seconds / 3600), "hour");
}

/** Sentence for a 429; says when to try again when the server told. */
export function rateLimitMessage(rl: { retryAfter: number | null }): string {
  return rl.retryAfter === null
    ? "Too many failed attempts. Try again later."
    : `Too many failed attempts. Try again in ${waitText(rl.retryAfter)}.`;
}

export interface DiagnosticsByPath {
  /** Messages per diagnostic `path`, for diagnostics that have one. */
  byPath: Record<string, string[]>;
  /** Messages of diagnostics without a path. */
  general: string[];
}

/** The messages of a 422 problem's diagnostics, grouped by `path` (for example `current_password`). */
export function diagnosticsByPath(e: unknown): DiagnosticsByPath {
  const out: DiagnosticsByPath = { byPath: {}, general: [] };
  if (!(e instanceof ApiError) || !e.problem) return out;
  const d: unknown = e.problem.diagnostics;
  if (!Array.isArray(d)) return out;
  for (const item of d as unknown[]) {
    if (typeof item !== "object" || item === null) continue;
    if (!("message" in item) || typeof item.message !== "string") continue;
    const path =
      "path" in item && typeof item.path === "string" ? item.path : "";
    if (path === "") out.general.push(item.message);
    else (out.byPath[path] ??= []).push(item.message);
  }
  return out;
}

/**
 * Lines to show for a failed sign-in change: the wait for a 429, the diagnostics of a 422 (with their path),
 * or the problem's message. Diagnostics whose path is in `shownElsewhere` (a form field shows them itself)
 * are left out. Never the request, so a password cannot leak into it.
 */
export function changeErrorLines(
  e: unknown,
  shownElsewhere: readonly string[] = [],
): string[] {
  const rl = parseRateLimit(e);
  if (rl) return [rateLimitMessage(rl)];
  const d = diagnosticsByPath(e);
  if (d.general.length === 0 && Object.keys(d.byPath).length === 0) {
    return [errorMessage(e)];
  }
  return [
    ...d.general,
    ...Object.entries(d.byPath)
      .filter(([path]) => !shownElsewhere.includes(path))
      .flatMap(([path, msgs]) => msgs.map((m) => `${path}: ${m}`)),
  ];
}
