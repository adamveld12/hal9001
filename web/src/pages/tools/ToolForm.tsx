import { useState } from "react";
import type { Tool, ToolParam } from "../../api/tools";
import { CodeEditor, type CodeLang } from "../../components/CodeEditor";
import {
  Button,
  Field,
  Select,
  TextArea,
  TextInput,
  Toggle,
} from "../../components/ui";
import { ConditionGroup } from "../../flow/transform/ConditionEditor";
import type { FieldInfo } from "../../flow/transform/fields";
import {
  PARAM_TYPES,
  TOOL_LANGS,
  paramsToSchema,
  schemaToParams,
  type ParamType,
} from "./toolModel";

const SCRIPT_LANG: Record<string, CodeLang> = {
  bash: "shell",
  python: "python",
  node: "javascript",
};

type ApproveMode = "never" | "always" | "when";
const approveMode = (t: Tool): ApproveMode =>
  t.approve === undefined
    ? "always"
    : typeof t.approve === "string"
      ? t.approve
      : "when";

/** Text of an enum or default value as typed: JSON for non-strings. */
const showValue = (v: unknown) =>
  typeof v === "string" ? v : JSON.stringify(v);
function parseValue(type: ParamType, text: string): unknown {
  if (type === "string") return text;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

function ParamRow({
  name,
  param,
  onChange,
  onRename,
  onRemove,
}: {
  name: string;
  param: ToolParam;
  onChange: (p: ToolParam) => void;
  onRename: (n: string) => void;
  onRemove: () => void;
}) {
  const [nameText, setNameText] = useState(name);
  const type = param.type as ParamType;
  return (
    <div
      className="space-y-1.5 rounded-md border border-line p-2"
      data-testid="param-row"
    >
      <div className="flex flex-wrap items-center gap-2">
        <TextInput
          aria-label="Parameter name"
          className="w-40 font-mono"
          value={nameText}
          onChange={(e) => setNameText(e.target.value)}
          onBlur={() =>
            nameText !== name && nameText !== "" && onRename(nameText)
          }
        />
        <Select
          aria-label={`Type of ${name}`}
          className="w-28"
          value={type}
          onChange={(e) =>
            onChange({
              type: e.target.value as ParamType,
              description: param.description,
              required: param.required,
            })
          }
        >
          {PARAM_TYPES.map((t) => (
            <option key={t}>{t}</option>
          ))}
        </Select>
        <Toggle
          label="required"
          checked={param.required === true}
          onChange={(v) => onChange({ ...param, required: v || undefined })}
        />
        <Button
          className="ml-auto"
          aria-label={`Remove ${name}`}
          onClick={onRemove}
        >
          Remove
        </Button>
      </div>
      <TextInput
        aria-label={`Description of ${name}`}
        placeholder="Description (the agent reads it)"
        value={param.description ?? ""}
        onChange={(e) =>
          onChange({ ...param, description: e.target.value || undefined })
        }
      />
      <div className="flex gap-2">
        <TextInput
          aria-label={`Allowed values of ${name}`}
          placeholder="Allowed values, comma separated (optional)"
          value={(param.enum ?? []).map(showValue).join(", ")}
          onChange={(e) => {
            const items = e.target.value
              .split(",")
              .map((s) => s.trim())
              .filter((s) => s !== "");
            onChange({
              ...param,
              enum: items.length
                ? items.map((s) => parseValue(type, s))
                : undefined,
            });
          }}
        />
        <TextInput
          aria-label={`Default of ${name}`}
          className="w-40"
          placeholder="Default"
          value={
            param.default === undefined || param.default === null
              ? ""
              : showValue(param.default)
          }
          onChange={(e) =>
            onChange({
              ...param,
              default:
                e.target.value === ""
                  ? undefined
                  : parseValue(type, e.target.value),
            })
          }
        />
      </div>
    </div>
  );
}

/** Structured editor of a tool. Every change calls `onChange` with the new tool; the page renders it as YAML. */
export function ToolForm({
  tool,
  idLocked,
  onChange,
}: {
  tool: Tool;
  idLocked: boolean;
  onChange: (t: Tool) => void;
}) {
  const params = tool.params ?? {};
  const rawMode = tool.schema !== undefined;
  const [schemaText, setSchemaText] = useState(() =>
    JSON.stringify(tool.schema ?? {}, null, 2),
  );
  const [schemaError, setSchemaError] = useState<string | null>(null);
  const [rawNotice, setRawNotice] = useState<string | null>(null);
  const names = Object.keys(params);
  const mode = approveMode(tool);
  const when = typeof tool.approve === "object" ? tool.approve.when : undefined;

  const setParams = (p: Record<string, ToolParam>) =>
    onChange({
      ...tool,
      schema: undefined,
      params: Object.keys(p).length ? p : undefined,
    });
  const argFields: FieldInfo[] = names.map((n) => ({
    path: n,
    type:
      params[n].type === "integer"
        ? "number"
        : (params[n].type as FieldInfo["type"]),
    example: "",
  }));

  const toggleRaw = (raw: boolean) => {
    setRawNotice(null);
    if (raw) {
      const s = paramsToSchema(params);
      setSchemaText(JSON.stringify(s, null, 2));
      setSchemaError(null);
      onChange({ ...tool, params: undefined, schema: s });
      return;
    }
    const back = schemaToParams(tool.schema ?? {});
    if (back === null) {
      setRawNotice(
        "This schema uses more than the parameter form can express, so it stays raw.",
      );
      return;
    }
    onChange({
      ...tool,
      schema: undefined,
      params: Object.keys(back).length ? back : undefined,
    });
  };

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <Field
          label="Id"
          hint={
            idLocked
              ? "The id names the file and cannot change here."
              : "Letters, digits, - and _."
          }
        >
          <TextInput
            value={tool.id}
            disabled={idLocked}
            onChange={(e) => onChange({ ...tool, id: e.target.value })}
          />
        </Field>
        <Field label="Timeout" hint="For example 30s. Empty: 1m.">
          <TextInput
            value={tool.timeout ?? ""}
            placeholder="1m"
            onChange={(e) =>
              onChange({ ...tool, timeout: e.target.value || undefined })
            }
          />
        </Field>
      </div>
      <Field
        label="Description"
        hint="The calling agent reads this to decide when to use the tool."
      >
        <TextArea
          rows={2}
          value={tool.description}
          onChange={(e) => onChange({ ...tool, description: e.target.value })}
        />
      </Field>

      <section className="space-y-2" aria-label="Parameters">
        <div className="flex items-center gap-3">
          <h3 className="m-0 text-xs font-semibold">Parameters</h3>
          <Toggle
            label="Raw JSON Schema"
            checked={rawMode}
            onChange={toggleRaw}
          />
        </div>
        {rawNotice && (
          <div role="status" className="text-xs text-warn">
            {rawNotice}
          </div>
        )}
        {rawMode ? (
          <>
            <CodeEditor
              lang="json"
              label="Argument JSON Schema"
              value={schemaText}
              onChange={(v) => {
                setSchemaText(v);
                try {
                  const s = JSON.parse(v) as unknown;
                  if (typeof s !== "object" || s === null || Array.isArray(s))
                    throw new Error("must be an object");
                  setSchemaError(null);
                  onChange({
                    ...tool,
                    params: undefined,
                    schema: s as Record<string, unknown>,
                  });
                } catch (e) {
                  setSchemaError((e as Error).message);
                }
              }}
              minHeight="8rem"
              maxHeight="20rem"
            />
            {schemaError && (
              <div role="alert" className="text-xs text-bad">
                Not valid JSON: {schemaError}
              </div>
            )}
          </>
        ) : (
          <>
            {names.map((n) => (
              <ParamRow
                key={n}
                name={n}
                param={params[n]}
                onChange={(p) => setParams({ ...params, [n]: p })}
                onRename={(to) => {
                  const next: Record<string, ToolParam> = {};
                  for (const k of names) next[k === n ? to : k] = params[k];
                  setParams(next);
                }}
                onRemove={() => {
                  const { [n]: _gone, ...rest } = params;
                  setParams(rest);
                }}
              />
            ))}
            <Button
              icon="plus"
              onClick={() => {
                let i = names.length + 1;
                while (`param${i}` in params) i++;
                setParams({ ...params, [`param${i}`]: { type: "string" } });
              }}
            >
              Add parameter
            </Button>
          </>
        )}
      </section>

      <section className="space-y-2" aria-label="Code">
        <div className="flex items-center gap-3">
          <h3 className="m-0 text-xs font-semibold">Code</h3>
          <Select
            aria-label="Language"
            className="w-28"
            value={tool.lang}
            onChange={(e) =>
              onChange({ ...tool, lang: e.target.value as Tool["lang"] })
            }
          >
            {TOOL_LANGS.map((l) => (
              <option key={l}>{l}</option>
            ))}
          </Select>
        </div>
        {tool.lang === "exec" ? (
          <Field label="Command (argv, one argument per line)">
            <TextArea
              rows={3}
              className="font-mono"
              value={(tool.command ?? []).join("\n")}
              onChange={(e) =>
                onChange({
                  ...tool,
                  script: undefined,
                  command: e.target.value.split("\n"),
                })
              }
            />
          </Field>
        ) : (
          <CodeEditor
            lang={SCRIPT_LANG[tool.lang] ?? "text"}
            label="Tool script"
            value={tool.script ?? ""}
            onChange={(v) =>
              onChange({ ...tool, command: undefined, script: v })
            }
            minHeight="10rem"
            maxHeight="28rem"
          />
        )}
        <p className="m-0 text-[11px] text-muted">
          Arguments arrive as JSON on stdin and as ARG_&lt;NAME&gt; variables.
          Stdout is the result; a non-zero exit is an error.
        </p>
      </section>

      <section className="space-y-2" aria-label="Approval">
        <h3 className="m-0 text-xs font-semibold">Approval</h3>
        <Select
          aria-label="Approval"
          className="w-72"
          value={mode}
          onChange={(e) => {
            const m = e.target.value as ApproveMode;
            onChange({
              ...tool,
              approve:
                m === "when"
                  ? {
                      when: when ?? {
                        conditions: [
                          { field: names[0] ?? "", op: "equals", value: "" },
                        ],
                      },
                    }
                  : m,
            });
          }}
        >
          <option value="never">Never ask (harmless reads)</option>
          <option value="always">Always ask</option>
          <option value="when">Ask when the arguments match rules</option>
        </Select>
        {when && (
          <ConditionGroup
            value={when}
            fields={argFields}
            path="approve.when.conditions"
            matchPath="approve.when.match"
            msgs={() => []}
            onChange={(v) =>
              onChange({
                ...tool,
                approve: {
                  when: { match: v.match, conditions: v.conditions ?? [] },
                },
              })
            }
          />
        )}
      </section>

      <Toggle
        label="Run in the calling node's sandbox (instead of on this machine)"
        checked={tool.runs_in_sandbox === true}
        onChange={(v) => onChange({ ...tool, runs_in_sandbox: v || undefined })}
      />
    </div>
  );
}
