import type { Schemas } from "../../api/client";
import { yamlBlock, yamlList, yamlScalar } from "../files/yaml";

export type Lang = Schemas["Lang"];

export const LANGS: readonly Lang[] = ["bash", "python", "node", "jq", "exec"];

export interface SensorForm {
  id: string;
  schedule: "interval" | "cron";
  interval: string;
  cron: string;
  lang: Lang;
  /** Script text for every lang except `exec`. */
  script: string;
  /** argv for `exec`, one argument per line. */
  command: string;
  /** Go template for the dedup key; empty uses the server default. */
  key: string;
  firstRun: Schemas["FirstRun"];
  /** Duration such as `60s`; empty uses the server default. */
  timeout: string;
}

export const EMPTY_SENSOR_FORM: SensorForm = {
  id: "",
  schedule: "interval",
  interval: "5m",
  cron: "",
  lang: "bash",
  script: "",
  command: "",
  key: "",
  firstRun: "baseline",
  timeout: "",
};

export const SLUG = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;
const DURATION = /^([0-9]+(\.[0-9]+)?(ns|us|ms|s|m|h|d))+$/;

export type FormErrors = Partial<Record<keyof SensorForm, string>>;

/** argv entries of the `exec` field: one per non-blank line. */
export function commandArgs(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

/** Problems that would make the skeleton unusable; the server still validates the result. */
export function validateSensorForm(
  f: SensorForm,
  existingIds: readonly string[] = [],
): FormErrors {
  const e: FormErrors = {};
  if (f.id === "") e.id = "Required.";
  else if (!SLUG.test(f.id)) e.id = "Use letters, digits, - and _ (max 64).";
  else if (existingIds.includes(f.id)) e.id = "A sensor with this id exists.";

  if (f.schedule === "interval") {
    if (f.interval === "") e.interval = "Required.";
    else if (!DURATION.test(f.interval)) e.interval = "Use a duration like 2m.";
  } else if (f.cron.trim() === "") {
    e.cron = "Required.";
  }

  if (f.lang === "exec") {
    if (commandArgs(f.command).length === 0) e.command = "Required.";
  } else if (f.script.trim() === "") {
    e.script = "Required.";
  }

  if (f.timeout !== "" && !DURATION.test(f.timeout)) {
    e.timeout = "Use a duration like 60s.";
  }
  return e;
}

/** Sensor file text (§5) for the form. Optional fields that are empty are left out. */
export function buildSensorYaml(f: SensorForm): string {
  const lines = [`id: ${yamlScalar(f.id)}`];
  lines.push(
    f.schedule === "interval"
      ? `interval: ${yamlScalar(f.interval)}`
      : `cron: ${yamlScalar(f.cron.trim())}`,
  );
  lines.push(`lang: ${f.lang}`);
  if (f.lang === "exec")
    lines.push(...yamlList("command", commandArgs(f.command)));
  else lines.push(...yamlBlock("script", f.script));
  if (f.key.trim() !== "") lines.push(`key: ${yamlScalar(f.key.trim())}`);
  lines.push(`first_run: ${f.firstRun}`);
  if (f.timeout !== "") lines.push(`timeout: ${yamlScalar(f.timeout)}`);
  return `${lines.join("\n")}\n`;
}
