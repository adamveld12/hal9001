// Instant client-side checks for transform steps: required fields and the
// operator/value rules of the contract. They give feedback while a form is half
// filled in; the server's diagnostics stay authoritative (it compiles the steps).
// Paths are relative to the node: `steps[1].conditions[0].value`.

import type { Diagnostic } from "../model";
import { WHOLE } from "./fields";
import { isTextOperator, valueNeed } from "./operators";
import type { TransformCondition, TransformStep } from "./types";

type Out = Diagnostic[];

function err(out: Out, path: string, code: string, message: string) {
  out.push({ severity: "error", code, path, message });
}

const isScalar = (v: unknown) =>
  typeof v === "string" || typeof v === "number" || typeof v === "boolean";

function validateCondition(c: TransformCondition, p: string, out: Out) {
  if (c.field.trim() === "")
    err(out, `${p}.field`, "required", "Choose a field");
  const need = valueNeed(c.op);
  const v = c.value;
  if (need === "none") {
    if (v !== undefined) {
      err(out, `${p}.value`, "field_not_allowed", "This test takes no value");
    }
  } else if (v === undefined || (Array.isArray(v) && v.length === 0)) {
    err(
      out,
      `${p}.value`,
      "required",
      need === "list" ? "Add at least one value" : "Enter a value",
    );
  } else if (need === "list") {
    if (!Array.isArray(v) || !v.every(isScalar)) {
      err(out, `${p}.value`, "invalid_value", "Use a list of text or numbers");
    }
  } else if (need === "text") {
    if (typeof v !== "string") {
      err(out, `${p}.value`, "invalid_value", "This test needs text");
    }
  } else if (need === "compare") {
    if (typeof v !== "number" && typeof v !== "string") {
      err(out, `${p}.value`, "invalid_value", "Use a number or a date");
    }
  } else if (!isScalar(v)) {
    err(out, `${p}.value`, "invalid_value", "Use text, a number or true/false");
  }
  if (c.case_sensitive === true && !isTextOperator(c.op)) {
    err(
      out,
      `${p}.case_sensitive`,
      "field_not_allowed",
      "Matching case only applies to text tests",
    );
  }
}

function validateConditions(
  conditions: readonly TransformCondition[] | undefined,
  p: string,
  out: Out,
) {
  if (!conditions || conditions.length === 0) {
    err(out, `${p}`, "required", "Add at least one condition");
    return;
  }
  conditions.forEach((c, i) => validateCondition(c, `${p}[${i}]`, out));
}

function validatePaths(
  fields: readonly string[] | undefined,
  p: string,
  out: Out,
  what: string,
) {
  if (!fields || fields.length === 0) {
    err(out, p, "required", `Choose at least one field to ${what}`);
    return;
  }
  fields.forEach((f, i) => {
    if (f.trim() === "") err(out, `${p}[${i}]`, "required", "Choose a field");
    else if (f === WHOLE || f.includes("[]")) {
      err(
        out,
        `${p}[${i}]`,
        "invalid_path",
        `Pick a single field, not a whole item or a list element, to ${what}`,
      );
    }
  });
}

function requireField(s: TransformStep, p: string, out: Out, message: string) {
  if (!s.field || s.field.trim() === "")
    err(out, `${p}.field`, "required", message);
}

function validateSet(s: TransformStep, p: string, out: Out) {
  if (!s.field || s.field.trim() === "") {
    err(out, `${p}.field`, "required", "Choose the field to write");
  } else if (s.field === WHOLE || s.field.includes("[]")) {
    err(
      out,
      `${p}.field`,
      "invalid_path",
      "Write to a single field, not a list element",
    );
  }
  const v = s.value;
  if (!v) {
    err(out, `${p}.value`, "required", "Choose a value");
    return;
  }
  const vp = `${p}.value`;
  switch (v.kind) {
    case "constant":
      if (v.value === undefined)
        err(out, `${vp}.value`, "required", "Enter a value");
      break;
    case "field":
      if (!v.field || v.field.trim() === "") {
        err(out, `${vp}.field`, "required", "Choose the field to copy");
      }
      break;
    case "template":
      if (!v.template) err(out, `${vp}.template`, "required", "Write the text");
      break;
    case "function":
      if (!v.field || v.field.trim() === "") {
        err(out, `${vp}.field`, "required", "Choose a field");
      }
      if (!v.function) {
        err(out, `${vp}.function`, "required", "Choose a calculation");
      } else if (v.function === "join" && typeof v.arg !== "string") {
        err(
          out,
          `${vp}.arg`,
          "required",
          "Enter the text to put between items",
        );
      } else if (
        v.function === "split" &&
        (typeof v.arg !== "string" || v.arg === "")
      ) {
        err(out, `${vp}.arg`, "required", "Enter the text to split at");
      } else if (v.function === "default" && v.arg === undefined) {
        err(out, `${vp}.arg`, "required", "Enter the fallback value");
      }
      break;
  }
}

