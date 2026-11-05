import { useState } from "react";
import { errorMessage } from "../../api/client";
import { useCreatePrompt } from "../../api/prompts";
import { CodeEditor } from "../../components/CodeEditor";
import {
  Button,
  Field,
  Panel,
  Select,
  TextArea,
  TextInput,
} from "../../components/ui";
import { diagnosticsOf } from "../files/diagnostics";
import { DiagnosticsList } from "../files/DiagnosticsList";
import {
  buildPromptMarkdown,
  EMPTY_PROMPT_FORM,
  validatePromptForm,
  type PromptForm,
  type PromptOutput,
} from "./skeleton";

/** Form that builds the front matter of a new prompt and creates the file. */
export function NewPromptForm({
  existingNames,
  onCreated,
  onCancel,
}: {
  existingNames: readonly string[];
  onCreated: (name: string) => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<PromptForm>(EMPTY_PROMPT_FORM);
  const [submitted, setSubmitted] = useState(false);
  const create = useCreatePrompt();
  const set = <K extends keyof PromptForm>(k: K, v: PromptForm[K]) =>
    setForm((f) => ({ ...f, [k]: v }));

  const errors = validatePromptForm(form, existingNames);
  const nameError =
    (submitted || form.name !== "") && errors.name ? [errors.name] : undefined;
  const markdown = buildPromptMarkdown(form);
  const serverDiagnostics = diagnosticsOf(create.error);
  const otherError =
    create.error && !serverDiagnostics ? errorMessage(create.error) : null;

  return (
    <form
      className="p-4"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (errors.name) return;
        create.mutate(markdown, { onSuccess: (d) => onCreated(d.name) });
      }}
    >
      <Panel
        title="New prompt"
        actions={
          <>
            <Button onClick={onCancel}>Cancel</Button>
            <Button
              type="submit"
              variant="primary"
              icon="plus"
              disabled={create.isPending}
            >
              {create.isPending ? "Creating…" : "Create prompt"}
            </Button>
          </>
        }
      >
        <div className="grid gap-4 lg:grid-cols-2">
          <div className="space-y-3">
            <Field
              label="Name"
              hint="File name: prompts/<name>.md"
              error={nameError}
            >
              <TextInput
                value={form.name}
                invalid={nameError !== undefined}
                onChange={(e) => set("name", e.target.value)}
                autoFocus
              />
            </Field>
            <Field
              label="Description"
              hint="Shown in the agent node's dropdown."
            >
              <TextInput
                value={form.description}
                onChange={(e) => set("description", e.target.value)}
              />
            </Field>
            <Field
              label="Output"
              hint="Note: the agent returns a structured inbox note. None: free text."
            >
              <Select
                value={form.output}
                onChange={(e) => set("output", e.target.value as PromptOutput)}
              >
                <option value="none">none</option>
                <option value="note">note</option>
              </Select>
            </Field>
            <Field
              label="Body"
              hint="Go template. Sensor item fields are under .input."
            >
              <TextArea
                rows={8}
                value={form.body}
                onChange={(e) => set("body", e.target.value)}
              />
            </Field>
          </div>
          <div className="space-y-3">
            <div className="text-[11px] font-medium text-muted">
              File preview
            </div>
            <CodeEditor
              lang="markdown"
              value={markdown}
              readOnly
              minHeight="12rem"
            />
            {otherError && (
              <div role="alert" className="text-xs text-bad">
                {otherError}
              </div>
            )}
            {serverDiagnostics && (
              <DiagnosticsList
                diagnostics={serverDiagnostics}
                source={markdown}
              />
            )}
          </div>
        </div>
      </Panel>
    </form>
  );
}
