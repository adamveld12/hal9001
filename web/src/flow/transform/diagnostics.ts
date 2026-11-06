// Routing server and client diagnostics to the step and form field they are
// about. Paths come in two shapes: `nodes[2].steps[1].conditions[0].value`
// (flow validation) and `steps[1].conditions[0].value` (preview, relative to
// the request body).

import type { Diagnostic, Severity } from "../model";
import type { TransformStep } from "./types";

export interface StepMessage {
  severity: Severity;
  message: string;
}

export interface StepPath {
  step: number;
  /** Path inside the step (`conditions[0].value`); empty for the step itself. */
  field: string;
}

const STEP_PATH = /^(?:nodes(?:\[\d+\]|\.[^.[]+)\.)?steps\[(\d+)\](?:\.(.*))?$/;

export function parseStepPath(path: string | undefined): StepPath | null {
  if (!path) return null;
  const m = STEP_PATH.exec(path);
  return m ? { step: Number(m[1]), field: m[2] ?? "" } : null;
}

export interface GroupedDiagnostics {
  byStep: Map<number, { field: string; msg: StepMessage }[]>;
  /** Diagnostics of the node that do not point into a step. */
  rest: Diagnostic[];
}

/** Group diagnostics by step index; duplicates (same path, severity, text) collapse. */
export function groupStepDiagnostics(
  diags: readonly Diagnostic[],
): GroupedDiagnostics {
  const byStep: GroupedDiagnostics["byStep"] = new Map();
  const rest: Diagnostic[] = [];
  const seen = new Set<string>();
  for (const d of diags) {
    const p = parseStepPath(d.path);
    if (!p) {
      rest.push(d);
      continue;
    }
    const key = `${p.step}|${p.field}|${d.severity}|${d.message}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const list = byStep.get(p.step) ?? [];
    list.push({
      field: p.field,
      msg: { severity: d.severity, message: d.message },
    });
    byStep.set(p.step, list);
  }
  return { byStep, rest };
}

function conditionPaths(prefix: string, count: number, out: string[]) {
  out.push(prefix);
  for (let i = 0; i < count; i++) {
    const p = `${prefix}[${i}]`;
    out.push(p, `${p}.field`, `${p}.op`, `${p}.value`, `${p}.case_sensitive`);
  }
}

/** Form field paths a step's editor shows messages under. */
export function renderedPaths(step: TransformStep): Set<string> {
  const out: string[] = [];
  const nConds = step.conditions?.length ?? 0;
  const nFields = step.fields?.length ?? 0;
  switch (step.type) {
    case "filter":
      out.push("match", "keep");
      conditionPaths("conditions", nConds, out);
      break;
    case "route":
      out.push("rules", "default_port");
      (step.rules ?? []).forEach((r, i) => {
        out.push(`rules[${i}]`, `rules[${i}].port`, `rules[${i}].match`);
        conditionPaths(
          `rules[${i}].conditions`,
          r.conditions?.length ?? 0,
          out,
        );
      });
      break;
    case "pick":
      out.push("fields", "rename", "flatten");
      for (let i = 0; i < nFields; i++) out.push(`fields[${i}]`);
      break;
    case "remove":
      out.push("fields");
      for (let i = 0; i < nFields; i++) out.push(`fields[${i}]`);
      break;
    case "set":
      out.push(
        "field",
        "value",
        "value.kind",
        "value.value",
        "value.field",
        "value.template",
        "value.function",
        "value.arg",
      );
      break;
    case "split":
      out.push("field", "as", "keep_parent");
      break;
    case "sort":
      out.push("field", "by", "order");
      break;
    case "limit":
      out.push("field", "count");
      break;
    case "unique":
      out.push("field", "by");
      break;
    case "aggregate":
      out.push("field", "into", "op", "of", "group_by");
      break;
    case "filter_list":
      out.push("field", "match", "keep");
      conditionPaths("conditions", nConds, out);
      break;
  }
  return new Set(out);
}

/** The deepest rendered path that is `field` or an ancestor of it; "" when none. */
export function claimingPath(rendered: ReadonlySet<string>, field: string) {
  let cur = field;
  while (cur !== "") {
    if (rendered.has(cur)) return cur;
    const cut = Math.max(cur.lastIndexOf("."), cur.lastIndexOf("["));
    cur = cut < 0 ? "" : cur.slice(0, cut);
  }
  return "";
}

export interface StepMessages {
  /** Messages shown under the form field at `path`. */
  at: (path: string) => StepMessage[];
  /** Messages with no matching field: shown at the bottom of the card. */
  card: StepMessage[];
}

const NONE: StepMessage[] = [];

export function stepMessages(
  step: TransformStep,
  items: readonly { field: string; msg: StepMessage }[] | undefined,
): StepMessages {
  const rendered = renderedPaths(step);
  const at = new Map<string, StepMessage[]>();
  const card: StepMessage[] = [];
  for (const { field, msg } of items ?? []) {
    const claim = claimingPath(rendered, field);
    if (claim === "") {
      card.push(msg);
      continue;
    }
    const list = at.get(claim) ?? [];
    list.push(msg);
    at.set(claim, list);
  }
  return { at: (path) => at.get(path) ?? NONE, card };
}

export function hasError(msgs: readonly StepMessage[]): boolean {
  return msgs.some((m) => m.severity === "error");
}
