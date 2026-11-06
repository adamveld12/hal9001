import { describe, expect, it } from "vitest";
import {
  RESET_ACTIONS,
  flowRerunKey,
  flowRunNowState,
  loadRerunSeen,
  resetMessage,
  runSummary,
  saveRerunSeen,
} from "./runNow";

describe("runSummary", () => {
  it("counts new and re-run events and the flow runs they started", () => {
    expect(
      runSummary({
        new_events: 3,
        rerun_events: 2,
        flow_run_ids: [..."abcde"],
      }),
    ).toBe("3 new, 2 re-run → 5 flow runs");
  });
  it("uses the singular for one flow run", () => {
    expect(
      runSummary({ new_events: 1, rerun_events: 0, flow_run_ids: ["a"] }),
    ).toBe("1 new, 0 re-run → 1 flow run");
  });
  it("says so when nothing started", () => {
    expect(
      runSummary({ new_events: 0, rerun_events: 0, flow_run_ids: [] }),
    ).toBe("0 new, 0 re-run → no flow runs");
  });
});

describe("flowRunNowState", () => {
  const ok = {
    dirty: false,
    subscribeCount: 1,
    enabled: true,
    killSwitch: false,
  };
  it("is enabled for a saved, enabled, subscribed flow", () => {
    expect(flowRunNowState(ok)).toEqual({ disabled: false });
  });
  it("asks to save first when there are unsaved changes", () => {
    expect(flowRunNowState({ ...ok, dirty: true })).toEqual({
      disabled: true,
      reason: "Save the flow first: Run now runs the saved version",
    });
  });
  it("is disabled when the flow subscribes to nothing", () => {
    expect(flowRunNowState({ ...ok, subscribeCount: 0 })).toEqual({
      disabled: true,
      reason: "This flow subscribes to no sensors",
    });
  });
  it("still runs a disabled flow, with a note", () => {
    expect(flowRunNowState({ ...ok, enabled: false })).toEqual({
      disabled: false,
      note: "This flow is disabled; Run now still runs it once.",
    });
  });
  it("is disabled by the kill switch before anything else", () => {
    expect(flowRunNowState({ ...ok, dirty: true, killSwitch: true })).toEqual({
      disabled: true,
      reason: "The kill switch is engaged",
    });
  });
});

function fakeStorage(): Pick<Storage, "getItem" | "setItem"> & {
  data: Map<string, string>;
} {
  const data = new Map<string, string>();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => void data.set(k, v),
  };
}

describe("remembered re-run option", () => {
  it("defaults to off and remembers each id separately", () => {
    const s = fakeStorage();
    expect(loadRerunSeen("gh", s)).toBe(false);
    saveRerunSeen("gh", true, s);
    expect(loadRerunSeen("gh", s)).toBe(true);
    expect(loadRerunSeen("other", s)).toBe(false);
    expect(loadRerunSeen(flowRerunKey("triage"), s)).toBe(false);
    saveRerunSeen(flowRerunKey("triage"), true, s);
    expect(s.data.get("hal9001.runNow.rerunSeen.flow:triage")).toBe("1");
    saveRerunSeen("gh", false, s);
    expect(loadRerunSeen("gh", s)).toBe(false);
  });
  it("tolerates missing or blocked storage", () => {
    const blocked = {
      getItem: () => {
        throw new Error("blocked");
      },
      setItem: () => {
        throw new Error("blocked");
      },
    };
    expect(loadRerunSeen("gh", null)).toBe(false);
    expect(loadRerunSeen("gh", blocked)).toBe(false);
    expect(() => saveRerunSeen("gh", true, null)).not.toThrow();
    expect(() => saveRerunSeen("gh", true, blocked)).not.toThrow();
  });
});

describe("reset dialog", () => {
  it("sends run:false for Reset and run:true for Reset and run now", () => {
    expect(RESET_ACTIONS.reset).toEqual({
      label: "Reset",
      body: { run: false },
    });
    expect(RESET_ACTIONS["reset-and-run"]).toEqual({
      label: "Reset and run now",
      body: { run: true },
    });
  });
  it("reports the number of cleared keys", () => {
    expect(resetMessage(12)).toBe("Reset: 12 dedup keys cleared");
    expect(resetMessage(1)).toBe("Reset: 1 dedup key cleared");
  });
});
