import { useState, type FormEvent } from "react";
import {
  useAuthOverview,
  useRemoveAuthPassword,
  useSetAuthPassword,
} from "../../api/auth";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  TextInput,
} from "../../components/ui";
import { diagnosticsByPath } from "./authProblem";
import { ChangeStatus } from "./ChangeStatus";
import { ConfirmAuthDialog } from "./ConfirmAuthDialog";
import {
  hasErrors,
  MIN_PASSWORD_LENGTH,
  passwordFormErrors,
  type PasswordFormErrors,
} from "./passwordForm";
import { useConfirmedChange } from "./useConfirmedChange";

export const PASSWORD_CARD_ID = "auth-password";
export const NEW_PASSWORD_INPUT_ID = "auth-password-new";

const FIELD_PATHS = ["current_password", "password"] as const;

/**
 * Set, change or remove the password. Only the server ever holds the hash; the typed
 * passwords live in this component's state until the request is answered and are not
 * kept in the query cache.
 */
export function PasswordCard() {
  const overview = useAuthOverview();
  const setPassword = useSetAuthPassword();
  const removePassword = useRemoveAuthPassword();
  const save = useConfirmedChange(setPassword.mutateAsync);
  const remove = useConfirmedChange(removePassword.mutateAsync);

  const [current, setCurrent] = useState("");
  const [password, setNewPassword] = useState("");
  const [repeat, setRepeat] = useState("");
  const [clientErrors, setClientErrors] = useState<PasswordFormErrors>({});
  const [removeOpen, setRemoveOpen] = useState(false);
  const [removeCurrent, setRemoveCurrent] = useState("");

  const status = overview.data?.password;
  const isSet = status?.set ?? false;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const errors = passwordFormErrors({
      current: isSet ? current : null,
      password,
      repeat,
    });
    setClientErrors(errors);
    if (hasErrors(errors)) return;
    const outcome = await save.run(
      isSet ? { password, current_password: current } : { password },
    );
    if (outcome === "done") {
      setCurrent("");
      setNewPassword("");
      setRepeat("");
    }
  };

  const closeRemove = () => {
    setRemoveOpen(false);
    setRemoveCurrent("");
  };

  const startRemove = async () => {
    const outcome = await remove.run({ current_password: removeCurrent });
    // A wrong password stays in the dialog, under its field. Otherwise it is done, or the
    // server asks to confirm (its dialog opens): either way this dialog is finished.
    if (outcome === "done") closeRemove();
    else if (outcome === "confirm") setRemoveOpen(false);
  };

  const saveFields = diagnosticsByPath(save.error).byPath;
  const removeFields = diagnosticsByPath(remove.error).byPath;
  const currentErrors = clientErrors.current
    ? [clientErrors.current]
    : saveFields["current_password"];
  const passwordErrors = clientErrors.password
    ? [clientErrors.password]
    : saveFields["password"];
  const repeatErrors = clientErrors.repeat ? [clientErrors.repeat] : undefined;

  return (
    <Panel
      title="Password"
      actions={
        status && (
          <Badge tone={status.set ? "ok" : "muted"}>
            {status.set ? "Set" : "Not set"}
          </Badge>
        )
      }
    >
      <div id={PASSWORD_CARD_ID} className="scroll-mt-4 space-y-3">
        {overview.isPending && <LoadingState />}
        {overview.isError && (
          <ErrorState
            error={overview.error}
            onRetry={() => void overview.refetch()}
          />
        )}
        {status && (
          <>
            <p className="m-0 text-xs text-muted" data-testid="password-status">
              {status.set ? (
                <>
                  A password is set. Last used{" "}
                  {status.last_used_at ? (
                    <Time ts={status.last_used_at} />
                  ) : (
                    "never"
                  )}
                  .
                </>
              ) : (
                "No password is set. Set one to sign in with it from other devices."
              )}{" "}
              Only a hash of it is stored; it is never shown.
            </p>
            <form
              className="max-w-sm space-y-2"
              onSubmit={(e) => void submit(e)}
              data-testid="password-form"
            >
              {isSet && (
                <Field label="Current password" error={currentErrors}>
                  <TextInput
                    type="password"
                    autoComplete="current-password"
                    value={current}
                    invalid={!!currentErrors}
                    disabled={save.busy}
                    onChange={(e) => setCurrent(e.target.value)}
                  />
                </Field>
              )}
              <Field
                label={isSet ? "New password" : "Password"}
                hint={`At least ${MIN_PASSWORD_LENGTH} characters.`}
                error={passwordErrors}
              >
                <TextInput
                  id={NEW_PASSWORD_INPUT_ID}
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  invalid={!!passwordErrors}
                  disabled={save.busy}
                  onChange={(e) => setNewPassword(e.target.value)}
                />
              </Field>
              <Field
                label={isSet ? "Repeat new password" : "Repeat password"}
                error={repeatErrors}
              >
                <TextInput
                  type="password"
                  autoComplete="new-password"
                  value={repeat}
                  invalid={!!repeatErrors}
                  disabled={save.busy}
                  onChange={(e) => setRepeat(e.target.value)}
                />
              </Field>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="submit"
                  variant="primary"
                  disabled={save.busy}
                  data-testid="password-save"
                >
                  {isSet ? "Change password" : "Set password"}
                </Button>
                {isSet && (
                  <Button
                    variant="danger"
                    disabled={save.busy}
                    data-testid="password-remove"
                    onClick={() => {
                      remove.dismiss();
                      setRemoveOpen(true);
                    }}
                  >
                    Remove password
                  </Button>
                )}
              </div>
            </form>
            {!removeOpen && (
              <ChangeStatus
                result={save.result ?? remove.result}
                error={save.error ?? remove.error}
                done={save.result ? "Password saved." : "Password removed."}
                shownElsewhere={FIELD_PATHS}
              />
            )}
          </>
        )}
      </div>

      <ConfirmDialog
        open={removeOpen}
        testId="password-remove-dialog"
        title="Remove the password?"
        confirmLabel="Remove password"
        busy={remove.busy}
        confirmDisabled={removeCurrent === ""}
        onCancel={closeRemove}
        onConfirm={() => void startRemove()}
      >
        <div className="space-y-3">
          <p className="m-0">
            This forgets the password, turns password sign-in off and ends the
            sessions that used it. Enter the current password to confirm.
          </p>
          <Field
            label="Current password"
            error={removeFields["current_password"]}
          >
            <TextInput
              type="password"
              autoComplete="current-password"
              value={removeCurrent}
              invalid={!!removeFields["current_password"]}
              disabled={remove.busy}
              onChange={(e) => setRemoveCurrent(e.target.value)}
            />
          </Field>
          <ChangeStatus
            result={null}
            error={remove.error}
            shownElsewhere={FIELD_PATHS}
          />
        </div>
      </ConfirmDialog>
      <ConfirmAuthDialog
        confirmation={remove.confirmation}
        busy={remove.busy}
        onCancel={() => {
          remove.cancel();
          closeRemove();
        }}
        onConfirm={() =>
          void remove.confirm().then((outcome) => {
            if (outcome === "done") closeRemove();
          })
        }
      />
    </Panel>
  );
}
