import { describe, expect, it } from "vitest";
import { rootsFor, rootsForBatch, STREAM_TYPES } from "./invalidate";
import { QUERY_ROOTS } from "./keys";

describe("stream invalidation", () => {
  it("only names known query roots", () => {
    for (const t of STREAM_TYPES) {
      if (t === "stream.ready" || t === "stream.resync") continue;
      for (const r of rootsFor(t)) expect(QUERY_ROOTS).toContain(r);
    }
  });

  it("refreshes the run family when a node run changes, and the approval queue when one is decided", () => {
    expect(rootsFor("node_run.updated")).toContain("flow-run");
    expect(rootsFor("approval.updated")).toContain("approvals");
  });

  it("does not refetch for status.changed because the payload is applied directly", () => {
    expect(rootsFor("status.changed")).toEqual([]);
  });

  it("merges a burst into one set of families", () => {
    const roots = rootsForBatch([
      "flow_run.updated",
      "flow_run.updated",
      "inbox.created",
    ]);
    expect([...roots].sort()).toEqual(
      ["event", "flow-run", "flow-runs", "flows", "inbox", "inbox-item"].sort(),
    );
  });
});
