// The form body of each step kind. Forms write plain objects; the caller
// normalizes the step (steps.ts) before it reaches the node.

import { useRef, type RefObject } from "react";
import {
  Button,
  Select,
  TextArea,
  TextInput,
  Toggle,
} from "../../components/ui";
import { ConditionGroup } from "./ConditionEditor";
import { FieldPicker, FormField, Messages, TypedValueInput } from "./controls";
import { hasError, type StepMessage } from "./diagnostics";
import {
  arrayFields,
  plainFields,
  relativeFields,
  WHOLE,
  type FieldInfo,
} from "./fields";
import {
  AGGREGATE_LABELS,
  AGGREGATE_TAKES_OF,
  FUNCTION_LABELS,
  newRule,
  VALUE_KIND_LABELS,
} from "./steps";
import type {
  AggregateOp,
  TransformRule,
  TransformStep,
  TransformValue,
  ValueFunction,
  ValueKind,
} from "./types";

export interface FormProps {
  step: TransformStep;
  /** Fields this step can read: the sample plus what earlier steps add. */
  fields: readonly FieldInfo[];
  /** Replace the step. `key` names a field being typed in (merged into one undo step). */
  onChange: (next: TransformStep, key?: string) => void;
  msgs: (path: string) => StepMessage[];
}

/** Fields inside the elements of the list at `list`, with the element itself first. */
function elementFields(
  fields: readonly FieldInfo[],
  list: string,
): FieldInfo[] {
  const rel = relativeFields(fields, list);
  if (rel.some((f) => f.path === WHOLE)) return rel;
  const nested = rel.length > 0;
  return [
    {
      path: WHOLE,
      type: nested ? "object" : "unknown",
      example: "the element itself",
    },
    ...rel,
  ];
}

function ListField({
  step,
  fields,
  onChange,
  msgs,
  allowWhole = true,
  label = "List",
}: FormProps & { allowWhole?: boolean; label?: string }) {
  const lists = arrayFields(fields).filter(
    (f) => allowWhole || f.path !== WHOLE,
  );
  return (
    <FormField label={label} msgs={msgs("field")}>
      <FieldPicker
        label={label}
        value={step.field ?? ""}
        fields={lists}
        placeholder="Choose the list field"
        invalid={hasError(msgs("field"))}
        onChange={(field, typing) =>
          onChange({ ...step, field }, typing ? "field" : undefined)
        }
      />
    </FormField>
  );
}

function ElementField({
  step,
  fields,
  onChange,
  msgs,
  prop,
  label,
  hint,
}: FormProps & {
  prop: "by" | "of" | "group_by";
  label: string;
  hint?: string;
}) {
  return (
    <FormField label={label} hint={hint} msgs={msgs(prop)}>
      <FieldPicker
        label={label}
        value={step[prop] ?? ""}
        fields={elementFields(fields, step.field ?? "")}
        placeholder="Choose a field of each element"
        invalid={hasError(msgs(prop))}
        onChange={(v, typing) =>
          onChange({ ...step, [prop]: v }, typing ? prop : undefined)
        }
      />
    </FormField>
  );
}

function KeepSelect({
  step,
  onChange,
  msgs,
  noun,
}: Pick<FormProps, "step" | "onChange" | "msgs"> & { noun: string }) {
  return (
    <FormField label={`What to do with ${noun} that match`} msgs={msgs("keep")}>
      <Select
        aria-label={`What to do with ${noun} that match`}
        value={step.keep === false ? "drop" : "keep"}
        onChange={(e) => onChange({ ...step, keep: e.target.value === "keep" })}
      >
        <option value="keep">Keep them and drop the rest</option>
        <option value="drop">Drop them and keep the rest</option>
      </Select>
    </FormField>
  );
}

function FilterForm(p: FormProps) {
  return (
    <>
      <ConditionGroup
        value={p.step}
        onChange={(v, key) => p.onChange({ ...p.step, ...v }, key)}
        fields={p.fields}
        path="conditions"
        matchPath="match"
        msgs={p.msgs}
      />
      <KeepSelect {...p} noun="items" />
    </>
  );
}

