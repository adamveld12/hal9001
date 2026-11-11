import type { AuthChangeResult } from "../../api/auth";
import { changeErrorLines } from "./authProblem";

/**
 * The outcome of a sign-in change, in a live region so a screen reader hears it:
 * the server's notice (what the change ended) or `done`, or why it failed.
 */
export function ChangeStatus({
  result,
  error,
  done = "Saved.",
  shownElsewhere,
}: {
  result: AuthChangeResult | null;
  error: unknown;
  /** Shown after a change that the server has nothing to add about. */
  done?: string;
  /** Diagnostic paths that a form field already shows. */
  shownElsewhere?: readonly string[];
}) {
  const failed = error !== null && error !== undefined;
  const lines = failed ? changeErrorLines(error, shownElsewhere) : [];
  return (
    <div aria-live="polite" aria-atomic="true" data-testid="auth-change-status">
      {lines.length > 0 && (
        <div className="space-y-0.5 text-xs text-bad">
          {lines.map((l) => (
            <p key={l} className="m-0">
              {l}
            </p>
          ))}
        </div>
      )}
      {result && !failed && (
        <p className="m-0 text-xs text-ok">{result.notice ?? done}</p>
      )}
    </div>
  );
}
