import { describe, expect, it } from "vitest";
import type { AssistTestResult } from "../../api/assist";
import { summarizeTest } from "./testSummary";

const blob = { text: "", size: 0, truncated: false, blob: null };

function result(over: Partial<AssistTestResult>): AssistTestResult {
  return {
    exit_code: 0,
    duration_ms: 10,
    timed_out: false,
    items: [],
    item_count: 0,
    parse_error: null,
    stdout: blob,
    stderr: blob,
    ...over,
  };
}

describe("summarizeTest", () => {
  it("counts key errors and repeated dedup keys among the listed items", () => {
    const s = summarizeTest(
      result({
        item_count: 4,
        items: [
          { dedup_key: "a", key_error: null, item: {} },
          { dedup_key: "a", key_error: null, item: {} },
          { dedup_key: null, key_error: "no such field", item: {} },
          { dedup_key: "b", key_error: null, item: {} },
        ],
      }),
    );
    expect(s).toMatchObject({
      listed: 4,
      total: 4,
      keyErrors: 1,
      duplicateKeys: 1,
    });
  });

  it("reports more items than listed when the daemon capped the list", () => {
    const s = summarizeTest(
      result({
        item_count: 350,
        items: [{ dedup_key: "a", key_error: null, item: 1 }],
      }),
    );
    expect(s.listed).toBe(1);
    expect(s.total).toBe(350);
  });

  it("ranks a timeout above a bad exit code above a parse error", () => {
    expect(
      summarizeTest(result({ timed_out: true, exit_code: -1 })).outcome,
    ).toBe("timed_out");
    expect(
      summarizeTest(result({ exit_code: 2, parse_error: "x" })).outcome,
    ).toBe("failed");
    expect(summarizeTest(result({ parse_error: "x" })).outcome).toBe(
      "parse_error",
    );
    expect(summarizeTest(result({})).outcome).toBe("ok");
  });
});
