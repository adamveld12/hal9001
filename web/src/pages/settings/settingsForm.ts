import type { Config, SettingsPatch } from "../../api/settings";

export const PRIORITIES = ["low", "normal", "high", "urgent"] as const;
export type Priority = (typeof PRIORITIES)[number];

/** Daemon defaults for omitted `config.yaml` keys (internal/spec/model.go). */
export const DEFAULTS = {
  max_agent_runs: 2,
  max_command_runs: 8,
  retention_days: 30,
  dedup_retention_days: 180,
  notify_min_priority: "normal",
  approval_timeout: "1h",
} as const;

export const INT_FIELDS = [
  "max_agent_runs",
  "max_command_runs",
  "retention_days",
  "dedup_retention_days",
] as const;
export type IntField = (typeof INT_FIELDS)[number];

/** Editable settings as the form holds them: numbers stay text until validated. */
export interface SettingsForm {
  max_agent_runs: string;
  max_command_runs: string;
  retention_days: string;
  dedup_retention_days: string;
  notify_min_priority: Priority;
  approval_timeout: string;
}

export type FormErrors = Partial<Record<keyof SettingsForm, string>>;

const DURATION = /^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h|d))+$/;

function isPriority(v: string): v is Priority {
  return (PRIORITIES as readonly string[]).includes(v);
}

/** Form values for a config, filling the daemon's defaults for omitted keys. */
export function formFromConfig(c: Config): SettingsForm {
  const priority = c.notify_min_priority;
  return {
    max_agent_runs: String(c.max_agent_runs ?? DEFAULTS.max_agent_runs),
    max_command_runs: String(c.max_command_runs ?? DEFAULTS.max_command_runs),
    retention_days: String(c.retention_days ?? DEFAULTS.retention_days),
    dedup_retention_days: String(
      c.dedup_retention_days ?? DEFAULTS.dedup_retention_days,
    ),
    notify_min_priority: priority ?? DEFAULTS.notify_min_priority,
    approval_timeout: c.approval_timeout ?? DEFAULTS.approval_timeout,
  };
}

/** Problems that make the form unsendable, by field. Empty when valid. */
export function validateForm(f: SettingsForm): FormErrors {
  const errors: FormErrors = {};
  for (const k of INT_FIELDS) {
    if (!/^[0-9]+$/.test(f[k].trim()) || Number(f[k]) < 1) {
      errors[k] = "Whole number, 1 or more.";
    }
  }
  if (!isPriority(f.notify_min_priority)) {
    errors.notify_min_priority = "Choose a priority.";
  }
  if (!DURATION.test(f.approval_timeout.trim())) {
    errors.approval_timeout = "Duration such as 30m, 1h, or 2d.";
  }
  return errors;
}

/**
 * Patch holding only the fields that differ from `base`, with `expected_hash`
 * set so a concurrent edit of config.yaml is a conflict. Undefined when
 * nothing changed. The form must be valid (see validateForm).
 */
export function buildPatch(
  base: SettingsForm,
  form: SettingsForm,
  expectedHash: string,
): SettingsPatch | undefined {
  const patch: SettingsPatch = { expected_hash: expectedHash };
  let changed = false;
  for (const k of INT_FIELDS) {
    if (form[k].trim() !== base[k].trim()) {
      patch[k] = Number(form[k]);
      changed = true;
    }
  }
  if (form.notify_min_priority !== base.notify_min_priority) {
    patch.notify_min_priority = form.notify_min_priority;
    changed = true;
  }
  if (form.approval_timeout.trim() !== base.approval_timeout.trim()) {
    patch.approval_timeout = form.approval_timeout.trim();
    changed = true;
  }
  return changed ? patch : undefined;
}

/** A form being edited, remembering which config version it started from. */
export interface Draft {
  form: SettingsForm;
  baseHash: string;
}

/** True when config.yaml changed on disk after the draft was started. */
export function isStale(draft: Draft | null, currentHash: string): boolean {
  return draft !== null && draft.baseHash !== currentHash;
}
