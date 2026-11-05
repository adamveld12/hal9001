import { useRef, useState, type FormEvent } from "react";
import { errorMessage } from "../../api/client";
import {
  useDeleteEnvKey,
  useEnvKeys,
  useSetEnvValue,
} from "../../api/settings";
import {
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
import { ConfirmButton } from "./ConfirmButton";
import { envKeyError } from "./envKey";

/** Names in the `env` file. Values are write-only: nothing here can read one back. */
export function EnvSection() {
  const keys = useEnvKeys();
  const set = useSetEnvValue();
  const remove = useDeleteEnvKey();
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const formRef = useRef<HTMLFormElement>(null);

  const keyProblem = key === "" ? undefined : envKeyError(key);
  const exists = keys.data?.includes(key) ?? false;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (envKeyError(key) || value === "") return;
    set.mutate(
      { key, value },
      {
        onSuccess: () => {
          setKey("");
          setValue("");
        },
      },
    );
  };

  return (
    <div className="space-y-4">
      <Panel title="Environment keys">
        <p className="mt-0 text-xs text-muted">
          Injected into every node process and redacted from stored output.
          Values are write-only: they are never shown, even here.
        </p>
        {keys.isPending && <LoadingState />}
        {keys.isError && (
          <ErrorState error={keys.error} onRetry={() => void keys.refetch()} />
        )}
        {keys.isSuccess && keys.data.length === 0 && (
          <EmptyState
            title="No environment keys"
            hint="Add a key below, for example a token a command node needs."
          />
        )}
        {keys.isSuccess && keys.data.length > 0 && (
          <Table>
            <thead>
              <tr>
                <Th>Name</Th>
                <Th>Value</Th>
                <Th className="w-px" />
              </tr>
            </thead>
            <tbody>
              {keys.data.map((k) => (
                <tr key={k}>
                  <Td className="font-mono">{k}</Td>
                  <Td className="text-muted">
                    <span aria-label="hidden">••••••••</span>
                  </Td>
                  <Td>
                    <div className="flex justify-end gap-2">
                      <Button
                        onClick={() => {
                          setKey(k);
                          setValue("");
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
                        onConfirm={() => remove.mutate(k)}
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
                placeholder="GITHUB_TOKEN"
                invalid={!!keyProblem}
                onChange={(e) => setKey(e.target.value)}
              />
            </Field>
            <Field
              label="Value"
              hint={exists ? "Replaces the stored value." : undefined}
            >
              <TextInput
                name="value"
                type="password"
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