function RouteForm({ step, fields, onChange, msgs }: FormProps) {
  const rules = step.rules ?? [];
  const setRule = (i: number, rule: TransformRule, key?: string) =>
    onChange(
      { ...step, rules: rules.map((r, j) => (j === i ? rule : r)) },
      key,
    );
  return (
    <>
      <p className="m-0 text-[11px] text-muted">
        Each output name becomes a connection point on the node. The first rule
        an item matches decides where it goes.
      </p>
      {rules.map((rule, i) => (
        <div
          key={i}
          className="space-y-2 rounded-md border border-line p-2"
          data-testid="route-rule"
        >
          <div className="flex items-start gap-1.5">
            <div className="min-w-0 flex-1">
              <FormField
                label={`Rule ${i + 1}: send to output`}
                msgs={msgs(`rules[${i}].port`)}
              >
                <TextInput
                  aria-label={`Output name of rule ${i + 1}`}
                  className="font-mono"
                  value={rule.port}
                  placeholder="output name"
                  invalid={hasError(msgs(`rules[${i}].port`))}
                  onChange={(e) =>
                    setRule(
                      i,
                      { ...rule, port: e.target.value },
                      `rules[${i}].port`,
                    )
                  }
                />
              </FormField>
            </div>
            <Button
              variant="ghost"
              icon="close"
              className="mt-4"
              aria-label={`Remove rule ${i + 1}`}
              title="Remove this rule"
              onClick={() =>
                onChange({ ...step, rules: rules.filter((_, j) => j !== i) })
              }
            />
          </div>
          <div className="text-[11px] font-medium text-muted">When</div>
          <ConditionGroup
            value={rule}
            onChange={(v, key) => setRule(i, { ...rule, ...v }, key)}
            fields={fields}
            path={`rules[${i}].conditions`}
            matchPath={`rules[${i}].match`}
            msgs={msgs}
          />
          <Messages list={msgs(`rules[${i}]`)} />
        </div>
      ))}
      <Messages list={msgs("rules")} />
      <Button
        icon="plus"
        onClick={() =>
          onChange({
            ...step,
            rules: [...rules, newRule(rules.map((r) => r.port))],
          })
        }
      >
        Add rule
      </Button>
      <FormField
        label="Everything else goes to"
        hint="Items that match no rule. Empty means “out”."
        msgs={msgs("default_port")}
      >
        <TextInput
          aria-label="Output for items that match no rule"
          className="font-mono"
          value={step.default_port ?? ""}
          placeholder="out"
          invalid={hasError(msgs("default_port"))}
          onChange={(e) =>
            onChange({ ...step, default_port: e.target.value }, "default_port")
          }
        />
      </FormField>
    </>
  );
}

/** The chosen fields as rows (with optional rename) and a box to add more. */
function FieldListForm({
  step,
  fields,
  onChange,
  msgs,
  rename,
}: FormProps & { rename: boolean }) {
  const chosen = step.fields ?? [];
  const offered = plainFields(fields).filter((f) => !chosen.includes(f.path));
  return (
    <>
      <FormField label="Fields" msgs={msgs("fields")}>
        <ul className="m-0 list-none space-y-1 p-0">
          {chosen.map((f, i) => (
            <li key={`${i}:${f}`} className="space-y-0.5">
              <div className="flex items-center gap-1.5">
                <span className="min-w-0 flex-1 truncate rounded border border-line bg-raised px-1.5 py-1 font-mono text-xs">
                  {f}
                </span>
                {rename && (
                  <TextInput
                    aria-label={`New name for ${f}`}
                    className="w-36 shrink-0"
                    placeholder="rename (optional)"
                    value={step.rename?.[f] ?? ""}
                    onChange={(e) =>
                      onChange(
                        {
                          ...step,
                          rename: { ...step.rename, [f]: e.target.value },
                        },
                        `rename.${f}`,
                      )
                    }
                  />
                )}
                <Button
                  variant="ghost"
                  icon="close"
                  aria-label={`Remove ${f}`}
                  onClick={() =>
                    onChange({
                      ...step,
                      fields: chosen.filter((_, j) => j !== i),
                    })
                  }
                />
              </div>
              <Messages list={msgs(`fields[${i}]`)} />
            </li>
          ))}
        </ul>
        <FieldPicker
          clearOnPick
          label="Add a field"
          value=""
          fields={offered}
          placeholder="Add a field…"
          invalid={hasError(msgs("fields"))}
          onChange={(f) => onChange({ ...step, fields: [...chosen, f] })}
        />
      </FormField>
      <Messages list={msgs("rename")} />
    </>
  );
}

