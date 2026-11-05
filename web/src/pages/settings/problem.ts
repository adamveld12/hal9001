import { ApiError } from "../../api/client";

/** Messages of a 422 `validation-failed` problem's diagnostics, with their path when present. */
export function diagnosticMessages(e: unknown): string[] {
  if (!(e instanceof ApiError) || !e.problem) return [];
  const d: unknown = e.problem.diagnostics;
  if (!Array.isArray(d)) return [];
  const out: string[] = [];
  for (const item of d as unknown[]) {
    if (typeof item !== "object" || item === null) continue;
    if (!("message" in item) || typeof item.message !== "string") continue;
    const path =
      "path" in item && typeof item.path === "string" ? item.path : "";
    out.push(path ? `${path}: ${item.message}` : item.message);
  }
  return out;
}

/** True for a 409 `conflict`: `expected_hash` no longer matches the file. */
export function isConflict(e: unknown): boolean {
  return e instanceof ApiError && e.kind === "conflict";
}
