import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { errorMessage } from "../../api/client";
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
  Toggle,
} from "../../components/ui";
import { ConfirmButton } from "./ConfirmButton";
import { envKeyError } from "./envKey";
import {
  rowDisplay,
  saveInput,
  secretForKey,
  toggleInput,
  type EnvKeyRow,
  type EnvSetInput,
} from "./envView";

export type { EnvKeyRow };

interface Mutation<V> {
  mutate: (v: V, o?: { onSuccess?: () => void }) => void;
  isPending: boolean;
  isError: boolean;
  error: unknown;
}

/**
 * Lists the keys of an env file and sets, replaces, removes or re-flags them.
 * Secret values are write-only; plain values are shown. Used for the global `env`
 * file (Settings) and for a sensor's own env file (sensor page).
 */
export function EnvEditor({
  keys,
  set,
  remove,
  listTitle,
  description,
  emptyHint,
  namePlaceholder = "GITHUB_TOKEN",
}: {
  keys: {
    isPending: boolean;
    isError: boolean;
    isSuccess: boolean;
    error: unknown;
    data: readonly EnvKeyRow[] | undefined;
    refetch: () => unknown;
  };
  set: Mutation<EnvSetInput>;
  remove: Mutation<string>;
  listTitle: string;
  description: ReactNode;
  emptyHint: string;
  namePlaceholder?: string;
}) {
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [secret, setSecret] = useState(true);
  const formRef = useRef<HTMLFormElement>(null);

  const keyProblem = key === "" ? undefined : envKeyError(key);
  const rows = keys.data ?? [];
  const exists = rows.some((r) => r.key === key);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (envKeyError(key) || value === "") return;
    set.mutate(saveInput(key, value, secret), {
      onSuccess: () => {
        setKey("");
        setValue("");
        setSecret(true);
      },
    });
  };

  return (
    <div className="space-y-4">
      <Panel title={listTitle}>
        <p className="mt-0 text-xs text-muted">{description}</p>
        {keys.isPending && <LoadingState />}
        {keys.isError && (
          <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
        )}
        {keys.isSuccess && rows.length === 0 && (
          <EmptyState title="No environment keys" hint={emptyHint} />
        )}
        {keys.isSuccess && rows.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Value</Th>
                <Th className="w-px" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key}>
                  <Td className="font-mono">
                    {r.key}
                    {r.overridesGlobal && (
                      <Badge tone="info" className="ml-2 align-middle">
                        overrides global
                      </Badge>
                    )}
                  </Td>
                  <Td className="font-mono text-muted">
                    <RowValue row={r} />
                  </Td>
                  <Td>
                    <div className="flex items-center justify-end gap-2">
                      <Toggle
                        label="Secret"
                        checked={r.secret}
                        disabled={set.isPending}
                        onChange={() => set.mutate(toggleInput(r))}
                      />
                      <Button
                        onClick={() => {
                          setKey(r.key);
                          setValue(r.value ?? "");
                          setSecret(r.secret);
                          formRef.current
                            ?.querySelector<HTMLInputElement>(
                              "input[name=value]",
                            )
                            ?.focus();
                        }}
                      >
                        Replace
                      </Button>
                      <ConfirmButton
                        icon="trash"
                        disabled={remove.isPending}
                        onConfirm={() => remove.mutate(r.key)}
                      >
                        Remove
                      </ConfirmButton>
                    </div>
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
        {remove.isError && (
          <p role="alert" className="mb-0 mt-3 text-xs text-bad">
            {errorMessage(remove.error)}
          </p>
        )}
      </Panel>

      <Panel title={exists ? "Replace value" : "Add key"}>
        <form ref={formRef} onSubmit={submit} className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <Field label="Name" error={keyProblem ? [keyProblem] : undefined}>
              <TextInput
                value={key}
                spellCheck={false}
                autoComplete="off"
                placeholder={namePlaceholder}
                invalid={!!keyProblem}
                onChange={(e) => {
                  setKey(e.target.value);
                  setSecret(secretForKey(rows, e.target.value, secret));
                }}
              />
            </Field>
            <Field
              label="Value"
              hint={exists ? "Replaces the stored value." : undefined}
            >
              <TextInput
                name="value"
                type={secret ? "password" : "text"}
                value={value}
                autoComplete="new-password"
                // Password managers must not offer to save an env value as a login.
                data-1p-ignore
                data-lpignore="true"
                spellCheck={false}
                onChange={(e) => setValue(e.target.value)}
              />
            </Field>
          </div>
          <Toggle label="Secret" checked={secret} onChange={setSecret} />
          <div className="flex items-center gap-2">
            <Button
              type="submit"
              variant="primary"
              icon="save"
              disabled={
                set.isPending || key === "" || !!keyProblem || value === ""
              }
            >
              {set.isPending ? "Saving…" : exists ? "Replace" : "Add"}
            </Button>
            {set.isError && (
              <span role="alert" className="text-xs text-bad">
                {errorMessage(set.error)}
              </span>
            )}
          </div>
        </form>
      </Panel>
    </div>
  );
}

function RowValue({ row }: { row: EnvKeyRow }) {
  const d = rowDisplay(row);
  if (d.kind === "secret") return <span aria-label="hidden">••••••••</span>;
  return d.empty ? <span className="italic">(empty)</span> : <>{d.text}</>;
}