function PickForm(p: FormProps) {
  return (
    <>
      <FieldListForm {...p} rename />
      <FormField
        label="Nested fields"
        hint="Turns author.login into one field called author_login."
        msgs={p.msgs("flatten")}
      >
        <Toggle
          label="Flatten nested fields into one level"
          checked={p.step.flatten === true}
          onChange={(v) => p.onChange({ ...p.step, flatten: v })}
        />
      </FormField>
    </>
  );
}

function templateInsert(
  ref: RefObject<HTMLTextAreaElement | null>,
  text: string,
  path: string,
): { text: string; caret: number } {
  const el = ref.current;
  const start = el?.selectionStart ?? text.length;
  const end = el?.selectionEnd ?? start;
  const token = `{${path}}`;
  return {
    text: text.slice(0, start) + token + text.slice(end),
    caret: start + token.length,
  };
}

const NEW_VALUE: Record<ValueKind, TransformValue> = {
  constant: { kind: "constant", value: "" },
  field: { kind: "field" },
  template: { kind: "template", template: "" },
  function: { kind: "function", function: "lowercase" },
};

const FUNCTION_ARG_DEFAULT: Partial<Record<ValueFunction, unknown>> = {
  join: ", ",
  split: ",",
  default: "",
};

function SetValueForm({ step, fields, onChange, msgs }: FormProps) {
  const v: TransformValue = step.value ?? NEW_VALUE.constant;
  const area = useRef<HTMLTextAreaElement>(null);
  const setValue = (next: TransformValue, key?: string) =>
    onChange({ ...step, value: next }, key);
  const readable = fields.filter((f) => f.path !== WHOLE);

  return (
    <>
      <FormField label="Value is" msgs={msgs("value.kind")}>
        <Select
          aria-label="Kind of value"
          value={v.kind}
          onChange={(e) => {
            const kind = e.target.value as ValueKind;
            const carry =
              (kind === "field" || kind === "function") &&
              (v.kind === "field" || v.kind === "function")
                ? v.field
                : undefined;
            setValue({
              ...NEW_VALUE[kind],
              ...(carry ? { field: carry } : {}),
            });
          }}
        >
          {(Object.keys(VALUE_KIND_LABELS) as ValueKind[]).map((k) => (
            <option key={k} value={k}>
              {VALUE_KIND_LABELS[k]}
            </option>
          ))}
        </Select>
      </FormField>

      {v.kind === "constant" && (
        <FormField label="Value" msgs={msgs("value.value")}>
          <TypedValueInput
            label="Constant value"
            value={v.value}
            tags={["string", "number", "boolean", "null"]}
            invalid={hasError(msgs("value.value"))}
            onChange={(value, typing) =>
              setValue(
                { kind: "constant", value },
                typing ? "value" : undefined,
              )
            }
          />
        </FormField>
      )}

      {v.kind === "field" && (
        <FormField
          label="Copy from"
          hint="A path with [] gives a list of every match."
          msgs={msgs("value.field")}
        >
          <FieldPicker
            label="Field to copy"
            value={v.field ?? ""}
            fields={readable}
            invalid={hasError(msgs("value.field"))}
            onChange={(field, typing) =>
              setValue(
                { kind: "field", field },
                typing ? "value.field" : undefined,
              )
            }
          />
        </FormField>
      )}

      {v.kind === "template" && (
        <FormField
          label="Text"
          hint="Put a field in braces, like {author.login}. Write {{ and }} for literal braces."
          msgs={msgs("value.template")}
        >
          <TextArea
            ref={area}
            rows={2}
            aria-label="Text template"
            value={v.template ?? ""}
            invalid={hasError(msgs("value.template"))}
            placeholder="{author.login} opened {title}"
            onChange={(e) =>
              setValue(
                { kind: "template", template: e.target.value },
                "value.template",
              )
            }
          />
          <Select
            aria-label="Insert a field into the text"
            value=""
            disabled={readable.length === 0}
            onChange={(e) => {
              if (e.target.value === "") return;
              const { text, caret } = templateInsert(
                area,
                v.template ?? "",
                e.target.value,
              );
              setValue({ kind: "template", template: text });
              requestAnimationFrame(() => {
                area.current?.focus();
                area.current?.setSelectionRange(caret, caret);
              });
            }}
          >
            <option value="">Insert a field…</option>
            {readable
              .filter((f) => !f.path.includes("[]"))
              .map((f) => (
                <option key={f.path} value={f.path}>
                  {f.path} ({f.example})
                </option>
              ))}
          </Select>
        </FormField>
      )}

      {v.kind === "function" && (
        <>
          <FormField label="Field" msgs={msgs("value.field")}>
            <FieldPicker
              label="Field to calculate on"
              value={v.field ?? ""}
              fields={readable}
              invalid={hasError(msgs("value.field"))}
              onChange={(field, typing) =>
                setValue({ ...v, field }, typing ? "value.field" : undefined)
              }
            />
          </FormField>
          <FormField label="Calculation" msgs={msgs("value.function")}>
            <Select
              aria-label="Calculation"
              value={v.function ?? ""}
              invalid={hasError(msgs("value.function"))}
              onChange={(e) => {
                const fn = e.target.value as ValueFunction;
                setValue({
                  kind: "function",
                  field: v.field,
                  function: fn,
                  arg: FUNCTION_ARG_DEFAULT[fn],
                });
              }}
            >
              {v.function === undefined && <option value="">Choose…</option>}
              {(Object.keys(FUNCTION_LABELS) as ValueFunction[]).map((f) => (
                <option key={f} value={f}>
                  {FUNCTION_LABELS[f]}
                </option>
              ))}
            </Select>
          </FormField>
          {(v.function === "join" || v.function === "split") && (
            <FormField
              label={
                v.function === "join" ? "Put this between items" : "Split at"
              }
              msgs={msgs("value.arg")}
            >
              <TextInput
                aria-label={
                  v.function === "join" ? "Separator" : "Split at this text"
                }
                value={typeof v.arg === "string" ? v.arg : ""}
                invalid={hasError(msgs("value.arg"))}
                onChange={(e) =>
                  setValue({ ...v, arg: e.target.value }, "value.arg")
                }
              />
            </FormField>
          )}
          {v.function === "default" && (
            <FormField
              label="Fallback value"
              hint="Used when the field is missing, null or empty."
              msgs={msgs("value.arg")}
            >
              <TypedValueInput
                label="Fallback value"
                value={v.arg}
                tags={["string", "number", "boolean"]}
                invalid={hasError(msgs("value.arg"))}
                onChange={(arg, typing) =>
                  setValue({ ...v, arg }, typing ? "value.arg" : undefined)
                }
              />
            </FormField>
          )}
        </>
      )}
    </>
  );
}

