// Pure helpers of the Tools editor: the YAML a form produces, the params <->
// JSON Schema conversion of the raw-schema toggle, and the args form of the
// test runner. The server stays the authority on validity.

import type { Tool, ToolParam } from "../../api/tools";
import { yamlBlock, yamlScalar } from "../files/yaml";

export const PARAM_TYPES = [
  "string",
  "number",
  "integer",
  "boolean",
  "array",
  "object",
] as const;
export type ParamType = (typeof PARAM_TYPES)[number];

export const TOOL_LANGS = ["bash", "python", "node", "exec"] as const;

export const NEW_TOOL_YAML = `# Describe the tool to the AI assistant, or write it here.
id: my_tool
description: What the tool does and when an agent should call it.
params:
  input: {"type":"string","description":"What the tool works on.","required":true}
lang: bash
script: |
  echo "$ARG_INPUT"
approve: always
`;

/** A tool with every list present, as the form edits it. */
export function emptyTool(): Tool {
  return { id: "", description: "", lang: "bash", script: "" };
}

const json = (v: unknown) => JSON.stringify(v);

/** `text` as a block scalar when it has several lines, else a quoted/plain scalar. */
function textField(key: string, text: string): string[] {
  return text.includes("\n")
    ? yamlBlock(key, text)
    : [`${key}: ${yamlScalar(text)}`];
}

/** The tool as YAML. Nested values are written as JSON, which is valid YAML flow syntax. */
export function toolToYaml(t: Tool): string {
  const out: string[] = [];
  out.push(`id: ${yamlScalar(t.id)}`);
  out.push(...textField("description", t.description));
  const names = Object.keys(t.params ?? {}).sort();
  if (t.schema) {
    out.push(`schema: ${json(t.schema)}`);
  } else if (names.length > 0) {
    out.push("params:");
    for (const n of names) {
      out.push(`  ${yamlScalar(n)}: ${json(cleanParam(t.params![n]))}`);
    }
  }
  out.push(`lang: ${t.lang}`);
  if (t.lang === "exec") {
    out.push(`command: ${json(t.command ?? [])}`);
  } else {
    out.push(...yamlBlock("script", t.script ?? ""));
  }
  if (t.approve !== undefined) {
    out.push(
      typeof t.approve === "string"
        ? `approve: ${t.approve}`
        : `approve: ${json(t.approve)}`,
    );
  }
  if (t.timeout) out.push(`timeout: ${yamlScalar(t.timeout)}`);
  if (t.runs_in_sandbox) out.push("runs_in_sandbox: true");
  return out.join("\n") + "\n";
}

function cleanParam(p: ToolParam): ToolParam {
  const o: ToolParam = { type: p.type };
  if (p.description) o.description = p.description;
  if (p.required) o.required = true;
  if (p.enum && p.enum.length > 0) o.enum = p.enum;
  if (p.default !== undefined && p.default !== null) o.default = p.default;
  return o;
}

type Schema = Record<string, unknown>;

/** The JSON Schema the server builds from params. */
export function paramsToSchema(params: Record<string, ToolParam>): Schema {
  const properties: Record<string, Schema> = {};
  const required: string[] = [];
  for (const name of Object.keys(params).sort()) {
    const p = cleanParam(params[name]);
    const { required: req, ...rest } = p;
    properties[name] = rest as Schema;
    if (req) required.push(name);
  }
  const s: Schema = { type: "object", properties, additionalProperties: false };
  if (required.length > 0) s.required = required;
  return s;
}

/**
 * Params equivalent to a JSON Schema, or null when the schema says more than
 * params can (nested shapes, other keywords): converting would lose meaning.
 */
export function schemaToParams(
  schema: Schema,
): Record<string, ToolParam> | null {
  const allowedTop = new Set([
    "type",
    "properties",
    "required",
    "additionalProperties",
  ]);
  if (schema.type !== "object") return null;
  if (Object.keys(schema).some((k) => !allowedTop.has(k))) return null;
  if (schema.additionalProperties === true) return null;
  const props = (schema.properties ?? {}) as Record<string, Schema>;
  const required = new Set((schema.required as string[] | undefined) ?? []);
  const allowedProp = new Set(["type", "description", "enum", "default"]);
  const out: Record<string, ToolParam> = {};
  for (const [name, p] of Object.entries(props)) {
    if (typeof p !== "object" || p === null) return null;
    if (Object.keys(p).some((k) => !allowedProp.has(k))) return null;
    if (!PARAM_TYPES.includes(p.type as ParamType)) return null;
    const param: ToolParam = { type: p.type as ParamType };
    if (typeof p.description === "string") param.description = p.description;
    if (Array.isArray(p.enum)) param.enum = p.enum;
    if (p.default !== undefined) param.default = p.default;
    if (required.has(name)) param.required = true;
    out[name] = param;
  }
  for (const r of required) if (!(r in props)) return null;
  return out;
}

// ---- the args form of the test runner ----

export interface ArgField {
  name: string;
  type: ParamType | "json";
  description: string;
  required: boolean;
  enum: unknown[];
  default: unknown;
}

/** One form field per top-level property of an object JSON Schema. */
export function argFields(schema: Schema | null | undefined): ArgField[] {
  const props = (schema?.properties ?? {}) as Record<string, Schema>;
  const required = new Set((schema?.required as string[] | undefined) ?? []);
  return Object.keys(props).map((name) => {
    const p = props[name] ?? {};
    const type = PARAM_TYPES.includes(p.type as ParamType)
      ? (p.type as ParamType)
      : "json";
    return {
      name,
      type,
      description: typeof p.description === "string" ? p.description : "",
      required: required.has(name),
      enum: Array.isArray(p.enum) ? p.enum : [],
      default: p.default,
    };
  });
}

/** The text a field starts with: its default as the user would type it. */
export function initialValue(f: ArgField): string {
  if (f.default === undefined || f.default === null) return "";
  return typeof f.default === "object" ? json(f.default) : String(f.default);
}

export interface BuiltArgs {
  args: Record<string, unknown>;
  /** Field name to message. */
  errors: Record<string, string>;
}

/**
 * Typed arguments from the text of the form. An empty optional field is left
 * out (the tool's default applies); an empty required field is an error.
 */
export function buildArgs(
  fields: readonly ArgField[],
  values: Record<string, string>,
): BuiltArgs {
  const args: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  for (const f of fields) {
    const raw = values[f.name] ?? "";
    if (raw === "" && f.type !== "boolean") {
      if (f.required && (f.default === undefined || f.default === null)) {
        errors[f.name] = "required";
      }
      continue;
    }
    switch (f.type) {
      case "string":
        args[f.name] = raw;
        break;
      case "boolean":
        if (raw === "") break;
        args[f.name] = raw === "true";
        break;
      case "number":
      case "integer": {
        const n = Number(raw);
        if (!Number.isFinite(n)) errors[f.name] = "must be a number";
        else if (f.type === "integer" && !Number.isInteger(n))
          errors[f.name] = "must be a whole number";
        else args[f.name] = n;
        break;
      }
      default:
        try {
          const v: unknown = JSON.parse(raw);
          if (f.type === "array" && !Array.isArray(v))
            errors[f.name] = "must be a JSON array";
          else if (
            f.type === "object" &&
            (typeof v !== "object" || v === null || Array.isArray(v))
          )
            errors[f.name] = "must be a JSON object";
          else args[f.name] = v;
        } catch {
          errors[f.name] = "must be valid JSON";
        }
    }
  }
  return { args, errors };
}
