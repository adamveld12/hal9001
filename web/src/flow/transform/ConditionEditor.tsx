// Editor for a group of conditions: "Match all/any of these", one row per
// condition (field, operator, value, case sensitivity) and add/remove.

import { Button, Select, Toggle } from "../../components/ui";
import { hasError, type StepMessage } from "./diagnostics";
import { fieldType, type FieldInfo } from "./fields";
import {
  isTextOperator,
  OPERATOR_LABELS,
  operatorsFor,
  reconcileCondition,
  valueNeed,
  valueTypesFor,
  withOperator,
} from "./operators";
import { newCondition } from "./steps";
import type {
  Scalar,
  TransformCondition,
  TransformMatch,
  TransformOperator,
} from "./types";
import { ChipsInput, FieldPicker, Messages, TypedValueInput } from "./controls";

export interface ConditionGroupValue {
  match?: TransformMatch;
  conditions?: TransformCondition[];
}

type Msgs = (path: string) => StepMessage[];

function ValueEditor({
  cond,
  type,
  label,
  invalid,
  onChange,
}: {
  cond: TransformCondition;
  type: FieldInfo["type"];
  label: string;
  invalid: boolean;
  onChange: (c: TransformCondition, typing: boolean) => void;
}) {
  const need = valueNeed(cond.op);
  if (need === "none") return null;
  if (need === "list") {
    const values = Array.isArray(cond.value)
      ? (cond.value as unknown[]).filter(
          (v): v is Scalar =>
            typeof v === "string" ||
            typeof v === "number" ||
            typeof v === "boolean",
        )
      : [];
    return (
      <ChipsInput
        label={label}
        values={values}
        fieldType={type}
        invalid={invalid}
        onChange={(v) =>
          onChange({ ...cond, value: v.length > 0 ? v : undefined }, false)
        }
      />
    );
  }
  return (
    <TypedValueInput
      label={label}
      value={cond.value}
      tags={valueTypesFor(cond.op, type)}
      invalid={invalid}
      placeholder={
        cond.op === "matches"
          ? "regular expression"
          : cond.op === "gt" ||
              cond.op === "gte" ||
              cond.op === "lt" ||
              cond.op === "lte"
            ? "number or date, e.g. 2025-01-31"
            : "value"
      }
      onChange={(v, typing) => onChange({ ...cond, value: v }, typing)}
    />
  );
}

function ConditionRow({
  cond,
  fields,
  path,
  msgs,
  onChange,
  onRemove,
}: {
  cond: TransformCondition;
  fields: readonly FieldInfo[];
  path: string;
  msgs: Msgs;
  onChange: (c: TransformCondition, key?: string) => void;
  onRemove: () => void;
}) {
  const type = fieldType(fields, cond.field);
  const offered = operatorsFor(type);
  const ops: TransformOperator[] = offered.includes(cond.op)
    ? offered
    : [cond.op, ...offered];
  const fieldMsgs = msgs(`${path}.field`);
  const opMsgs = msgs(`${path}.op`);
  const valueMsgs = msgs(`${path}.value`);
  const caseMsgs = msgs(`${path}.case_sensitive`);
  const rowMsgs = msgs(path);
  const n = path.slice(path.lastIndexOf("[") + 1, -1);
  const name = `condition ${Number(n) + 1}`;
  const textual = type === "string" || type === "unknown";

  return (
    <div
      className="space-y-1.5 rounded-md border border-line bg-bg/40 p-1.5"
      data-testid="condition-row"
    >
      <div className="flex items-start gap-1.5">
        <div className="min-w-0 flex-1">
          <FieldPicker
            label={`Field of ${name}`}
            value={cond.field}
            fields={fields}
            invalid={hasError(fieldMsgs)}
            onChange={(field, typing) =>
              onChange(
                reconcileCondition(
                  { ...cond, field },
                  fieldType(fields, field),
                ),
                typing ? `${path}.field` : undefined,
              )
            }
          />
        </div>
        <Button
          variant="ghost"
          icon="close"
          aria-label={`Remove ${name}`}
          title="Remove this condition"
          onClick={onRemove}
        />
      </div>
      <Messages list={fieldMsgs} />
      <div className="flex items-start gap-1.5">
        <Select
          aria-label={`Test of ${name}`}
          className="w-40 shrink-0"
          value={cond.op}
          invalid={hasError(opMsgs)}
          onChange={(e) =>
            onChange(
              withOperator(cond, e.target.value as TransformOperator, type),
            )
          }
        >
          {ops.map((o) => (
            <option key={o} value={o}>
              {OPERATOR_LABELS[o]}
            </option>
          ))}
        </Select>
        <div className="min-w-0 flex-1">
          <ValueEditor
            cond={cond}
            type={type}
            label={`Value of ${name}`}
            invalid={hasError(valueMsgs)}
            onChange={(c, typing) =>
              onChange(c, typing ? `${path}.value` : undefined)
            }
          />
        </div>
      </div>
      <Messages list={opMsgs} />
      <Messages list={valueMsgs} />
      {isTextOperator(cond.op) && textual && (
        <Toggle
          label="Match upper and lower case exactly"
          checked={cond.case_sensitive === true}
          onChange={(v) =>
            onChange({ ...cond, case_sensitive: v ? true : undefined })
          }
        />
      )}
      <Messages list={caseMsgs} />
      <Messages list={rowMsgs} />
    </div>
  );
}

export function ConditionGroup({
  value,
  onChange,
  fields,
  path,
  matchPath,
  defaultField,
  msgs,
}: {
  value: ConditionGroupValue;
  onChange: (v: ConditionGroupValue, key?: string) => void;
  fields: readonly FieldInfo[];
  /** Diagnostic path of the condition list: `conditions` or `rules[0].conditions`. */
  path: string;
  matchPath: string;
  /** Field of a newly added condition. */
  defaultField?: string;
  msgs: Msgs;
}) {
  const conditions = value.conditions ?? [];
  const match = value.match ?? "all";
  const setAt = (i: number, c: TransformCondition, key?: string) =>
    onChange(
      { ...value, conditions: conditions.map((x, j) => (j === i ? c : x)) },
      key,
    );

  return (
    <div className="space-y-1.5">
      {conditions.length > 1 && (
        <div className="flex items-center gap-1.5 text-xs">
          <span>Match</span>
          <Select
            aria-label="Match all or any condition"
            className="w-28"
            value={match}
            invalid={hasError(msgs(matchPath))}
            onChange={(e) =>
              onChange({ ...value, match: e.target.value as TransformMatch })
            }
          >
            <option value="all">all (and)</option>
            <option value="any">any (or)</option>
          </Select>
          <span className="text-muted">of these conditions</span>
        </div>
      )}
      <Messages list={msgs(matchPath)} />
      {conditions.map((c, i) => (
        <div key={i} className="space-y-1">
          {i > 0 && (
            <div className="px-1 text-[11px] font-medium uppercase tracking-wide text-muted">
              {match === "any" ? "or" : "and"}
            </div>
          )}
          <ConditionRow
            cond={c}
            fields={fields}
            path={`${path}[${i}]`}
            msgs={msgs}
            onChange={(next, key) => setAt(i, next, key)}
            onRemove={() =>
              onChange({
                ...value,
                conditions: conditions.filter((_, j) => j !== i),
              })
            }
          />
        </div>
      ))}
      <Messages list={msgs(path)} />
      <Button
        icon="plus"
        onClick={() =>
          onChange({
            ...value,
            conditions: [...conditions, newCondition(defaultField)],
          })
        }
      >
        Add condition
      </Button>
    </div>
  );
}
