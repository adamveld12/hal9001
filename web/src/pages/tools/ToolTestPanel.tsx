import { useState } from "react";
import { errorMessage } from "../../api/client";
import { useTestTool } from "../../api/tools";
import { Button, Field, Select, TextInput } from "../../components/ui";
import { ToolResultView } from "./ToolResultView";
import { argFields, buildArgs, initialValue } from "./toolModel";

/** Args form generated from the saved tool's schema, a Run button and the result. */
export function ToolTestPanel({
  toolId,
  schema,
  disabled,
}: {
  toolId: string;
  schema: Record<string, unknown> | null | undefined;
  disabled: boolean;
}) {
  const fields = argFields(schema);
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(argFields(schema).map((f) => [f.name, initialValue(f)])),
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const test = useTestTool(toolId);

  const run = () => {
    const built = buildArgs(fields, values);
    setErrors(built.errors);
    if (Object.keys(built.errors).length === 0) test.mutate(built.args);
  };

  return (
    <div className="space-y-3">
      {fields.length === 0 && (
        <p className="m-0 text-xs text-muted">This tool takes no arguments.</p>
      )}
      <div className="grid gap-3 sm:grid-cols-2">
        {fields.map((f) => {
          const err = errors[f.name] ? [errors[f.name]] : undefined;
          const set = (v: string) => setValues((p) => ({ ...p, [f.name]: v }));
          return (
            <Field
              key={f.name}
              label={`${f.name}${f.required ? " *" : ""}`}
              hint={f.description}
              error={err}
            >
              {f.enum.length > 0 ? (
                <Select
                  value={values[f.name] ?? ""}
                  onChange={(e) => set(e.target.value)}
                >
                  {!f.required && <option value="" />}
                  {f.enum.map((v) => (
                    <option key={String(v)}>{String(v)}</option>
                  ))}
                </Select>
              ) : f.type === "boolean" ? (
                <Select
                  value={values[f.name] ?? ""}
                  onChange={(e) => set(e.target.value)}
                >
                  <option value="" />
                  <option>true</option>
                  <option>false</option>
                </Select>
              ) : (
                <TextInput
                  value={values[f.name] ?? ""}
                  placeholder={
                    f.type === "array" ||
                    f.type === "object" ||
                    f.type === "json"
                      ? "JSON"
                      : f.type
                  }
                  invalid={err !== undefined}
                  onChange={(e) => set(e.target.value)}
                />
              )}
            </Field>
          );
        })}
      </div>
      <div className="flex items-center gap-2">
        <Button
          icon="play"
          variant="primary"
          disabled={disabled || test.isPending}
          onClick={run}
        >
          {test.isPending ? "Running…" : "Run tool"}
        </Button>
        {disabled && (
          <span className="text-[11px] text-muted">
            Save your changes first: the test runs the saved tool.
          </span>
        )}
      </div>
      {test.isError && (
        <div role="alert" className="text-xs text-bad">
          {errorMessage(test.error)}
        </div>
      )}
      {test.data && <ToolResultView result={test.data} />}
    </div>
  );
}
