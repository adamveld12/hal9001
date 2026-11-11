import { useState } from "react";
import type { AuthChangeResult } from "../../api/auth";
import { parseConfirmation, type ConfirmationRequired } from "./authProblem";

interface Pending<V> {
  vars: V;
  confirmation: ConfirmationRequired;
}
/** What became of a `run`: done, waiting for the user's confirmation, or failed (`error` holds why). */
export type ChangeOutcome = "done" | "confirm" | "error";

/**
 * Drives a sign-in change that may answer 409 `confirmation-required`: `run`
 * sends it; when the server asks, `confirmation` is set for the dialog
 * (ConfirmAuthDialog), and `confirm` sends the same change again with
 * `confirm: true`. Results and errors stay in this hook's state, not in the
 * query cache, so a password in `vars` is dropped once the change is decided.
 */
export function useConfirmedChange<V extends object>(
  send: (vars: V) => Promise<AuthChangeResult>,
) {
  const [pending, setPending] = useState<Pending<V> | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<AuthChangeResult | null>(null);

  const attempt = async (vars: V): Promise<ChangeOutcome> => {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setResult(await send(vars));
      setPending(null);
      return "done";
    } catch (e) {
      const alreadyConfirmed = "confirm" in vars && vars.confirm === true;
      const confirmation = alreadyConfirmed ? null : parseConfirmation(e);
      if (confirmation) {
        setPending({ vars, confirmation });
        return "confirm";
      }
      setPending(null);
      setError(e);
      return "error";
    } finally {
      setBusy(false);
    }
  };

  return {
    run: attempt,
    /** Sends the change again with `confirm: true`. */
    confirm: async (): Promise<ChangeOutcome> =>
      pending ? attempt({ ...pending.vars, confirm: true }) : "error",
    cancel: () => setPending(null),
    /** Forgets the last result and error. */
    dismiss: () => {
      setError(null);
      setResult(null);
    },
    confirmation: pending?.confirmation ?? null,
    busy,
    error,
    result,
  };
}
