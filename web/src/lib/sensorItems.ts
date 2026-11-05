// Parsing and tabulating sensor output. `parseSensorStdout` follows the
// daemon's runner.ParseSensorItems: one JSON array yields its elements,
// anything else is a sequence of JSON values (JSONL, one per line, or
// concatenated). Pure; used by every view that shows sensor items.

export interface ParseError {
  /** 1-based line where the offending value starts. */
  line: number;
  message: string;
}

export interface ParsedItems {
  /** Parsed items. On an error these are the ones read before it. */
  items: unknown[];
  error: ParseError | null;
}

const WS = /\s/;
const SCALAR = /[^\s,\]}[{"]+/y;

function lineAt(text: string, offset: number): number {
  let line = 1;
  for (let i = 0; i < offset && i < text.length; i++) {
    if (text.charCodeAt(i) === 10) line++;
  }
  return line;
}

class ScanError extends Error {
  readonly offset: number;
  constructor(message: string, offset: number) {
    super(message);
    this.offset = offset;
  }
}

/** End offset (exclusive) of the JSON value starting at `start`. Validity is left to JSON.parse. */
function scanValue(text: string, start: number): number {
  const c = text[start];
  if (c === '"') return scanString(text, start);
  if (c === "{" || c === "[") {
    let depth = 0;
    for (let i = start; i < text.length; i++) {
      const ch = text[i];
      if (ch === '"') {
        i = scanString(text, i) - 1;
      } else if (ch === "{" || ch === "[") {
        depth++;
      } else if (ch === "}" || ch === "]") {
        depth--;
        if (depth === 0) return i + 1;
      }
    }
    throw new ScanError("unexpected end of input", start);
  }
  SCALAR.lastIndex = start;
  const m = SCALAR.exec(text);
  if (!m)
    throw new ScanError(`unexpected character ${JSON.stringify(c)}`, start);
  return start + m[0].length;
}

function scanString(text: string, start: number): number {
  for (let i = start + 1; i < text.length; i++) {
    const ch = text[i];
    if (ch === "\\") i++;
    else if (ch === '"') return i + 1;
  }
  throw new ScanError("unterminated string", start);
}

function skipSpace(text: string, i: number): number {
  while (i < text.length && WS.test(text[i]!)) i++;
  return i;
}

function parseAt(text: string, start: number): { value: unknown; end: number } {
  const end = scanValue(text, start);
  try {
    return { value: JSON.parse(text.slice(start, end)) as unknown, end };
  } catch (e) {
    throw new ScanError(e instanceof Error ? e.message : String(e), start);
  }
}

/** Elements of an array that may be cut off mid-way, up to the first broken one. */
function salvageArray(text: string, open: number): unknown[] {
  const out: unknown[] = [];
  let i = open + 1;
  try {
    for (;;) {
      i = skipSpace(text, i);
      if (text[i] === ",") i = skipSpace(text, i + 1);
      if (i >= text.length || text[i] === "]") break;
      const r = parseAt(text, i);
      out.push(r.value);
      i = r.end;
    }
  } catch {
    // keep what was read
  }
  return out;
}

/** Items of a sensor's stdout. Empty output is zero items. */
export function parseSensorStdout(text: string): ParsedItems {
  const values: unknown[] = [];
  let pos = skipSpace(text, 0);
  const firstStart = pos;
  while (pos < text.length) {
    try {
      const r = parseAt(text, pos);
      values.push(r.value);
      pos = skipSpace(text, r.end);
    } catch (e) {
      const offset = e instanceof ScanError ? e.offset : pos;
      const message = e instanceof Error ? e.message : String(e);
      const items =
        values.length === 0 && text[firstStart] === "["
          ? salvageArray(text, firstStart)
          : values;
      return {
        items,
        error: { line: lineAt(text, offset), message },
      };
    }
  }
  if (values.length === 1 && Array.isArray(values[0])) {
    return { items: values[0] as unknown[], error: null };
  }
  return { items: values, error: null };
}

// ---------------------------------------------------------------- flattening

/** Objects nested deeper than this many levels are shown as `{n keys}`. */
export const MAX_DEPTH = 3;

/** Column of items that are not objects. */
export const VALUE_COLUMN = "(value)";

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Dot-path -> value, with nested objects flattened up to `maxDepth` levels. */
export function flattenItem(
  item: unknown,
  maxDepth = MAX_DEPTH,
): Map<string, unknown> {
  const out = new Map<string, unknown>();
  if (!isRecord(item)) {
    out.set(VALUE_COLUMN, item);
    return out;
  }
  const walk = (
    obj: Record<string, unknown>,
    prefix: string,
    depth: number,
  ) => {
    for (const [k, v] of Object.entries(obj)) {
      const path = prefix === "" ? k : `${prefix}.${k}`;
      if (isRecord(v) && depth < maxDepth && Object.keys(v).length > 0) {
        walk(v, path, depth + 1);
      } else {
        out.set(path, v);
      }
    }
  };
  walk(item, "", 1);
  return out;
}

export interface CellText {
  /** Shown in the cell, not truncated. */
  text: string;
  /** Hover text with the whole value. */
  full: string;
  kind: "null" | "bool" | "number" | "string" | "array" | "object";
}

function preview(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === null || typeof v !== "object") return String(v);
  return Array.isArray(v) ? `[${v.length}]` : "{…}";
}

/** Compact text of one value: arrays as a count and preview, objects as a key count. */
export function describeCell(v: unknown): CellText {
  if (v === null || v === undefined) {
    return { text: "null", full: "null", kind: "null" };
  }
  if (typeof v === "string") return { text: v, full: v, kind: "string" };
  if (typeof v === "number") {
    return { text: String(v), full: String(v), kind: "number" };
  }
  if (typeof v === "boolean") {
    return { text: String(v), full: String(v), kind: "bool" };
  }
  const full = JSON.stringify(v, null, 2);
  if (Array.isArray(v)) {
    if (v.length === 0) return { text: "[]", full, kind: "array" };
    const shown = v.slice(0, 3).map(preview).join(", ");
    return {
      text: `[${v.length}] ${shown}${v.length > 3 ? ", …" : ""}`,
      full,
      kind: "array",
    };
  }
  const n = Object.keys(v as object).length;
  return {
    text: n === 0 ? "{}" : `{${n} key${n === 1 ? "" : "s"}}`,
    full,
    kind: "object",
  };
}

// ---------------------------------------------------------------- columns

export interface Column {
  path: string;
  /** Number of items that have this column. */
  count: number;
}

const TOP_KEYS = [
  "id",
  "key",
  "dedup_key",
  "uid",
  "uuid",
  "number",
  "name",
  "title",
];
const LEAF_KEYS = ["id", "key", "number", "name", "title"];

/** Lower sorts first: identifying fields before the rest. */
export function keyRank(path: string): number {
  const top = TOP_KEYS.indexOf(path);
  if (top >= 0) return top;
  const leaf = path.slice(path.lastIndexOf(".") + 1);
  return path.includes(".") && LEAF_KEYS.includes(leaf)
    ? TOP_KEYS.length
    : TOP_KEYS.length + 1;
}

/** Union of the rows' columns: identifying fields first, then by how many rows have them, then first seen. */
export function buildColumns(rows: readonly Map<string, unknown>[]): Column[] {
  const counts = new Map<string, number>();
  for (const row of rows) {
    for (const path of row.keys())
      counts.set(path, (counts.get(path) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([path, count], order) => ({ path, count, order }))
    .sort(
      (a, b) =>
        keyRank(a.path) - keyRank(b.path) ||
        b.count - a.count ||
        a.order - b.order,
    )
    .map(({ path, count }) => ({ path, count }));
}

/** Columns shown at first: the first `limit` in order, so wide data starts readable. */
export function defaultVisible(
  columns: readonly Column[],
  limit = 10,
): Set<string> {
  return new Set(columns.slice(0, limit).map((c) => c.path));
}

// ---------------------------------------------------------------- rows

export interface ItemRow {
  /** 0-based position in the output. */
  index: number;
  item: unknown;
  /** Dedup key computed by the daemon; undefined when the source has none. */
  dedupKey?: string | null;
  keyError?: string | null;
  flat: Map<string, unknown>;
}

export interface RawItem {
  item: unknown;
  dedupKey?: string | null;
  keyError?: string | null;
}

export function toRows(items: readonly RawItem[]): ItemRow[] {
  return items.map((it, index) => ({
    index,
    item: it.item,
    dedupKey: it.dedupKey,
    keyError: it.keyError,
    flat: flattenItem(it.item),
  }));
}

/** Rows with any cell, dedup key or key error containing `query` (case-insensitive). */
export function filterRows(rows: readonly ItemRow[], query: string): ItemRow[] {
  const q = query.trim().toLowerCase();
  if (q === "") return [...rows];
  return rows.filter((r) => {
    if (r.dedupKey?.toLowerCase().includes(q)) return true;
    if (r.keyError?.toLowerCase().includes(q)) return true;
    for (const v of r.flat.values()) {
      if (describeCell(v).full.toLowerCase().includes(q)) return true;
    }
    return false;
  });
}