function SetForm(p: FormProps) {
  return (
    <>
      <FormField label="Field to write" msgs={p.msgs("field")}>
        <FieldPicker
          label="Field to write"
          value={p.step.field ?? ""}
          fields={plainFields(p.fields)}
          placeholder="Choose or type a field name"
          invalid={hasError(p.msgs("field"))}
          onChange={(field, typing) =>
            p.onChange({ ...p.step, field }, typing ? "field" : undefined)
          }
        />
      </FormField>
      <SetValueForm {...p} />
    </>
  );
}

function SplitForm(p: FormProps) {
  return (
    <>
      <ListField {...p} label="List to split" />
      <FormField
        label="Name for each element (optional)"
        hint="Leave empty to make the element the item itself."
        msgs={p.msgs("as")}
      >
        <TextInput
          aria-label="Name for each element"
          className="font-mono"
          value={p.step.as ?? ""}
          onChange={(e) => p.onChange({ ...p.step, as: e.target.value }, "as")}
        />
      </FormField>
      <Toggle
        label="Keep the rest of the item with each element"
        checked={p.step.keep_parent === true}
        onChange={(v) => p.onChange({ ...p.step, keep_parent: v })}
      />
      <Messages list={p.msgs("keep_parent")} />
    </>
  );
}

function SortForm(p: FormProps) {
  return (
    <>
      <ListField {...p} label="List to sort" />
      <ElementField
        {...p}
        prop="by"
        label="Sort by (optional)"
        hint="A field of each element. Empty sorts the elements themselves."
      />
      <FormField label="Order" msgs={p.msgs("order")}>
        <Select
          aria-label="Order"
          value={p.step.order === "desc" ? "desc" : "asc"}
          onChange={(e) =>
            p.onChange({ ...p.step, order: e.target.value as "asc" | "desc" })
          }
        >
          <option value="asc">Smallest first (A to Z, oldest first)</option>
          <option value="desc">Largest first (Z to A, newest first)</option>
        </Select>
      </FormField>
    </>
  );
}

