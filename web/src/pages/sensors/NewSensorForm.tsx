import { useState } from "react";
import { errorMessage } from "../../api/client";
import { useCreateSensor } from "../../api/sensors";
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
  buildSensorYaml,
  EMPTY_SENSOR_FORM,
  LANGS,
  validateSensorForm,
  type SensorForm,
} from "./skeleton";

/** Form that builds the YAML skeleton of a new sensor and creates the file. */
export function NewSensorForm({
  existingIds,
  onCreated,
  onCancel,
}: {
  existingIds: readonly string[];
  onCreated: (id: string) => void;
  onCancel: () => void;
}) {
  const [form, setForm] = useState<SensorForm>(EMPTY_SENSOR_FORM);
  const [submitted, setSubmitted] = useState(false);
  const create = useCreateSensor();
  const set = <K extends keyof SensorForm>(k: K, v: SensorForm[K]) =>
    setForm((f) => ({ ...f, [k]: v }));

  const errors = validateSensorForm(form, existingIds);
  const invalid = Object.keys(errors).length > 0;
  // Show field errors once the user has tried to create, or for the id as it is typed.
  const show = (k: keyof SensorForm) =>
    submitted || (k === "id" && form.id !== "") ? errors[k] : undefined;
  const fieldErrors = (k: keyof SensorForm) => {
    const m = show(k);
    return m ? [m] : undefined;
  };
  const yaml = buildSensorYaml(form);
  const serverDiagnostics = diagnosticsOf(create.error);
  const otherError =
    create.error && !serverDiagnostics ? errorMessage(create.error) : null;

  return (
    <form
      className="p-4"
      onSubmit={(e) => {
        e.preventDefault();
        setSubmitted(true);
        if (invalid) return;
        create.mutate(yaml, { onSuccess: (d) => onCreated(d.id) });
      }}
    >
      <Panel
        title="New sensor"
        actions={
          <>
            <Button onClick={onCancel}>Cancel</Button>
            <Button
              type="submit"
              variant="primary"
              icon="plus"
              disabled={create.isPending}
            >
              {create.isPending ? "Creating…" : "Create sensor"}
            </Button>
          </>
        }
      >
        <div className="grid gap-4 lg:grid-cols-2">
          <div className="space-y-3">
            <Field
              label="Id"
              hint="File name: sensors/<id>.yaml"
              error={fieldErrors("id")}
            >
              <TextInput
                value={form.id}
                invalid={show("id") !== undefined}
                onChange={(e) => set("id", e.target.value)}
                autoFocus
              />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Schedule">
                <Select
                  value={form.schedule}
                  onChange={(e) =>
                    set("schedule", e.target.value as SensorForm["schedule"])
                  }
                >
                  <option value="interval">Interval</option>
                  <option value="cron">Cron</option>
                </Select>
              </Field>
              {form.schedule === "interval" ? (
                <Field label="Every" error={fieldErrors("interval")}>
                  <TextInput
                    value={form.interval}
                    placeholder="2m"
                    invalid={show("interval") !== undefined}
                    onChange={(e) => set("interval", e.target.value)}
                  />
                </Field>
              ) : (
                <Field label="Cron expression" error={fieldErrors("cron")}>
                  <TextInput
                    value={form.cron}
                    placeholder="*/5 9-18 * * 1-5"
                    invalid={show("cron") !== undefined}
                    onChange={(e) => set("cron", e.target.value)}
                  />
                </Field>
              )}
            </div>
            <Field label="Language">
              <Select
                value={form.lang}
                onChange={(e) =>
                  set("lang", e.target.value as SensorForm["lang"])
                }
              >
                {LANGS.map((l) => (
                  <option key={l} value={l}>
                    {l}
                  </option>
                ))}
              </Select>
            </Field>
            {form.lang === "exec" ? (
              <Field
                label="Command"
                hint="One argument per line."
                error={fieldErrors("command")}
              >
                <TextArea
                  rows={4}
                  value={form.command}
                  invalid={show("command") !== undefined}
                  onChange={(e) => set("command", e.target.value)}
                />
              </Field>
            ) : (
              <Field
                label="Script"
                hint="Prints a JSON array or JSONL of items to stdout."
                error={fieldErrors("script")}
              >
                <TextArea
                  rows={6}
                  value={form.script}
                  invalid={show("script") !== undefined}
                  onChange={(e) => set("script", e.target.value)}
                />
              </Field>
            )}
            <Field
              label="Dedup key"
              hint="Go template over the item that gives its dedup key."
              error={fieldErrors("key")}
            >
              <TextInput
                value={form.key}
                invalid={show("key") !== undefined}
                placeholder="{{.id}}:{{.updated_at}}"
                onChange={(e) => set("key", e.target.value)}
              />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field
                label="First run"
                hint="Baseline marks existing items seen without firing."
              >
                <Select
                  value={form.firstRun}
                  onChange={(e) =>
                    set("firstRun", e.target.value as SensorForm["firstRun"])
                  }
                >
                  <option value="baseline">baseline</option>
                  <option value="fire">fire</option>
                </Select>
              </Field>
              <Field
                label="Timeout"
                hint="Empty uses the server default."
                error={fieldErrors("timeout")}
              >
                <TextInput
                  value={form.timeout}
                  placeholder="60s"
                  invalid={show("timeout") !== undefined}
                  onChange={(e) => set("timeout", e.target.value)}
                />
              </Field>
            </div>
          </div>
          <div className="space-y-3">
            <div className="text-[11px] font-medium text-muted">
              File preview
            </div>
            <CodeEditor lang="yaml" value={yaml} readOnly minHeight="12rem" />
            {otherError && (
              <div role="alert" className="text-xs text-bad">
                {otherError}
              </div>
            )}
            {serverDiagnostics && (
              <DiagnosticsList diagnostics={serverDiagnostics} source={yaml} />
            )}
          </div>
        </div>
      </Panel>
    </form>
  );
}
