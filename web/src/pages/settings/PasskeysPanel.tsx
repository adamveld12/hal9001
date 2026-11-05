import { useState, type FormEvent } from "react";
import { errorMessage } from "../../api/client";
import { useDeletePasskey, usePasskeys } from "../../api/auth";
import { Time } from "../../components/Time";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  Table,
  Td,
  TextInput,
  Th,
} from "../../components/ui";
import { useRegisterPasskey } from "../auth/passkeys";
import { passkeysSupported } from "../auth/webauthn";
import { ConfirmButton } from "./ConfirmButton";
import { groupPasskeys } from "./authView";

/** Passkeys are stored per relying-party ID, so each host name lists separately. */
export function PasskeysPanel() {
  const passkeys = usePasskeys();
  const remove = useDeletePasskey();
  const register = useRegisterPasskey();
  const [label, setLabel] = useState("");
  const host = window.location.hostname;
  const supported = passkeysSupported();

  const submit = (e: FormEvent) => {
    e.preventDefault();
    register.mutate(label, { onSuccess: () => setLabel("") });
  };

  const groups = groupPasskeys(passkeys.data?.credentials ?? [], host);

  return (
    <Panel title="Passkeys">
      {passkeys.isPending && <LoadingState />}
      {passkeys.isError && (
        <ErrorState
          error={passkeys.error}
          onRetry={() => void passkeys.refetch()}
        />
      )}
      {passkeys.isSuccess && groups.length === 0 && (
        <EmptyState
          title="No passkeys"
          hint={`Register one for ${host} below.`}
        />
      )}
      {passkeys.isSuccess &&
        groups.map((g) => (
          <div key={g.rpId} className="mb-4">
            <div className="mb-1 flex items-center gap-2 text-xs font-medium">
              <span className="font-mono">{g.rpId}</span>
              {g.rpId === host && <Badge tone="info">this host</Badge>}
            </div>
            <Table>
              <thead>
                <tr>
                  <Th>Label</Th>
                  <Th>Added</Th>
                  <Th>Last used</Th>
                  <Th>Uses</Th>
                  <Th className="w-px" />
                </tr>
              </thead>
              <tbody>
                {g.passkeys.map((p) => (
                  <tr key={p.id}>
                    <Td>{p.label}</Td>
                    <Td>
                      <Time ts={p.created_at} />
                    </Td>
                    <Td>
                      <Time ts={p.last_used_at} />
                    </Td>
                    <Td className="tabular-nums text-muted">{p.sign_count}</Td>
                    <Td>
                      <ConfirmButton
                        icon="trash"
                        disabled={remove.isPending}
                        onConfirm={() => remove.mutate(p.id)}
                      >
                        Remove
                      </ConfirmButton>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </div>
        ))}
      {remove.isError && (
        <p role="alert" className="mb-3 mt-0 text-xs text-bad">
          {errorMessage(remove.error)}
        </p>
      )}

      <form
        onSubmit={submit}
        className="flex items-end gap-2 border-t border-line pt-3"
      >
        <div className="w-64">
          <Field
            label="Label (optional)"
            hint={`Registers for ${host}, the host name in the address bar. Open this page from each host name to add a passkey for it.`}
          >
            <TextInput
              value={label}
              maxLength={80}
              disabled={register.isPending}
              placeholder={`passkey for ${host}`}
              onChange={(e) => setLabel(e.target.value)}
            />
          </Field>
        </div>
        <Button
          type="submit"
          variant="primary"
          icon="plus"
          disabled={!supported || register.isPending}
        >
          {register.isPending ? "Waiting for passkey…" : "Register passkey"}
        </Button>
      </form>
      {!supported && (
        <p className="mb-0 mt-2 text-[11px] text-muted">
          This browser cannot create passkeys.
        </p>
      )}
      {register.isError && (
        <p role="alert" className="mb-0 mt-2 text-xs text-bad">
          {errorMessage(register.error)}
        </p>
      )}
    </Panel>
  );
}
