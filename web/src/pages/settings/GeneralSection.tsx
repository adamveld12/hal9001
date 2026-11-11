import { useState } from "react";
import { errorMessage } from "../../api/client";
import {
  useSettings,
  useUpdateSettings,
  type Settings,
} from "../../api/settings";
import {
  Button,
  ErrorState,
  Field,
  LoadingState,
  Panel,
  Select,
  TextInput,
} from "../../components/ui";
import { usePageContext } from "../../layout/pageContext";
import { factList } from "../../lib/contextSummary";
import { KillSwitchPanel } from "./KillSwitchPanel";
import { diagnosticMessages, isConflict } from "./problem";
import {
  buildPatch,
  formFromConfig,
  isStale,
  PRIORITIES,
  validateForm,
  type Draft,
  type SettingsForm,
} from "./settingsForm";

export function GeneralSection() {
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
        <SettingsEditor
          settings={settings.data}
          onReload={() => void settings.refetch()}
        />
      )}
      <KillSwitchPanel />
    </div>
  );
}

function SettingsEditor({
  settings,
  onReload,
}: {
  settings: Settings;
  onReload: () => void;
}) {
  const { config, hash } = settings;
  const update = useUpdateSettings();
  const [draft, setDraft] = useState<Draft | null>(null);

  const base = formFromConfig(config);
  const form = draft?.form ?? base;
  const errors = validateForm(form);
  const stale = isStale(draft, hash) || isConflict(update.error);
  const patch = stale ? undefined : buildPatch(base, form, hash);
  const invalid = Object.keys(errors).length > 0;

  const edit = <K extends keyof SettingsForm>(k: K, v: SettingsForm[K]) =>
    setDraft({ form: { ...form, [k]: v }, baseHash: draft?.baseHash ?? hash });

  const reload = () => {
    setDraft(null);
    update.reset();
    onReload();
  };

  const save = () => {
    if (!patch) return;
    update.mutate(patch, { onSuccess: () => setDraft(null) });
  };

  const diagnostics = diagnosticMessages(update.error);
  usePageContext({
    title: "Settings: General",
    facts: factList([
      ["max agent runs", base.max_agent_runs],
      ["max command runs", base.max_command_runs],
      ["run retention days", base.retention_days],
      ["dedup retention days", base.dedup_retention_days],
      ["notify min priority", base.notify_min_priority],
      ["approval timeout", base.approval_timeout],
      ["unsaved edits", draft !== null || undefined],
      ["form errors", invalid ? Object.keys(errors).join(", ") : undefined],
    ]),
  });

  return (
    <>
      {stale && (
        <div
          role="alert"
          className="flex items-center gap-3 rounded-md border border-warn/40 bg-warn/5 p-3 text-xs"
        >
          <span>
            config.yaml changed after you started editing. Reload to see the
            current values; your edits are discarded.
          </span>
          <Button className="ml-auto" icon="refresh" onClick={reload}>
            Reload
          </Button>
        </div>
      )}

      <Panel title="Concurrency">
        <div className="grid grid-cols-2 gap-3">
          <Field
            label="Max agent runs"
            hint="Agent runs executing at once."
            error={errors.max_agent_runs ? [errors.max_agent_runs] : undefined}
          >
            <TextInput
              inputMode="numeric"
              value={form.max_agent_runs}
              invalid={!!errors.max_agent_runs}
              onChange={(e) => edit("max_agent_runs", e.target.value)}
            />
          </Field>
          <Field
            label="Max command runs"
            hint="Command nodes executing at once."
            error={
              errors.max_command_runs ? [errors.max_command_runs] : undefined
            }
          >
            <TextInput
              inputMode="numeric"
              value={form.max_command_runs}
              invalid={!!errors.max_command_runs}
              onChange={(e) => edit("max_command_runs", e.target.value)}
            />
          </Field>
        </div>
      </Panel>

      <Panel title="Retention">
        <div className="grid grid-cols-2 gap-3">
          <Field
            label="Retention (days)"
            hint="Runs, events, blobs, and worktrees older than this are pruned nightly. Items linked to a starred inbox item or a pending approval are kept."
            error={errors.retention_days ? [errors.retention_days] : undefined}
          >
            <TextInput
              inputMode="numeric"
              value={form.retention_days}
              invalid={!!errors.retention_days}
              onChange={(e) => edit("retention_days", e.target.value)}
            />
          </Field>
          <Field
            label="Dedup retention (days)"
            hint="How long dedup keys are kept, so old items don't fire again."
            error={
              errors.dedup_retention_days
                ? [errors.dedup_retention_days]
                : undefined
            }
          >
            <TextInput
              inputMode="numeric"
              value={form.dedup_retention_days}
              invalid={!!errors.dedup_retention_days}
              onChange={(e) => edit("dedup_retention_days", e.target.value)}
            />
          </Field>
        </div>
      </Panel>

      <Panel title="Notifications and approvals">
        <div className="grid grid-cols-2 gap-3">
          <Field
            label="Notify at or above"
            hint="Inbox items below this priority don't send a notification. Every new approval does."
            error={
              errors.notify_min_priority
                ? [errors.notify_min_priority]
                : undefined
            }
          >
            <Select
              value={form.notify_min_priority}
              onChange={(e) =>
                edit(
                  "notify_min_priority",
                  e.target.value as SettingsForm["notify_min_priority"],
                )
              }
            >
              {PRIORITIES.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </Select>
          </Field>
          <Field
            label="Approval timeout"
            hint="Default wait for a tool-call approval before it is rejected. Agent nodes can override it."
            error={
              errors.approval_timeout ? [errors.approval_timeout] : undefined
            }
          >
            <TextInput
              value={form.approval_timeout}
              invalid={!!errors.approval_timeout}
              onChange={(e) => edit("approval_timeout", e.target.value)}
            />
          </Field>
        </div>
      </Panel>

      <div className="flex items-center gap-2">
        <Button
          variant="primary"
          icon="save"
          disabled={!patch || invalid || update.isPending}
          onClick={save}
        >
          {update.isPending ? "Saving…" : "Save"}
        </Button>
        <Button
          disabled={draft === null || update.isPending}
          onClick={() => {
            setDraft(null);
            update.reset();
          }}
        >
          Discard
        </Button>
        {update.isError && !isConflict(update.error) && (
          <span role="alert" className="text-xs text-bad">
            {errorMessage(update.error)}
          </span>
        )}
        {update.isSuccess && draft === null && (
          <span className="text-xs text-ok">Saved.</span>
        )}
      </div>
      {diagnostics.length > 0 && (
        <ul className="m-0 list-none space-y-1 p-0 text-xs text-bad">
          {diagnostics.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      )}

      <Panel title="Set in config.yaml">
        <dl className="m-0 grid grid-cols-[10rem_1fr] gap-x-3 gap-y-1.5 text-xs">
          <dt className="text-muted">Session TTL</dt>
          <dd className="m-0 font-mono">{config.session_ttl ?? "7d"}</dd>
          <dt className="text-muted">Allowed hosts</dt>
          <dd className="m-0 font-mono">
            {config.allowed_hosts?.length
              ? config.allowed_hosts.join(", ")
              : "none (localhost only)"}
          </dd>
          <dt className="text-muted">Allowed logins</dt>
          <dd className="m-0 font-mono">
            {config.allowed_logins?.length
              ? config.allowed_logins.join(", ")
              : "owner of this Mac"}
          </dd>
        </dl>
        <p className="mb-0 mt-3 text-[11px] text-muted">
          These, runtimes, and the auth stacks are edited in config.yaml and
          reload automatically.
        </p>
      </Panel>
    </>
  );
}
