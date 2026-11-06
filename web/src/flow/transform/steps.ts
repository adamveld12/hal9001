// Step catalogue, factories, canonical form and plain-language summaries.
//
// `normalizeStep` writes a step the way the Go side does: `type` first, then
// the fields of that kind in a fixed order, with defaults and empty values
// left out (`match` when all, `keep` when true, `order` when asc, empty lists
// and strings), so editing never rewrites a file differently from the engine.

import { normalizeCondition, OPERATOR_LABELS, takesValue } from "./operators";
import { WHOLE } from "./fields";
import type {
  AggregateOp,
  StepKind,
  TransformCondition,
  TransformRule,
  TransformStep,
  TransformValue,
  ValueFunction,
  ValueKind,
} from "./types";

export interface StepInfo {
  /** Card header. */
  title: string;
  /** One line shown in the "Add step" menu. */
  description: string;
}

export const STEP_INFO: Record<StepKind, StepInfo> = {
  filter: {
    title: "Keep only items where…",
    description: "Drop the items that don't match your conditions.",
  },
  route: {
    title: "Send to different outputs when…",
    description:
      "Send items to separate outputs, such as urgent and normal. Must be the last step.",
  },
  pick: {
    title: "Choose fields",
    description: "Keep only the fields you list and discard the rest.",
  },
  set: {
    title: "Add or change a field",
    description:
      "Write a fixed value, a copy of another field, some text, or a calculated value.",
  },
  remove: {
    title: "Remove fields",
    description: "Delete the fields you don't need.",
  },
  split: {
    title: "Turn a list into separate items",
    description: "Send one item for each element of a list.",
  },
  sort: {
    title: "Sort a list",
    description: "Put the elements of a list in order.",
  },
  limit: {
    title: "Keep the first N of a list",
    description: "Cut a list down to a maximum length.",
  },
  unique: {
    title: "Remove duplicates from a list",
    description: "Keep one of each repeated element.",
  },
  aggregate: {
    title: "Summarize a list",
    description:
      "Count, add up, average or collect the elements of a list into one field.",
  },
  filter_list: {
    title: "Filter a list",
    description: "Keep or remove the list elements that match conditions.",
  },
};

/** Header of a card; a filter that drops matches reads differently. */
export function stepTitle(step: TransformStep): string {
  if (step.type === "filter" && step.keep === false) {
    return "Drop items where…";
  }
  return STEP_INFO[step.type].title;
}

export const AGGREGATE_LABELS: Record<AggregateOp, string> = {
  count: "Count the elements",
  sum: "Add up (sum)",
  avg: "Average",
  min: "Smallest",
  max: "Largest",
  collect: "Collect into a list",
  group_count: "Count per value",
};

/** Aggregations that read a value from each element. */
export const AGGREGATE_TAKES_OF: readonly AggregateOp[] = [
  "sum",
  "avg",
  "min",
  "max",
  "collect",
];

export const FUNCTION_LABELS: Record<ValueFunction, string> = {
  lowercase: "Lowercase",
  uppercase: "Uppercase",
  trim: "Trim spaces",
  length: "Length",
  join: "Join a list into text",
  first: "First of a list",
  last: "Last of a list",
  split: "Split text into a list",
  to_number: "Convert to a number",
  to_string: "Convert to text",
  default: "Use a fallback when empty",
};

export const VALUE_KIND_LABELS: Record<ValueKind, string> = {
  constant: "A fixed value",
  field: "A copy of another field",
  template: "Text with fields inserted",
  function: "A calculation on a field",
};

export function newCondition(field = ""): TransformCondition {
  return { field, op: "equals" };
}

/** A route rule with a port name not in `taken`. */
export function newRule(taken: readonly string[]): TransformRule {
  let n = 1;
  while (taken.includes(`match-${n}`)) n++;
  return { port: `match-${n}`, conditions: [newCondition()] };
}

