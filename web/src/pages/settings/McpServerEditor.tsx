import { useState } from "react";
import { errorMessage } from "../../api/client";
import {
  useCreateMcpServer,
  useUpdateMcpServer,
  type RemoteMcpServer,
} from "../../api/mcpServers";
import { Button, Field, Panel, TextInput } from "../../components/ui";
import {
  AUTH_MODES,
  buildCreateBody,
  buildUpdateBody,
  emptyForm,
  fieldsFor,
  formFromServer,
  hasErrors,
  validateForm,
  type McpForm,
} from "./mcpServerForm";
import { diagnosticMessages } from "./problem";

/** Inline add (no `server`) or edit panel for one remote MCP server. */
export function McpServerEditor({
  server,
  existing,
  onDone,
  onCancel,
}: {
  server?: RemoteMcpServer;
  existing: readonly string[];
  /** Called with the saved server's name. */
  onDone: (name: string) => void;
  onCancel: () => void;
}) {
  const editing = server !== undefined;
  const [form, setForm] = useState<McpForm>(() =>
    server ? formFromServer(server) : emptyForm(),
  );
  const [submitted, setSubmitted] = useState(false);
  const create = useCreateMcpServer();
  const update = useUpdateMcpServer(server?.name ?? "");
  const mutation = editing ? update : create;
  const errors = validateForm(form, { editing, existing });
  const shown = submitted ? errors : {};
  const applies = fieldsFor(form.auth);
  const set = (patch: Partial<McpForm>) => {
    mutation.reset();
    setForm((f) => ({ ...f, ...patch }));
  };

  const submit = () => {
    setSubmitted(true);
    if (hasErrors(errors)) return;
    if (editing) {
      update.mutate(buildUpdateBody(form), {
        onSuccess: (s) => onDone(s.name),
      });
    } else {
      create.mutate(buildCreateBody(form), {
        onSuccess: (s) => onDone(s.name),
      });
    }
  };

  return (
    <Panel title={editing ? `Edit ${server.name}` : "Add MCP server"}>
      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <Field
          label="Name"
          hint="Lowercase letters, digits, - and _ (max 32). Sensors refer to the server by this name."
          error={shown.name ? [shown.name] : undefined}
        >
          <TextInput
            value={form.name}
            readOnly={editing}
            disabled={editing}
            invalid={!!shown.name}
            spellCheck={false}
            autoComplete="off"
            placeholder="notion"
            className="max-w-xs font-mono"
            onChange={(e) => set({ name: e.target.value })}
          />
        </Field>
        <Field
          label="URL"
          hint="The server's Streamable HTTP endpoint: https, or http for localhost only. No credentials or fragment."
          error={shown.url ? [shown.url] : undefined}
        >
          <TextInput
            value={form.url}
            invalid={!!shown.url}
            spellCheck={false}
            autoComplete="off"
            placeholder="https://mcp.example.com/mcp"
            className="font-mono"
            onChange={(e) => set({ url: e.target.value })}
          />
        </Field>

        <fieldset className="m-0 space-y-1.5 border-0 p-0">
          <legend className="mb-1 text-[11px] font-medium text-muted">
            Authentication
          </legend>
          {AUTH_MODES.map((m) => (
            <label
              key={m.key}
              className="flex cursor-pointer items-start gap-2 text-xs"
            >
              <input
                type="radio"
                name="mcp-auth"
                className="mt-0.5"
                checked={form.auth === m.key}
                onChange={() => set({ auth: m.key })}
              />
              <span>
                <span className="font-medium">{m.label}</span>
                <span className="block text-[11px] text-muted">{m.hint}</span>
              </span>
            </label>
          ))}
        </fieldset>

        {applies.clientId && (
          <Field
            label="Client ID"
            error={shown.clientId ? [shown.clientId] : undefined}
          >
            <TextInput
              value={form.clientId}
              invalid={!!shown.clientId}
              spellCheck={false}
              autoComplete="off"
              className="max-w-md font-mono"
              onChange={(e) => set({ clientId: e.target.value })}
            />
          </Field>
        )}
        {applies.clientSecret && (
          <SecretField
            label="Client secret"
            stored={server?.has_client_secret === true}
            value={form.clientSecret}
            remove={form.removeClientSecret}
            optionalHint="Leave empty for a public client."
            onValue={(v) => set({ clientSecret: v, removeClientSecret: false })}
            onRemove={(r) => set({ removeClientSecret: r, clientSecret: "" })}
          />
        )}
        {applies.scopes && (
          <Field
            label="Scopes (optional)"
            hint="Space separated. Empty asks for the scopes the server advertises."
            error={shown.scopes ? [shown.scopes] : undefined}
          >
            <TextInput
              value={form.scopes}
              invalid={!!shown.scopes}
              spellCheck={false}
              autoComplete="off"
              className="font-mono"
              onChange={(e) => set({ scopes: e.target.value })}
            />
          </Field>
        )}
        {applies.header && (
          <Field
            label="Header name"
            error={shown.header ? [shown.header] : undefined}
          >
            <TextInput
              value={form.header}
              invalid={!!shown.header}
              spellCheck={false}
              autoComplete="off"
              placeholder="Authorization"
              className="max-w-xs font-mono"
              onChange={(e) => set({ header: e.target.value })}
            />
          </Field>
        )}
        {applies.apiKey && (
          <SecretField
            label="Header value"
            stored={server?.has_api_key === true}
            value={form.apiKey}
            remove={form.removeApiKey}
            optionalHint="Sent as is: include a scheme such as “Bearer ” when the server needs one."
            onValue={(v) => set({ apiKey: v, removeApiKey: false })}
            onRemove={(r) => set({ removeApiKey: r, apiKey: "" })}
          />
        )}

        <div className="flex flex-wrap items-center gap-2 pt-1">
          <Button variant="primary" type="submit" disabled={mutation.isPending}>
            {mutation.isPending ? "Saving…" : editing ? "Save" : "Add server"}
          </Button>
          <Button onClick={onCancel} disabled={mutation.isPending}>
            Cancel
          </Button>
          {mutation.isError && (
            <span role="alert" className="text-xs text-bad">
              {errorMessage(mutation.error)}
            </span>
          )}
        </div>
        {mutation.isError && (
          <ul className="m-0 list-none space-y-1 p-0 text-xs text-bad">
            {diagnosticMessages(mutation.error).map((m) => (
              <li key={m}>{m}</li>
            ))}
          </ul>
        )}
      </form>
    </Panel>
  );
}

/** A write-only password input: blank keeps the stored value, "Remove stored value" deletes it. */
function SecretField({
  label,
  stored,
  value,
  remove,
  optionalHint,
  onValue,
  onRemove,
}: {
  label: string;
  stored: boolean;
  value: string;
  remove: boolean;
  optionalHint: string;
  onValue: (v: string) => void;
  onRemove: (remove: boolean) => void;
}) {
  const keeping = stored && !remove;
  return (
    <div className="space-y-1">
      <Field
        label={label}
        hint={
          keeping
            ? `A value is stored. Leave empty to keep it, or type a new one to replace it. ${optionalHint}`
            : optionalHint
        }
      >
        <TextInput
          type="password"
          value={value}
          disabled={remove}
          autoComplete="new-password"
          spellCheck={false}
          placeholder={keeping ? "stored" : ""}
          className="max-w-md font-mono"
          onChange={(e) => onValue(e.target.value)}
        />
      </Field>
      {stored && (
        <label className="flex cursor-pointer items-center gap-2 text-xs">
          <input
            type="checkbox"
            checked={remove}
            onChange={(e) => onRemove(e.target.checked)}
          />
          Remove stored value
        </label>
      )}
    </div>
  );
}
