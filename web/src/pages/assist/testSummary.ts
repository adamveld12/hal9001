import type { AssistTestResult } from "../../api/assist";

export interface TestSummary {
  /** Items listed in the result (the daemon lists at most the first 200). */
  listed: number;
  total: number;
  /** Items whose dedup key template failed. */
  keyErrors: number;
  /** Items that share a dedup key with an earlier listed item: they would be one event, not several. */
  duplicateKeys: number;
  /** Overall verdict for the pill. */
  outcome: "ok" | "failed" | "timed_out" | "parse_error";
}

export function summarizeTest(r: AssistTestResult): TestSummary {
  const seen = new Set<string>();
  let duplicateKeys = 0;
  let keyErrors = 0;
  for (const it of r.items) {
    if (it.key_error) keyErrors++;
    if (it.dedup_key !== null) {
      if (seen.has(it.dedup_key)) duplicateKeys++;
      seen.add(it.dedup_key);
    }
  }
  let outcome: TestSummary["outcome"] = "ok";
  if (r.timed_out) outcome = "timed_out";
  else if (r.exit_code !== 0) outcome = "failed";
  else if (r.parse_error) outcome = "parse_error";
  return {
    listed: r.items.length,
    total: r.item_count,
    keyErrors,
    duplicateKeys,
    outcome,
  };
}
