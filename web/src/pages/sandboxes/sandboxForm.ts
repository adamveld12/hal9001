import type { SandboxConfig } from "../../api/sandboxes";

/**
 * Form state of one sandbox backend. Free-text and numeric fields are kept as
 * typed (so a half-typed number or a trailing newline is not rewritten under
 * the cursor); everything else is the config itself.
 */
export interface SandboxForm {
  config: SandboxConfig;
  /** keep_alive as typed; configFromForm validates it. */
  keep_alive: string;
  imagesText: string;
  secretsText: string;
  cpus: string;
  memory_mb: string;
  disk_gb: string;
  timeout_minutes: string;
  port: string;
}

export type FormErrors = Partial<Record<keyof SandboxForm, string>>;

const num = (n: number) => (n === 0 ? "" : String(n));

export function formFromConfig(config: SandboxConfig): SandboxForm {
  return {
    config,
    keep_alive: config.keep_alive,
    imagesText: config.images.join("\n"),
    secretsText: Object.entries(config.secrets)
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    cpus: num(config.resources.cpus),
    memory_mb: num(config.resources.memory_mb),
    disk_gb: num(config.resources.disk_gb),
    timeout_minutes: num(config.timeout_minutes),
    port: num(config.port),
  };
}

/** Non-empty trimmed lines. */
export function textToList(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

const ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

function wholeNumber(text: string, what: string): number | string {
  if (text.trim() === "") return 0;
  if (!/^\d+$/.test(text.trim())) return `${what} must be a whole number`;
  return Number(text.trim());
}

/** A bare zero, the only number the server accepts without a unit. */
const ZERO = /^0+$/;
/** The shape of the API's Duration: one or more number + unit parts. */
const DURATION = /^(\d+(\.\d+)?(ns|us|ms|s|m|h|d))+$/;

/**
 * Checks the keep_alive field by the server's rule: a duration such as 30s, 1m
 * or 2h, from 0 up. A bare 0 is accepted and returned as 0s (the API's
 * duration format needs a unit); 0 removes the sandbox when a run ends.
 * Negative values, text and numbers without a unit are errors.
 */
export function parseKeepAlive(
  text: string,
): { value: string } | { error: string } {
  const t = text.trim();
  if (t === "") {
    return {
      error: "Enter a duration such as 30s, 1m or 2h (0 removes it at once)",
    };
  }
  if (t.startsWith("-")) return { error: "Keep alive must not be negative" };
  if (ZERO.test(t)) return { value: "0s" };
  if (!DURATION.test(t)) {
    return {
      error: `"${t}" is not a duration: use a number and a unit, such as 30s, 1m or 2h`,
    };
  }
  return { value: t };
}

/**
 * The config the form describes, or the errors that prevent it. Only shape
 * errors are found here; the server validates the rest and its diagnostics
 * are shown after a save.
 */
export function configFromForm(
  form: SandboxForm,
): { config: SandboxConfig } | { errors: FormErrors } {
  const errors: FormErrors = {};
  const secrets: Record<string, string> = {};
  for (const line of textToList(form.secretsText)) {
    const i = line.indexOf("=");
    const name = i < 0 ? "" : line.slice(0, i).trim();
    const secret = i < 0 ? "" : line.slice(i + 1).trim();
    if (!ENV_NAME.test(name) || secret === "") {
      errors.secretsText = `"${line}" is not ENV_NAME=secret-name`;
      break;
    }
    secrets[name] = secret;
  }
  const cpus =
    form.cpus.trim() === "" ? 0 : Number(form.cpus.trim().replace(",", "."));
  if (!Number.isFinite(cpus) || cpus < 0) errors.cpus = "CPUs must be a number";
  const keepAlive = parseKeepAlive(form.keep_alive);
  if ("error" in keepAlive) errors.keep_alive = keepAlive.error;
  const ints = {
    memory_mb: wholeNumber(form.memory_mb, "Memory"),
    disk_gb: wholeNumber(form.disk_gb, "Disk"),
    timeout_minutes: wholeNumber(form.timeout_minutes, "Timeout"),
    port: wholeNumber(form.port, "Port"),
  };
  for (const [k, v] of Object.entries(ints)) {
    if (typeof v === "string") errors[k as keyof SandboxForm] = v;
  }
  if ("error" in keepAlive || Object.keys(errors).length > 0) {
    return { errors };
  }
  return {
    config: {
      ...form.config,
      keep_alive: keepAlive.value,
      images: textToList(form.imagesText),
      secrets,
      resources: {
        cpus,
        memory_mb: ints.memory_mb as number,
        disk_gb: ints.disk_gb as number,
      },
      timeout_minutes: ints.timeout_minutes as number,
      port: ints.port as number,
    },
  };
}