function LimitForm(p: FormProps) {
  return (
    <>
      <ListField {...p} label="List to shorten" />
      <FormField label="Keep this many" msgs={p.msgs("count")}>
        <TypedValueInput
          label="How many to keep"
          value={p.step.count}
          tags={["number"]}
          invalid={hasError(p.msgs("count"))}
          onChange={(v, typing) =>
            p.onChange(
              { ...p.step, count: typeof v === "number" ? v : undefined },
              typing ? "count" : undefined,
            )
          }
        />
      </FormField>
    </>
  );
}

function UniqueForm(p: FormProps) {
  return (
    <>
      <ListField {...p} label="List to clean up" />
      <ElementField
        {...p}
        prop="by"
        label="Compare by (optional)"
        hint="Elements with the same value in this field count as duplicates. Empty compares whole elements."
      />
    </>
  );
}

function AggregateForm(p: FormProps) {
  const { step, fields, onChange, msgs } = p;
  const op = step.op;
  return (
    <>
      <ListField {...p} label="List to summarize" allowWhole={false} />
      <FormField label="Summary" msgs={msgs("op")}>
        <Select
          aria-label="Summary"
          value={op ?? ""}
          invalid={hasError(msgs("op"))}
          onChange={(e) =>
            onChange({ ...step, op: e.target.value as AggregateOp })
          }
        >
          {op === undefined && <option value="">Choose…</option>}
          {(Object.keys(AGGREGATE_LABELS) as AggregateOp[]).map((o) => (
            <option key={o} value={o}>
              {AGGREGATE_LABELS[o]}
            </option>
          ))}
        </Select>
      </FormField>
      {op && AGGREGATE_TAKES_OF.includes(op) && (
        <ElementField
          {...p}
          prop="of"
          label="Of (optional)"
          hint="A field of each element. Empty uses the element itself."
        />
      )}
      {op === "group_count" && (
        <ElementField {...p} prop="group_by" label="Group by" />
      )}
      <FormField
        label="Put the result in"
        hint="A field of the item; it is created or replaced."
        msgs={msgs("into")}
      >
        <FieldPicker
          label="Field for the result"
          value={step.into ?? ""}
          fields={plainFields(fields)}
          placeholder="Choose or type a field name"
          invalid={hasError(msgs("into"))}
          onChange={(into, typing) =>
            onChange({ ...step, into }, typing ? "into" : undefined)
          }
        />
      </FormField>
    </>
  );
}

function FilterListForm(p: FormProps) {
  const rel = elementFields(p.fields, p.step.field ?? "");
  return (
    <>
      <ListField {...p} label="List to filter" />
      <div className="text-[11px] font-medium text-muted">
        Conditions on each element
      </div>
      <ConditionGroup
        value={p.step}
        onChange={(v, key) => p.onChange({ ...p.step, ...v }, key)}
        fields={rel}
        path="conditions"
        matchPath="match"
        msgs={p.msgs}
        defaultField={WHOLE}
      />
      <KeepSelect {...p} noun="elements" />
    </>
  );
}

export function StepForm(props: FormProps) {
  switch (props.step.type) {
    case "filter":
      return <FilterForm {...props} />;
    case "route":
      return <RouteForm {...props} />;
    case "pick":
      return <PickForm {...props} />;
    case "set":
      return <SetForm {...props} />;
    case "remove":
      return <FieldListForm {...props} rename={false} />;
    case "split":
      return <SplitForm {...props} />;
    case "sort":
      return <SortForm {...props} />;
    case "limit":
      return <LimitForm {...props} />;
    case "unique":
      return <UniqueForm {...props} />;
    case "aggregate":
      return <AggregateForm {...props} />;
    case "filter_list":
      return <FilterListForm {...props} />;
  }
}
