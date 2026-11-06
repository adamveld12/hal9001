// Condition operators: which ones a field type offers, which value each one
// takes, and how a condition is kept consistent when its field or operator
// changes. The rules mirror the TransformCondition contract in
// api/openapi.yaml.

import type { FieldType } from "./fields";
import type { Scalar, TransformCondition, TransformOperator } from "./types";

export const OPERATOR_LABELS: Record<TransformOperator, string> = {
  equals: "is",
  not_equals: "is not",
  contains: "contains",
  not_contains: "does not contain",
  starts_with: "starts with",
  ends_with: "ends with",
  matches: "matches pattern",
  gt: "is greater than",
  gte: "is at least",
  lt: "is less than",
  lte: "is at most",
  exists: "exists",
  not_exists: "does not exist",
  is_empty: "is empty",
  is_not_empty: "is not empty",
  is_true: "is true",
  is_false: "is false",
  in: "is one of",
};

export const ALL_OPERATORS = Object.keys(
  OPERATOR_LABELS,
) as TransformOperator[];

/** What a value looks like: none, any scalar, text only, number or text, a list. */
export type ValueNeed = "none" | "any" | "text" | "compare" | "list";

export function valueNeed(op: TransformOperator): ValueNeed {
  switch (op) {
    case "exists":
    case "not_exists":
    case "is_empty":
    case "is_not_empty":
    case "is_true":
    case "is_false":
      return "none";
    case "starts_with":
    case "ends_with":
    case "matches":
      return "text";
    case "gt":
    case "gte":
    case "lt":
    case "lte":
      return "compare";
    case "in":
      return "list";
    default:
      return "any";
  }
}

export function takesValue(op: TransformOperator): boolean {
  return valueNeed(op) !== "none";
}

/** Operators that accept `case_sensitive`. */
export function isTextOperator(op: TransformOperator): boolean {
  switch (op) {
    case "equals":
    case "not_equals":
    case "contains":
    case "not_contains":
    case "starts_with":
    case "ends_with":
    case "matches":
    case "in":
      return true;
    default:
      return false;
  }
}

const PRESENCE: TransformOperator[] = ["exists", "not_exists"];
const EMPTINESS: TransformOperator[] = ["is_empty", "is_not_empty"];

/** Operators worth offering for a field of this type (all when unknown). */
export function operatorsFor(type: FieldType): TransformOperator[] {
  switch (type) {
    case "string":
      return [
        "equals",
        "not_equals",
        "contains",
        "not_contains",
        "starts_with",
        "ends_with",
        "matches",
        "gt",
        "gte",
        "lt",
        "lte",
        "in",
        ...PRESENCE,
        ...EMPTINESS,
      ];
    case "number":
      return [
        "equals",
        "not_equals",
        "gt",
        "gte",
        "lt",
        "lte",
        "in",
        ...PRESENCE,
      ];
    case "boolean":
      return ["is_true", "is_false", "equals", "not_equals", ...PRESENCE];
    case "array":
    case "object":
      return [...PRESENCE, ...EMPTINESS];
    default:
      return [...ALL_OPERATORS];
  }
}

export type ValueType = "string" | "number" | "boolean";

/** Value types a condition may carry for this operator and field type. */
export function valueTypesFor(
  op: TransformOperator,
  field: FieldType,
): ValueType[] {
  switch (valueNeed(op)) {
    case "none":
      return [];
    case "text":
      return ["string"];
    case "compare":
      return field === "number"
        ? ["number"]
        : field === "string"
          ? ["string"]
          : ["number", "string"];
    default:
      return field === "number"
        ? ["number"]
        : field === "string"
          ? ["string"]
          : field === "boolean"
            ? ["boolean"]
            : ["string", "number", "boolean"];
  }
}

export function valueTypeOf(v: unknown): ValueType | undefined {
  return typeof v === "string" ||
    typeof v === "number" ||
    typeof v === "boolean"
    ? (typeof v as ValueType)
    : undefined;
}

/** Text typed into a value box, as a value of `type`; undefined when it is not one. */
export function coerceText(text: string, type: ValueType): Scalar | undefined {
  switch (type) {
    case "string":
      return text;
    case "number": {
      if (text.trim() === "") return undefined;
      const n = Number(text);
      return Number.isFinite(n) ? n : undefined;
    }
    case "boolean":
      return text === "true" ? true : text === "false" ? false : undefined;
  }
}

/** A list entry typed by the user; unknown fields infer numbers and booleans. */
export function coerceListItem(
  text: string,
  field: FieldType,
): Scalar | undefined {
  const t = text.trim();
  if (t === "") return undefined;
  if (field === "number") return coerceText(t, "number");
  if (field === "boolean") return coerceText(t.toLowerCase(), "boolean");
  if (field === "string") return t;
  if (/^-?\d+(\.\d+)?$/.test(t)) return Number(t);
  if (t === "true" || t === "false") return t === "true";
  return t;
}

function convert(v: unknown, to: ValueType): Scalar | undefined {
  if (valueTypeOf(v) === to) return v as Scalar;
  if (v === undefined || v === null) return undefined;
  if (typeof v === "object") return undefined;
  return coerceText(String(v), to);
}

/** The value after the operator or field type changed: kept when it still fits. */
export function reconcileValue(
  op: TransformOperator,
  field: FieldType,
  value: unknown,
): unknown {
  const need = valueNeed(op);
  if (need === "none") return undefined;
  const types = valueTypesFor(op, field);
  const fit = (v: unknown): Scalar | undefined => {
    if (
      valueTypeOf(v) !== undefined &&
      types.includes(valueTypeOf(v) as ValueType)
    )
      return v as Scalar;
    for (const t of types) {
      const c = convert(v, t);
      if (c !== undefined) return c;
    }
    return undefined;
  };
  if (need === "list") {
    const items = Array.isArray(value)
      ? value
      : value === undefined
        ? []
        : [value];
    const kept = items.map(fit).filter((v) => v !== undefined);
    return kept.length > 0 ? kept : undefined;
  }
  return fit(Array.isArray(value) ? value[0] : value);
}

/** Canonical condition: `field`, `op`, then only the optional parts that apply. */
export function normalizeCondition(c: TransformCondition): TransformCondition {
  const out: TransformCondition = { field: c.field, op: c.op };
  const v = c.value;
  if (
    takesValue(c.op) &&
    v !== undefined &&
    !(Array.isArray(v) && v.length === 0)
  ) {
    out.value = v;
  }
  if (c.case_sensitive === true && isTextOperator(c.op)) {
    out.case_sensitive = true;
  }
  return out;
}

/**
 * The condition after its field changed to one of `field` type: an operator
 * the new type does not offer falls back to `equals` (or the first offered),
 * the value is converted when it can be and dropped when it cannot.
 */
export function reconcileCondition(
  c: TransformCondition,
  field: FieldType,
): TransformCondition {
  const ops = operatorsFor(field);
  const op = ops.includes(c.op)
    ? c.op
    : ops.includes("equals")
      ? "equals"
      : ops[0];
  const textual = field === "string" || field === "unknown";
  return normalizeCondition({
    field: c.field,
    op,
    value: reconcileValue(op, field, c.value),
    case_sensitive: textual ? c.case_sensitive : false,
  });
}

/** The condition with a new operator; the value is reconciled to it. */
export function withOperator(
  c: TransformCondition,
  op: TransformOperator,
  field: FieldType,
): TransformCondition {
  return normalizeCondition({
    ...c,
    op,
    value: reconcileValue(op, field, c.value),
  });
}
