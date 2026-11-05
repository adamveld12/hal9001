import { describe, expect, it } from "vitest";
import {
  isRoutineAuthEvent,
  rootsFor,
  rootsForBatch,
  STREAM_TYPES,
} from "./invalidate";
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

  it("refreshes the sensor list and the sensor page when a sensor is paused or resumed", () => {
    expect(rootsFor("sensor.changed")).toEqual(["sensors", "sensor"]);
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

describe("isRoutineAuthEvent", () => {
  const ev = (action: string, outcome: string) => ({
    type: "auth_event.created",
    data: { action, outcome },
  });

  it("ignores successful per-request decisions, which would refetch themselves forever", () => {
    expect(isRoutineAuthEvent(ev("request", "success"))).toBe(true);
    expect(isRoutineAuthEvent(ev("whoami", "success"))).toBe(true);
  });

  it("keeps refreshing for logins, revocations and any failure", () => {
    expect(isRoutineAuthEvent(ev("login_continue", "success"))).toBe(false);
    expect(isRoutineAuthEvent(ev("session_revoke", "success"))).toBe(false);
    expect(isRoutineAuthEvent(ev("request", "fail"))).toBe(false);
  });

  it("is false for other stream events", () => {
    expect(isRoutineAuthEvent({ type: "inbox.created", data: {} })).toBe(false);
  });
});