export function validateStep(
  s: TransformStep,
  index: number,
  all: readonly TransformStep[],
): Diagnostic[] {
  const out: Out = [];
  const p = `steps[${index}]`;
  switch (s.type) {
    case "filter":
      validateConditions(s.conditions, `${p}.conditions`, out);
      break;
    case "route": {
      const rules = s.rules ?? [];
      if (rules.length === 0)
        err(out, `${p}.rules`, "required", "Add at least one rule");
      rules.forEach((r, i) => {
        const rp = `${p}.rules[${i}]`;
        if (r.port.trim() === "")
          err(out, `${rp}.port`, "required", "Name this output");
        else if (r.port === "error") {
          err(
            out,
            `${rp}.port`,
            "reserved",
            "“error” is a built-in output; use another name",
          );
        }
        validateConditions(r.conditions, `${rp}.conditions`, out);
      });
      if (s.default_port === "error") {
        err(
          out,
          `${p}.default_port`,
          "reserved",
          "“error” is a built-in output; use another name",
        );
      }
      const firstRoute = all.findIndex((x) => x.type === "route");
      if (firstRoute !== index) {
        err(out, p, "route_position", "Only one route step is allowed");
      } else if (index !== all.length - 1) {
        err(out, p, "route_position", "A route step must be the last step");
      }
      break;
    }
    case "pick":
      validatePaths(s.fields, `${p}.fields`, out, "keep");
      break;
    case "remove":
      validatePaths(s.fields, `${p}.fields`, out, "remove");
      break;
    case "set":
      validateSet(s, p, out);
      break;
    case "split":
      requireField(s, p, out, "Choose the list to split");
      break;
    case "sort":
      requireField(s, p, out, "Choose the list to sort");
      break;
    case "unique":
      requireField(s, p, out, "Choose the list");
      break;
    case "limit":
      requireField(s, p, out, "Choose the list to shorten");
      if (s.count === undefined) {
        err(out, `${p}.count`, "required", "Enter how many to keep");
      } else if (!Number.isInteger(s.count) || s.count < 0) {
        err(
          out,
          `${p}.count`,
          "invalid_value",
          "Use a whole number, 0 or more",
        );
      }
      break;
    case "aggregate":
      if (!s.field || s.field.trim() === "") {
        err(out, `${p}.field`, "required", "Choose the list to summarize");
      } else if (s.field === WHOLE) {
        err(out, `${p}.field`, "invalid_path", "Choose a list inside the item");
      }
      if (!s.into || s.into.trim() === "") {
        err(out, `${p}.into`, "required", "Choose where to put the result");
      } else if (s.into.includes("[]") || s.into === WHOLE) {
        err(out, `${p}.into`, "invalid_path", "Write to a single field");
      }
      if (!s.op) err(out, `${p}.op`, "required", "Choose how to summarize");
      else if (s.op === "group_count" && (!s.group_by || s.group_by === "")) {
        err(out, `${p}.group_by`, "required", "Choose what to group by");
      }
      break;
    case "filter_list":
      requireField(s, p, out, "Choose the list to filter");
      validateConditions(s.conditions, `${p}.conditions`, out);
      break;
  }
  return out;
}

/** Diagnostics for every step, paths relative to the node. */
export function validateSteps(
  steps: readonly TransformStep[] | undefined,
): Diagnostic[] {
  const list = steps ?? [];
  return list.flatMap((s, i) => validateStep(s, i, list));
}
