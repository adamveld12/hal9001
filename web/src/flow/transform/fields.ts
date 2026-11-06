// Field discovery from a sample JSON value, for the field pickers. Paths use
// the transform path syntax: dot paths (`author.login`), `[]` for "any element
// of the array" (`labels[].name`, `tags[]`), `.` for the whole value.

export type FieldType =
  "string" | "number" | "boolean" | "array" | "object" | "null" | "unknown";

export interface FieldInfo {
  path: string;
  type: FieldType;
  /** Short display text of a sample value ("open", "3", "[2 items]"). */
  example: string;
}

/** The whole value (the item itself, or the element inside a list). */
export const WHOLE = ".";

const MAX_DEPTH = 5;
const MAX_FIELDS = 300;
const MAX_ELEMENTS = 25;
const EXAMPLE_CHARS = 40;

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function typeOfValue(v: unknown): FieldType {
  if (v === null || v === undefined) return "null";
  if (Array.isArray(v)) return "array";
  switch (typeof v) {
    case "string":
      return "string";
    case "number":
      return "number";
    case "boolean":
      return "boolean";
    case "object":
      return "object";
    default:
      return "unknown";
  }
}

export function exampleOf(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "string") {
    const flat = v.replace(/\s+/g, " ").trim();
    return flat.length > EXAMPLE_CHARS
      ? `${flat.slice(0, EXAMPLE_CHARS)}…`
      : flat;
  }
  if (Array.isArray(v)) {
    return v.length === 0
      ? "[empty]"
      : `[${v.length} item${v.length === 1 ? "" : "s"}]`;
  }
  if (isObject(v)) {
    const n = Object.keys(v).length;
    return n === 0 ? "{}" : `{${n} key${n === 1 ? "" : "s"}}`;
  }
  return String(v);
}

function mergeType(a: FieldType, b: FieldType): FieldType {
  if (a === b) return a;
  if (a === "null") return b;
  if (b === "null") return a;
  return "unknown";
}

/** Keys that cannot be written as a path segment are not offered. */
function usableKey(k: string): boolean {
  return k !== "" && !/[.[\]]/.test(k);
}

function join(base: string, key: string): string {
  return base === "" ? key : `${base}.${key}`;
}

/**
 * Fields of `sample`, parents before their children, document order. Arrays of
 * objects contribute `name[].key` paths (merged over their first elements);
 * arrays of scalars contribute `name[]`. A sample that is not an object yields
 * only the whole-value entry `.`.
 */
export function discoverFields(
  sample: unknown,
  opts: { maxDepth?: number; maxFields?: number } = {},
): FieldInfo[] {
  const maxDepth = opts.maxDepth ?? MAX_DEPTH;
  const maxFields = opts.maxFields ?? MAX_FIELDS;
  const found = new Map<string, FieldInfo>();

  const record = (path: string, v: unknown) => {
    const type = typeOfValue(v);
    const known = found.get(path);
    if (!known) {
      if (found.size < maxFields) {
        found.set(path, { path, type, example: exampleOf(v) });
      }
      return;
    }
    if (known.type === "null" && type !== "null") known.example = exampleOf(v);
    known.type = mergeType(known.type, type);
  };

  const walk = (v: unknown, path: string, depth: number) => {
    if (depth >= maxDepth) return;
    if (isObject(v)) {
      for (const [key, child] of Object.entries(v)) {
        if (!usableKey(key)) continue;
        const childPath = join(path, key);
        record(childPath, child);
        walk(child, childPath, depth + 1);
      }
    } else if (Array.isArray(v)) {
      const base = `${path}[]`;
      for (const el of v.slice(0, MAX_ELEMENTS)) {
        if (isObject(el)) walk(el, base, depth + 1);
        else if (!Array.isArray(el)) record(base, el);
      }
    }
  };

  if (isObject(sample)) walk(sample, "", 0);
  else record(WHOLE, sample);
  return [...found.values()];
}

export function fieldType(
  fields: readonly FieldInfo[],
  path: string,
): FieldType {
  return fields.find((f) => f.path === path)?.type ?? "unknown";
}

/** Fields that hold a list: what sort, limit, unique, split, aggregate take. */
export function arrayFields(fields: readonly FieldInfo[]): FieldInfo[] {
  return fields.filter((f) => f.type === "array");
}

/** Fields a step may write to or remove: no `[]`, not the whole value. */
export function plainFields(fields: readonly FieldInfo[]): FieldInfo[] {
  return fields.filter((f) => f.path !== WHOLE && !f.path.includes("[]"));
}

/** Whether `path` is `prefix` or lies inside it. */
export function pathUnder(path: string, prefix: string): boolean {
  return (
    path === prefix ||
    path.startsWith(`${prefix}.`) ||
    path.startsWith(`${prefix}[]`)
  );
}

/**
 * Fields inside the elements of the list at `listPath`, with paths relative to
 * an element (`.` is the element itself), for `by`, `of`, `group_by` and
 * list-filter conditions.
 */
export function relativeFields(
  fields: readonly FieldInfo[],
  listPath: string,
): FieldInfo[] {
  if (listPath === WHOLE || listPath === "") return [];
  const prefix = `${listPath}[]`;
  const out: FieldInfo[] = [];
  for (const f of fields) {
    if (f.path === prefix) out.push({ ...f, path: WHOLE });
    else if (f.path.startsWith(`${prefix}.`)) {
      out.push({ ...f, path: f.path.slice(prefix.length + 1) });
    }
  }
  return out;
}
