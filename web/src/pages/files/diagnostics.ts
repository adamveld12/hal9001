import { ApiError, type Schemas } from "../../api/client";

export type Diagnostic = Schemas["Diagnostic"];
export type Severity = Schemas["Severity"];

const SEVERITY_ORDER: Record<Severity, number> = {
  error: 0,
  warning: 1,
  info: 2,
};

export interface DiagnosticGroup {
  /** 1-based line, or null for findings without a known line. */
  line: number | null;
  items: Diagnostic[];
}

export interface GroupedDiagnostics {
  groups: DiagnosticGroup[];
  counts: Record<Severity, number>;
}

/**
 * Groups findings by line (ascending), most severe first inside a group.
 * Findings without a line form the last group.
 */
export function groupDiagnostics(
  diagnostics: readonly Diagnostic[],
): GroupedDiagnostics {
  const counts: Record<Severity, number> = { error: 0, warning: 0, info: 0 };
  const byLine = new Map<number, Diagnostic[]>();
  const unlocated: Diagnostic[] = [];
  for (const d of diagnostics) {
    counts[d.severity] += 1;
    if (d.line === undefined) {
      unlocated.push(d);
    } else {
      const list = byLine.get(d.line);
      if (list) list.push(d);
      else byLine.set(d.line, [d]);
    }
  }
  const bySeverity = (a: Diagnostic, b: Diagnostic) =>
    SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity];
  const groups: DiagnosticGroup[] = [...byLine.entries()]
    .sort(([a], [b]) => a - b)
    .map(([line, items]) => ({ line, items: items.toSorted(bySeverity) }));
  if (unlocated.length > 0) {
    groups.push({ line: null, items: unlocated.toSorted(bySeverity) });
  }
  return { groups, counts };
}

export function hasErrors(diagnostics: readonly Diagnostic[]): boolean {
  return diagnostics.some((d) => d.severity === "error");
}

/** Diagnostics carried by a 422 `validation-failed` problem, else null. */
export function diagnosticsOf(error: unknown): Diagnostic[] | null {
  if (!(error instanceof ApiError) || error.kind !== "validation-failed") {
    return null;
  }
  const list = error.problem?.diagnostics;
  return Array.isArray(list) ? (list as Diagnostic[]) : [];
}

/** True for 409 `conflict` (hash mismatch); other 409s are not edit conflicts. */
export function isConflict(error: unknown): error is ApiError {
  return error instanceof ApiError && error.kind === "conflict";
}

/** `current_hash` of a conflict problem, null when absent or when the file is gone. */
export function conflictHash(error: ApiError): string | null {
  const h = error.problem?.current_hash;
  return typeof h === "string" ? h : null;
}

/** Text of a 1-based line, or null when out of range. */
export function sourceLine(text: string, line: number): string | null {
  const lines = text.split("\n");
  const l = lines[line - 1];
  return l === undefined ? null : l.replace(/\r$/, "");
}

/** Diagnostics for some text, from a validate call or a rejected save. */
export interface Checked {
  text: string;
  diagnostics: Diagnostic[];
}

export interface VisibleDiagnostics {
  diagnostics: Diagnostic[];
  /** The list describes other text than the current draft. */
  stale: boolean;
  /** `saved`: file on disk (from GET); `checked`: validate call or rejected save. */
  origin: "saved" | "checked";
}

/**
 * Chooses which diagnostics to show for the draft: a check of exactly this
 * text wins, then the server's findings for the saved file when the draft is
 * unchanged. Otherwise the latest known list is shown, marked stale.
 */
export function visibleDiagnostics(
  draft: string,
  saved: Checked,
  checked: Checked | null,
): VisibleDiagnostics {
  if (checked && checked.text === draft) {
    return {
      diagnostics: checked.diagnostics,
      stale: false,
      origin: "checked",
    };
  }
  if (draft === saved.text) {
    return { diagnostics: saved.diagnostics, stale: false, origin: "saved" };
  }
  if (checked) {
    return { diagnostics: checked.diagnostics, stale: true, origin: "checked" };
  }
  return { diagnostics: saved.diagnostics, stale: true, origin: "saved" };
}
