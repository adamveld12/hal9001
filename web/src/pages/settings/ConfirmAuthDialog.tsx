import { ConfirmDialog } from "../../components/ConfirmDialog";
import {
  confirmReasons,
  ENDS_SESSION_WARNING,
  type ConfirmationRequired,
} from "./authProblem";

/** Shown when the server answers 409 `confirmation-required`: why, and "Do it anyway" repeats the change with `confirm: true`. */
export function ConfirmAuthDialog({
  confirmation,
  busy,
  onConfirm,
  onCancel,
}: {
  /** The server's answer; the dialog is open while it is set. */
  confirmation: ConfirmationRequired | null;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <ConfirmDialog
      open={confirmation !== null}
      testId="auth-confirm-dialog"
      title="Make this change anyway?"
      confirmLabel="Do it anyway"
      busy={busy}
      onCancel={onCancel}
      onConfirm={onConfirm}
    >
      {confirmation && (
        <div className="space-y-2">
          {confirmReasons(confirmation).length > 0 ? (
            <ul
              className="m-0 list-disc space-y-1 pl-4"
              data-testid="auth-confirm-reasons"
            >
              {confirmReasons(confirmation).map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
          ) : (
            !confirmation.endsSession && (
              <p className="m-0">The server asks you to confirm this change.</p>
            )
          )}
          {confirmation.endsSession && (
            <p
              className="m-0 font-bold text-fg"
              data-testid="auth-confirm-ends-session"
            >
              {ENDS_SESSION_WARNING}
            </p>
          )}
        </div>
      )}
    </ConfirmDialog>
  );
}
