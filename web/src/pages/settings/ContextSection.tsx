import { useState } from "react";
import { errorMessage } from "../../api/client";
import { useSettings, useUpdateSettings } from "../../api/settings";
import {
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  TextArea,
} from "../../components/ui";
import {
  byteLength,
  MAX_SYSTEM_PROMPT_BYTES,
  systemPromptError,
} from "./contextForm";
import { diagnosticMessages } from "./problem";

/** Context agents always get. More fields (each its own Panel) can be added below System Prompt. */
export function ContextSection() {
  const settings = useSettings();
  return (
    <div className="space-y-4">
      {settings.isPending && <LoadingState />}
      {settings.isError && (
        <ErrorState
          error={settings.error}
          onRetry={() => void settings.refetch()}
        />
      )}
      {settings.isSuccess && (
        <SystemPromptPanel
          // Remount on a new hash so the draft starts from the saved value.
          key={settings.data.hash}
          saved={settings.data.config.system_prompt ?? ""}
        />
      )}
    </div>
  );
}

function SystemPromptPanel({ saved }: { saved: string }) {
  const update = useUpdateSettings();
  const [draft, setDraft] = useState<string | null>(null);
  const value = draft ?? saved;
  const dirty = draft !== null && draft !== saved;
  const error = systemPromptError(value);
  const bytes = byteLength(value);

  return (
    <Panel title="System Prompt">
      <Field
        label="System Prompt"
        hint="Added to every agent call: flows, chat assistant, transforms. Use it for your name, preferences and facts agents should always know."
        error={error ? [error] : undefined}
      >
        <TextArea
          rows={14}
          value={value}
          invalid={!!error}
          spellCheck={false}
          placeholder="My name is … I prefer …"
          onChange={(e) => {
            update.reset();
            setDraft(e.target.value);
          }}
        />
      </Field>
      <div className="mt-1 text-[11px] text-muted">
        {bytes.toLocaleString()} / {MAX_SYSTEM_PROMPT_BYTES.toLocaleString()}{" "}
        bytes. Takes effect for new agent runs and chat turns.
      </div>
      <div className="mt-3 flex items-center gap-2">
        <Button
          variant="primary"
          disabled={!dirty || !!error || update.isPending}
          onClick={() =>
            update.mutate(
              { system_prompt: value },
              { onSuccess: () => setDraft(null) },
            )
          }
        >
          {update.isPending ? "Saving…" : "Save"}
        </Button>
        <Button
          disabled={!dirty || update.isPending}
          onClick={() => {
            setDraft(null);
            update.reset();
          }}
        >
          Revert
        </Button>
        {dirty && <span className="text-xs text-warn">Unsaved changes</span>}
        {update.isError && (
          <span role="alert" className="text-xs text-bad">
            {errorMessage(update.error)}
          </span>
        )}
        {update.isSuccess && !dirty && (
          <span className="text-xs text-ok">Saved.</span>
        )}
      </div>
      {update.isError && (
        <ul className="m-0 mt-2 list-none space-y-1 p-0 text-xs text-bad">
          {diagnosticMessages(update.error).map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      )}
    </Panel>
  );
}