export function newStep(kind: StepKind): TransformStep {
  switch (kind) {
    case "filter":
      return { type: "filter", conditions: [newCondition()] };
    case "route":
      return { type: "route", rules: [newRule([])] };
    case "pick":
      return { type: "pick" };
    case "set":
      return { type: "set", value: { kind: "constant", value: "" } };
    case "remove":
      return { type: "remove" };
    case "split":
      return { type: "split" };
    case "sort":
      return { type: "sort" };
    case "limit":
      return { type: "limit", count: 10 };
    case "unique":
      return { type: "unique" };
    case "aggregate":
      return { type: "aggregate", op: "count" };
    case "filter_list":
      return { type: "filter_list", conditions: [newCondition(WHOLE)] };
  }
}

type Entry = [string, unknown];

/** Keys whose empty string is a real value (a port is required, a constant may be ""). */
const KEEP_EMPTY = new Set(["port", "value", "arg"]);

function build<T>(entries: Entry[]): T {
  const out: Record<string, unknown> = {};
  for (const [k, v] of entries) {
    if (v === undefined) continue;
    if (v === "" && !KEEP_EMPTY.has(k)) continue;
    if (Array.isArray(v) && v.length === 0) continue;
    out[k] = v;
  }
  return out as T;
}

const anyMatch = (m: unknown) => (m === "any" ? "any" : undefined);
const falseOnly = (v: unknown) => (v === false ? false : undefined);
const trueOnly = (v: unknown) => (v === true ? true : undefined);

function normalizeValue(v: TransformValue): TransformValue {
  switch (v.kind) {
    case "constant":
      return build<TransformValue>([
        ["kind", v.kind],
        ["value", v.value],
      ]);
    case "field":
      return build<TransformValue>([
        ["kind", v.kind],
        ["field", v.field],
      ]);
    case "template":
      return build<TransformValue>([
        ["kind", v.kind],
        ["template", v.template],
      ]);
    case "function":
      return build<TransformValue>([
        ["kind", v.kind],
        ["field", v.field],
        ["function", v.function],
        [
          "arg",
          v.function === "join" ||
          v.function === "split" ||
          v.function === "default"
            ? v.arg
            : undefined,
        ],
      ]);
  }
}

function normalizeRule(r: TransformRule): TransformRule {
  return build<TransformRule>([
    ["port", r.port],
    ["match", anyMatch(r.match)],
    ["conditions", r.conditions?.map(normalizeCondition)],
  ]);
}

export function normalizeStep(s: TransformStep): TransformStep {
  const conditions = s.conditions?.map(normalizeCondition);
  switch (s.type) {
    case "filter":
      return build([
        ["type", s.type],
        ["match", anyMatch(s.match)],
        ["conditions", conditions],
        ["keep", falseOnly(s.keep)],
      ]);
    case "route":
      return build([
        ["type", s.type],
        ["rules", s.rules?.map(normalizeRule)],
        ["default_port", s.default_port === "out" ? undefined : s.default_port],
      ]);
    case "pick": {
      const fields = s.fields ?? [];
      const rename = Object.fromEntries(
        fields.flatMap((f) => {
          const to = s.rename?.[f];
          return to !== undefined && to !== "" ? [[f, to]] : [];
        }),
      );
      return build([
        ["type", s.type],
        ["fields", fields],
        ["rename", Object.keys(rename).length > 0 ? rename : undefined],
        ["flatten", trueOnly(s.flatten)],
      ]);
    }
    case "set":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["value", s.value ? normalizeValue(s.value) : undefined],
      ]);
    case "remove":
      return build([
        ["type", s.type],
        ["fields", s.fields],
      ]);
    case "split":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["as", s.as],
        ["keep_parent", trueOnly(s.keep_parent)],
      ]);
    case "sort":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["by", s.by],
        ["order", s.order === "desc" ? "desc" : undefined],
      ]);
    case "limit":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["count", s.count],
      ]);
    case "unique":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["by", s.by],
      ]);
    case "aggregate":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["into", s.into],
        ["op", s.op],
        ["of", s.op && AGGREGATE_TAKES_OF.includes(s.op) ? s.of : undefined],
        ["group_by", s.op === "group_count" ? s.group_by : undefined],
      ]);
    case "filter_list":
      return build([
        ["type", s.type],
        ["field", s.field],
        ["match", anyMatch(s.match)],
        ["conditions", conditions],
        ["keep", falseOnly(s.keep)],
      ]);
  }
}

