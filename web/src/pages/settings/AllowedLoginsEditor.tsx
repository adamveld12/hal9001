import { useState, type FormEvent } from "react";
import { useSetAllowedLogins } from "../../api/auth";
import { Icon } from "../../components/icons";
import { Button, TextInput } from "../../components/ui";
import {
  addLogin,
  emptyListNote,
  removeLogin,
  sameLogins,
} from "./allowedLogins";
import { ChangeStatus } from "./ChangeStatus";
import { ConfirmAuthDialog } from "./ConfirmAuthDialog";
import { useConfirmedChange } from "./useConfirmedChange";

/**
 * Who can sign in over the tailnet (`allowed_logins`). Edits stay local until Save; an empty list
 * means the owner of this Mac only.
 */
export function AllowedLoginsEditor({
  configured,
  ownerLogin,
}: {
  /** `allowed_logins` as in config.yaml. */
  configured: readonly string[];
  /** The login that owns this Mac, from Tailscale. */
  ownerLogin: string | null;
}) {
  const setLogins = useSetAllowedLogins();
  const change = useConfirmedChange(setLogins.mutateAsync);
  // null: show what the server has; an array: the user's unsaved edit.
  const [edited, setEdited] = useState<string[] | null>(null);
  const [input, setInput] = useState("");
  const [inputError, setInputError] = useState<string | null>(null);

  const list = edited ?? [...configured];
  const dirty = edited !== null && !sameLogins(edited, configured);

  const add = (e: FormEvent) => {
    e.preventDefault();
    if (input.trim() === "") return;
    const r = addLogin(list, input);
    setInputError(r.error);
    if (r.error === null) {
      setEdited(r.logins);
      setInput("");
    }
  };

  const save = async () => {
    const outcome = await change.run({ logins: list });
    if (outcome === "done") setEdited(null);
  };

  return (
    <div className="space-y-2" data-testid="tailscale-logins">
      <ul
        className="m-0 flex list-none flex-wrap gap-1.5 p-0"
        aria-label="Logins that can sign in"
      >
        {list.map((login) => (
          <li
            key={login.toLowerCase()}
            className="inline-flex items-center gap-1 rounded-md border border-line bg-raised py-0.5 pl-2 pr-1 font-mono text-xs"
          >
            {login}
            <button
              type="button"
              aria-label={`Remove ${login}`}
              disabled={change.busy}
              onClick={() => setEdited(removeLogin(list, login))}
              className="inline-flex size-4 items-center justify-center rounded text-muted hover:bg-line hover:text-fg disabled:opacity-50"
            >
              <Icon name="close" size={12} />
            </button>
          </li>
        ))}
      </ul>
      {list.length === 0 && (
        <p
          className="m-0 text-xs text-muted"
          data-testid="tailscale-logins-empty"
        >
          {emptyListNote(ownerLogin)}
        </p>
      )}

      <form className="max-w-md space-y-1" onSubmit={add}>
        <div className="flex items-center gap-2">
          <TextInput
            type="text"
            aria-label="Add a login"
            aria-invalid={inputError !== null}
            inputMode="email"
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            placeholder="name@example.com"
            value={input}
            invalid={inputError !== null}
            disabled={change.busy}
            onChange={(e) => {
              setInput(e.target.value);
              setInputError(null);
            }}
          />
          <Button
            type="submit"
            icon="plus"
            disabled={change.busy || input.trim() === ""}
          >
            Add
          </Button>
        </div>
        {inputError ? (
          <p className="m-0 text-[11px] text-bad" role="alert">
            {inputError}
          </p>
        ) : (
          <p className="m-0 text-[11px] text-muted">
            A Tailscale login such as name@example.com. Press Enter to add.
          </p>
        )}
      </form>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="primary"
          icon="save"
          data-testid="tailscale-logins-save"
          disabled={!dirty || change.busy}
          onClick={() => void save()}
        >
          Save
        </Button>
        {dirty && (
          <Button
            disabled={change.busy}
            onClick={() => {
              setEdited(null);
              setInputError(null);
              change.dismiss();
            }}
          >
            Discard changes
          </Button>
        )}
      </div>
      <ChangeStatus result={change.result} error={change.error} />

      <ConfirmAuthDialog
        confirmation={change.confirmation}
        busy={change.busy}
        onCancel={change.cancel}
        onConfirm={() =>
          void change.confirm().then((outcome) => {
            if (outcome === "done") setEdited(null);
          })
        }
      />
    </div>
  );
}
