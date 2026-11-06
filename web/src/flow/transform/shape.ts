// Approximate field list after a step, so the pickers of a later step offer
// the fields earlier steps create. It follows the step semantics only as far
// as the pickers need; the server stays the source of truth for results.

import {
  fieldType,
  pathUnder,
  relativeFields,
  typeOfValue,
  WHOLE,
  type FieldInfo,
  type FieldType,
} from "./fields";
import type { TransformStep, TransformValue } from "./types";

function valueType(
  v: TransformValue | undefined,
  fields: readonly FieldInfo[],
): FieldType {
  if (!v) return "unknown";
  switch (v.kind) {
    case "constant":
      return typeOfValue(v.value);
    case "field": {
      const src = v.field ?? "";
      return src.includes("[]") ? "array" : fieldType(fields, src);
    }
    case "template":
      return "string";
    case "function":
      switch (v.function) {
        case "lowercase":
        case "uppercase":
        case "trim":
        case "join":
        case "to_string":
          return "string";
        case "length":
        case "to_number":
          return "number";
        case "split":
          return "array";
        default:
          return "unknown";
      }
  }
}

function upsert(fields: FieldInfo[], f: FieldInfo): FieldInfo[] {
  const i = fields.findIndex((x) => x.path === f.path);
  if (i < 0) return [...fields, f];
  const next = [...fields];
  next[i] = f;
  return next;
}

export function fieldsAfterStep(
  fields: readonly FieldInfo[],
  step: TransformStep,
): FieldInfo[] {
  switch (step.type) {
    case "set": {
      const target = step.field?.trim() ?? "";
      if (target === "") return [...fields];
      return upsert([...fields], {
        path: target,
        type: valueType(step.value, fields),
        example: "",
      });
    }
    case "remove": {
      const gone = (step.fields ?? []).filter((p) => p !== "");
      return fields.filter((f) => !gone.some((p) => pathUnder(f.path, p)));
    }
    case "pick": {
      const picked = (step.fields ?? []).filter((p) => p !== "");
      if (picked.length === 0) return [...fields];
      let out: FieldInfo[] = [];
      for (const p of picked) {
        const base = step.rename?.[p] || p;
        const inside = fields.filter((f) => pathUnder(f.path, p));
        const moved = (path: string) =>
          step.flatten ? path.replaceAll(".", "_") : path;
        if (inside.length === 0) {
          out = upsert(out, {
            path: moved(base),
            type: "unknown",
            example: "",
          });
        }
        for (const f of inside) {
          out = upsert(out, {
            ...f,
            path: moved(base + f.path.slice(p.length)),
          });
        }
      }
      return out;
    }
    case "split": {
      const list = step.field?.trim() ?? "";
      if (list === "" || list === WHOLE) return [...fields];
      const rel = relativeFields(fields, list);
      const inner = rel.filter((r) => r.path !== WHOLE);
      const kept = step.keep_parent
        ? fields.filter((f) => !pathUnder(f.path, list))
        : [];
      const as = step.as?.trim() ?? "";
      if (as === "") return [...kept, ...inner];
      const whole = rel.find((r) => r.path === WHOLE);
      return [
        ...kept,
        {
          path: as,
          type: inner.length > 0 ? "object" : (whole?.type ?? "unknown"),
          example: "",
        },
        ...inner.map((r) => ({ ...r, path: `${as}.${r.path}` })),
      ];
    }
    case "aggregate": {
      const into = step.into?.trim() ?? "";
      if (into === "") return [...fields];
      const type: FieldType =
        step.op === "count" || step.op === "sum" || step.op === "avg"
          ? "number"
          : step.op === "collect" || step.op === "group_count"
            ? "array"
            : "unknown";
      return upsert([...fields], { path: into, type, example: "" });
    }
    default:
      return [...fields];
  }
}

/** `result[i]` is the field list the step at index i can pick from. */
export function fieldsBeforeEachStep(
  input: readonly FieldInfo[],
  steps: readonly TransformStep[],
): FieldInfo[][] {
  const out: FieldInfo[][] = [];
  let cur: FieldInfo[] = [...input];
  for (const step of steps) {
    out.push(cur);
    cur = fieldsAfterStep(cur, step);
  }
  return out;
}