/** Index at which to insert a new step: before a trailing route step, which must stay last. */
export function insertIndex(steps: readonly TransformStep[]): number {
  const route = steps.findIndex((s) => s.type === "route");
  return route >= 0 && route === steps.length - 1 ? route : steps.length;
}

/** Move the item at `from` so it ends up at index `to` of the resulting list. */
export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  if (from === to || from < 0 || from >= list.length) return [...list];
  const next = [...list];
  const [item] = next.splice(from, 1);
  next.splice(Math.max(0, Math.min(to, next.length)), 0, item);
  return next;
}

// ---------------------------------------------------------------- summaries

function show(v: unknown): string {
  if (typeof v === "string") return `“${v}”`;
  if (Array.isArray(v)) return v.map(show).join(", ");
  return JSON.stringify(v) ?? "";
}

export function describeField(path: string | undefined): string {
  if (path === undefined || path === "") return "…";
  return path === WHOLE ? "the value" : path;
}

export function describeCondition(c: TransformCondition): string {
  const base = `${describeField(c.field)} ${OPERATOR_LABELS[c.op]}`;
  return takesValue(c.op) && c.value !== undefined
    ? `${base} ${show(c.value)}`
    : base;
}

function describeConditions(
  match: TransformStep["match"],
  conditions: readonly TransformCondition[] | undefined,
): string {
  if (!conditions || conditions.length === 0) return "…";
  return conditions
    .map(describeCondition)
    .join(match === "any" ? " or " : " and ");
}

function describeValue(v: TransformValue | undefined): string {
  if (!v) return "…";
  switch (v.kind) {
    case "constant":
      return v.value === undefined ? "…" : show(v.value);
    case "field":
      return `a copy of ${describeField(v.field)}`;
    case "template":
      return v.template ? `“${v.template}”` : "…";
    case "function":
      return `${v.function ? FUNCTION_LABELS[v.function].toLowerCase() : "…"} of ${describeField(v.field)}`;
  }
}

const listOf = (fields: readonly string[] | undefined) =>
  fields && fields.length > 0 ? fields.join(", ") : "…";

/** One short plain-language sentence for a step, used where the server's wording is not available. */
export function summarizeStep(s: TransformStep): string {
  switch (s.type) {
    case "filter":
      return `${s.keep === false ? "Drop" : "Keep only"} items where ${describeConditions(s.match, s.conditions)}`;
    case "route": {
      const rules = (s.rules ?? []).map((r) => r.port || "…").join(", ");
      return `Send items to ${rules || "…"}, everything else to ${s.default_port || "out"}`;
    }
    case "pick":
      return `Keep only ${listOf(s.fields)}`;
    case "set":
      return `Set ${describeField(s.field)} to ${describeValue(s.value)}`;
    case "remove":
      return `Remove ${listOf(s.fields)}`;
    case "split":
      return `One item per element of ${describeField(s.field)}`;
    case "sort":
      return `Sort ${describeField(s.field)}${s.by ? ` by ${s.by}` : ""}, ${s.order === "desc" ? "largest first" : "smallest first"}`;
    case "limit":
      return `Keep the first ${s.count ?? "…"} of ${describeField(s.field)}`;
    case "unique":
      return `Remove duplicates from ${describeField(s.field)}${s.by ? ` by ${s.by}` : ""}`;
    case "aggregate":
      return `${s.op ? AGGREGATE_LABELS[s.op] : "Summarize"} of ${describeField(s.field)} into ${describeField(s.into)}`;
    case "filter_list":
      return `${s.keep === false ? "Remove" : "Keep"} elements of ${describeField(s.field)} where ${describeConditions(s.match, s.conditions)}`;
  }
}
