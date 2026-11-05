/**
 * Reads what a tool call is about to do from its raw input, for display.
 * Agents describe edits differently (`old_string`/`new_string`, `oldText`/
 * `newText`, a `diff` string, `edits` lists, whole-file `content`), so the
 * extractor recognises the common shapes and returns nothing for the rest,
 * which the UI then shows as raw JSON.
 */

import type { ToolContent } from "../runs/transcript";

export type DiffKind = "add" | "del" | "ctx" | "hunk" | "skip";

export interface DiffLine {
  kind: DiffKind;
  text: string;
}

export interface FileDiff {
  path: string | null;
  lines: DiffLine[];
}

const OLD_KEYS = ["old_string", "oldString", "old_text", "oldText", "old"];
const NEW_KEYS = ["new_string", "newString", "new_text", "newText", "new"];
const PATH_KEYS = ["file_path", "filePath", "path", "file"];
const UNIFIED_KEYS = ["diff", "patch"];

/** Past this many old x new lines the middle of a change is shown as removed then added. */
const MAX_LCS_CELLS = 250_000;

function splitLines(text: string): string[] {
  if (text === "") return [];
  const lines = text.split("\n");
  if (lines.at(-1) === "") lines.pop();
  return lines;
}

/** Line diff of two texts: common prefix and suffix are trimmed, the rest uses an LCS table. */
export function lineDiff(oldText: string, newText: string): DiffLine[] {
  const a = splitLines(oldText);
  const b = splitLines(newText);
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  const out: DiffLine[] = a
    .slice(0, start)
    .map((text) => ({ kind: "ctx", text }));
  out.push(...middleDiff(a.slice(start, endA), b.slice(start, endB)));
  for (const text of a.slice(endA)) out.push({ kind: "ctx", text });
  return out;
}

function middleDiff(a: string[], b: string[]): DiffLine[] {
  if (a.length === 0) return b.map((text) => ({ kind: "add", text }));
  if (b.length === 0) return a.map((text) => ({ kind: "del", text }));
  if (a.length * b.length > MAX_LCS_CELLS) {
    return [
      ...a.map((text): DiffLine => ({ kind: "del", text })),
      ...b.map((text): DiffLine => ({ kind: "add", text })),
    ];
  }
  // lcs[i][j]: length of the longest common subsequence of a[i..] and b[j..].
  const w = b.length + 1;
  const lcs = new Uint32Array((a.length + 1) * w);
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i * w + j] =
        a[i] === b[j]
          ? lcs[(i + 1) * w + j + 1]! + 1
          : Math.max(lcs[(i + 1) * w + j]!, lcs[i * w + j + 1]!);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "ctx", text: a[i]! });
      i++;
      j++;
    } else if (lcs[(i + 1) * w + j]! >= lcs[i * w + j + 1]!) {
      out.push({ kind: "del", text: a[i++]! });
    } else {
      out.push({ kind: "add", text: b[j++]! });
    }
  }
  while (i < a.length) out.push({ kind: "del", text: a[i++]! });
  while (j < b.length) out.push({ kind: "add", text: b[j++]! });
  return out;
}

/**
 * Replace long runs of unchanged lines with a single `skip` line, keeping
 * `keep` lines of context around each change. All-unchanged input is kept whole.
 */
export function collapseContext(lines: DiffLine[], keep = 3): DiffLine[] {
  const changed = lines.map((l) => l.kind === "add" || l.kind === "del");
  if (!changed.includes(true)) return lines;
  const near = lines.map((_, i) => {
    for (let d = -keep; d <= keep; d++) if (changed[i + d]) return true;
    return false;
  });
  const out: DiffLine[] = [];
  let hidden = 0;
  const flush = () => {
    if (hidden > 0) {
      out.push({
        kind: "skip",
        text: `${hidden} unchanged line${hidden === 1 ? "" : "s"}`,
      });
      hidden = 0;
    }
  };
  lines.forEach((l, i) => {
    if (l.kind !== "ctx" || near[i]) {
      flush();
      out.push(l);
    } else hidden++;
  });
  flush();
  return out;
}

/** Classify the lines of a unified diff or patch by their leading character. */
export function parseUnified(text: string): DiffLine[] {
  return splitLines(text).map((line): DiffLine => {
    if (line.startsWith("@@")) return { kind: "hunk", text: line };
    if (
      line.startsWith("+++") ||
      line.startsWith("---") ||
      line.startsWith("diff ") ||
      line.startsWith("index ")
    ) {
      return { kind: "hunk", text: line };
    }
    if (line.startsWith("+")) return { kind: "add", text: line.slice(1) };
    if (line.startsWith("-")) return { kind: "del", text: line.slice(1) };
    return {
      kind: "ctx",
      text: line.startsWith(" ") ? line.slice(1) : line,
    };
  });
}

type Rec = Record<string, unknown>;

function isRec(v: unknown): v is Rec {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function firstString(o: Rec, keys: string[]): string | null {
  for (const k of keys) {
    const v = o[k];
    if (typeof v === "string") return v;
  }
  return null;
}

/** A key that is present with a string or null value (null = no previous text). */
function hasText(o: Rec, keys: string[]): boolean {
  return keys.some((k) => typeof o[k] === "string" || o[k] === null);
}

function fromRecord(o: Rec, inheritedPath: string | null): FileDiff[] {
  const path = firstString(o, PATH_KEYS) ?? inheritedPath;
  const unified = firstString(o, UNIFIED_KEYS);
  if (unified !== null) return [{ path, lines: parseUnified(unified) }];

  if (hasText(o, OLD_KEYS) || hasText(o, NEW_KEYS)) {
    return [
      {
        path,
        lines: lineDiff(
          firstString(o, OLD_KEYS) ?? "",
          firstString(o, NEW_KEYS) ?? "",
        ),
      },
    ];
  }
  if (Array.isArray(o.edits)) {
    return o.edits.flatMap((e) => (isRec(e) ? fromRecord(e, path) : []));
  }
  if (Array.isArray(o.content)) {
    return o.content.flatMap((c) => (isRec(c) ? fromRecord(c, path) : []));
  }
  // A whole-file write: a path with string content and nothing to compare against.
  if (path !== null && typeof o.content === "string") {
    return [{ path, lines: lineDiff("", o.content) }];
  }
  return [];
}

/** Every file change described by a tool call's raw input; empty when it describes none. */
export function extractDiffs(raw: unknown): FileDiff[] {
  if (Array.isArray(raw)) return raw.flatMap(extractDiffs);
  return isRec(raw) ? fromRecord(raw, null) : [];
}

/** The shell command of an `execute` call, if the input carries one. */
export function extractCommand(raw: unknown): string | null {
  if (!isRec(raw)) return null;
  const c = raw.command ?? raw.cmd;
  if (typeof c === "string") return c;
  if (Array.isArray(c) && c.every((p) => typeof p === "string")) {
    return c.join(" ");
  }
  return null;
}

/**
 * File changes carried by an ACP tool call's `content` (type `diff`). Codex
 * sends edit permission requests with no raw input at all, so this is the only
 * place the change is described.
 */
export function diffsFromToolContent(
  content: readonly ToolContent[],
): FileDiff[] {
  const out: FileDiff[] = [];
  for (const c of content) {
    if (c.type !== "diff") continue;
    out.push({
      path: c.path === "" ? null : c.path,
      lines: lineDiff(c.oldText ?? "", c.newText),
    });
  }
  return out;
}
