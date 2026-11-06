import { describe, expect, it } from "vitest";
import {
  buildSensorYaml,
  EMPTY_SENSOR_FORM,
  validateSensorForm,
  type SensorForm,
} from "./skeleton";

const form = (over: Partial<SensorForm>): SensorForm => ({
  ...EMPTY_SENSOR_FORM,
  id: "github-notifications",
  script: "gh api notifications --paginate -q '.[]'\n",
  key: "{{.id}}",
  ...over,
});

describe("buildSensorYaml", () => {
  it("writes the §5 layout for a scripted interval sensor", () => {
    const yaml = buildSensorYaml(
      form({ interval: "2m", key: "{{.id}}:{{.updated_at}}", timeout: "60s" }),
    );
    expect(yaml).toBe(
      [
        "id: github-notifications",
        "interval: 2m",
        "lang: bash",
        "script: |",
        "  gh api notifications --paginate -q '.[]'",
        'key: "{{.id}}:{{.updated_at}}"',
        "first_run: baseline",
        "timeout: 60s",
        "",
      ].join("\n"),
    );
  });

  it("uses cron instead of interval and quotes the expression", () => {
    const yaml = buildSensorYaml(
      form({ schedule: "cron", cron: "*/5 9-18 * * 1-5" }),
    );
    expect(yaml).toContain('cron: "*/5 9-18 * * 1-5"');
    expect(yaml).not.toContain("interval:");
  });

  it("writes neither interval nor cron for a manual sensor", () => {
    const yaml = buildSensorYaml(form({ schedule: "manual", cron: "ignored" }));
    expect(yaml).not.toContain("interval:");
    expect(yaml).not.toContain("cron:");
    expect(yaml).toContain("lang: bash");
  });

  it("writes exec sensors with an argv list and no script", () => {
    const yaml = buildSensorYaml(
      form({ lang: "exec", command: "gog\n gmail \nsearch\n\n" }),
    );
    expect(yaml).toContain(
      "lang: exec\ncommand:\n  - gog\n  - gmail\n  - search\n",
    );
    expect(yaml).not.toContain("script");
  });

  it("omits optional fields that are empty and keeps key and first_run", () => {
    const yaml = buildSensorYaml(form({ firstRun: "fire" }));
    expect(yaml).toContain('key: "{{.id}}"');
    expect(yaml).not.toContain("timeout:");
    expect(yaml).toContain("first_run: fire");
  });

  it("quotes an id that YAML would read as a number", () => {
    expect(buildSensorYaml(form({ id: "123" }))).toMatch(/^id: "123"\n/);
  });
});

describe("validateSensorForm", () => {
  it("requires a dedup key, which the server also requires", () => {
    expect(validateSensorForm(form({ key: "  " })).key).toMatch(/Required/);
  });

  it("accepts a complete form", () => {
    expect(validateSensorForm(form({}))).toEqual({});
  });

  it("requires the id and rejects invalid or taken ids", () => {
    expect(validateSensorForm(form({ id: "" })).id).toBe("Required.");
    expect(validateSensorForm(form({ id: "a b" })).id).toMatch(/letters/);
    expect(validateSensorForm(form({ id: "dup" }), ["dup"]).id).toMatch(
      /exists/,
    );
  });

  it("requires the field that belongs to the chosen schedule", () => {
    expect(validateSensorForm(form({ interval: "" })).interval).toBe(
      "Required.",
    );
    expect(validateSensorForm(form({ interval: "soon" })).interval).toMatch(
      /duration/,
    );
    const cron = validateSensorForm(form({ schedule: "cron", interval: "" }));
    expect(cron.cron).toBe("Required.");
    expect(cron.interval).toBeUndefined();
    const manual = validateSensorForm(
      form({ schedule: "manual", interval: "", cron: "" }),
    );
    expect(manual.interval).toBeUndefined();
    expect(manual.cron).toBeUndefined();
  });

  it("requires a script, or a command for exec", () => {
    expect(validateSensorForm(form({ script: "  \n" })).script).toBe(
      "Required.",
    );
    const exec = validateSensorForm(form({ lang: "exec", script: "" }));
    expect(exec.command).toBe("Required.");
    expect(exec.script).toBeUndefined();
  });

  it("checks the timeout only when given", () => {
    expect(validateSensorForm(form({ timeout: "" })).timeout).toBeUndefined();
    expect(validateSensorForm(form({ timeout: "1 minute" })).timeout).toMatch(
      /duration/,
    );
  });
});
