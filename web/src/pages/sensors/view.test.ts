import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "../../api/client";
import type { SensorRun, SensorSummary } from "../../api/sensors";
import {
  isFailedRun,
  pauseErrorMessage,
  pollErrorMessage,
  resetErrorMessage,
  runFlowErrorMessage,
  scheduleLabel,
  sensorRow,
  sensorStateTone,
  unscheduledLabel,
} from "./view";

const blob = { text: "", size: 0, truncated: false, blob: null };

function run(over: Partial<SensorRun>): SensorRun {
  return {
    id: "01JC7Z3K9Q8V5T2M4N6P8R0S1T",
    sensor_id: "s",
    trigger: "scheduled",
    status: "succeeded",
    baseline: false,
    started_at: "2026-11-05T10:00:00Z",
    ended_at: "2026-11-05T10:00:02Z",
    duration_ms: 2000,
    exit_code: 0,
    item_count: 3,
    new_count: 1,
    stdout: blob,
    stderr: blob,
    error: null,
    ...over,
  };
}

function summary(over: Partial<SensorSummary>): SensorSummary {
  return {
    id: "s",
    path: "sensors/s.yaml",
    hash: "a".repeat(64),
    sensor: { id: "s", lang: "bash", interval: "2m", script: "true" },
    diagnostics: [],
    status: {
      state: "idle",
      paused: false,
      paused_at: null,
      paused_by: null,
      last_run: run({}),
      last_success_at: "2026-11-05T10:00:02Z",
      next_poll_at: "2026-11-05T10:02:00Z",
      new_items_24h: 7,
      manual: false,
      reset_at: null,
      reset_by: null,
    },
    ...over,
  };
}

describe("unscheduledLabel", () => {
  it("explains a running-state sensor that has no next poll", () => {
    expect(unscheduledLabel("idle", null)).toBe("not scheduled");
    expect(unscheduledLabel("failing", null)).toBe("not scheduled");
  });
  it("stays quiet when there is a time, or the state already says why", () => {
    expect(unscheduledLabel("idle", "2026-11-05T10:02:00Z")).toBeNull();
    expect(unscheduledLabel("paused", null)).toBeNull();
    expect(unscheduledLabel("invalid", null)).toBeNull();
  });
});

describe("sensorRow", () => {
  it("reports a healthy sensor without problems", () => {
    expect(sensorRow(summary({}))).toMatchObject({
      state: "idle",
      schedule: "every 2m",
      manual: false,
      newItems24h: 7,
      lastPollAt: "2026-11-05T10:00:00Z",
      problems: [],
    });
  });

  it("lists the failure class of a failed last poll and validation errors", () => {
    const row = sensorRow(
      summary({
        diagnostics: [
          { severity: "error", code: "required", message: "m" },
          { severity: "error", code: "required", message: "m" },
          { severity: "warning", code: "x", message: "w" },
        ],
        status: {
          state: "failing",
          paused: false,
          paused_at: null,
          paused_by: null,
          last_run: run({ status: "failed", error: "exit_nonzero" }),
          last_success_at: null,
          next_poll_at: null,
          new_items_24h: 0,
          manual: false,
          reset_at: null,
          reset_by: null,
        },
      }),
    );
    expect(row.problems).toEqual(["exit_nonzero", "2 validation errors"]);
  });

  it("handles a sensor that never polled and a file that does not parse", () => {
    const row = sensorRow(
      summary({
        sensor: null,
        status: {
          state: "invalid",
          paused: false,
          paused_at: null,
          paused_by: null,
          last_run: null,
          last_success_at: null,
          next_poll_at: null,
          new_items_24h: 0,
          manual: false,
          reset_at: null,
          reset_by: null,
        },
      }),
    );
    expect(row.lastPollAt).toBeNull();
    expect(row.schedule).toBe("–");
    expect(sensorStateTone(row.state)).toBe("bad");
  });
});

describe("paused sensors", () => {
  const paused = summary({
    status: {
      state: "paused",
      paused: true,
      paused_at: "2026-11-05T10:00:00Z",
      paused_by: "adam",
      last_run: run({}),
      last_success_at: "2026-11-05T10:00:02Z",
      next_poll_at: null,
      new_items_24h: 0,
      manual: false,
      reset_at: null,
      reset_by: null,
    },
  });

  it("marks a sensor paused on its own and says who paused it and that Run now still works", () => {
    const row = sensorRow(paused);
    expect(row.paused).toBe(true);
    expect(row.pausedTitle).toMatch(/Paused by adam on /);
    expect(row.pausedTitle).toMatch(/Run now still works/);
  });

  it("does not call a sensor held by the kill switch paused (resume would do nothing)", () => {
    const held = summary({
      status: {
        ...paused.status,
        paused: false,
        paused_at: null,
        paused_by: null,
      },
    });
    const row = sensorRow(held);
    expect(row.state).toBe("paused");
    expect(row.paused).toBe(false);
    expect(row.pausedTitle).toBeNull();
  });
});

describe("pauseErrorMessage", () => {
  it("names the action that failed", () => {
    expect(pauseErrorMessage(new Error("boom"), false)).toMatch(
      /pause the sensor/,
    );
    expect(pauseErrorMessage(new Error("boom"), true)).toMatch(
      /resume the sensor/,
    );
  });
});

describe("scheduleLabel", () => {
  it("shows cron when there is no interval", () => {
    expect(scheduleLabel({ id: "s", lang: "bash", cron: "0 * * * *" })).toBe(
      "cron 0 * * * *",
    );
  });
});

describe("manual sensors", () => {
  it("labels a sensor with neither interval nor cron as manual", () => {
    expect(scheduleLabel({ id: "s", lang: "bash" })).toBe("manual");
    expect(scheduleLabel({ id: "s", lang: "bash", interval: "2m" })).toBe(
      "every 2m",
    );
  });
});

describe("isFailedRun", () => {
  it("is true only for finished unsuccessful polls", () => {
    expect(isFailedRun({ status: "timeout" })).toBe(true);
    expect(isFailedRun({ status: "interrupted" })).toBe(true);
    expect(isFailedRun({ status: "running" })).toBe(false);
    expect(isFailedRun({ status: "succeeded" })).toBe(false);
  });
});

describe("pollErrorMessage", () => {
  const problem = (status: number, type: string, extra: object = {}) =>
    new ApiError(
      status,
      {
        type: `urn:hal9001:problem:${type}`,
        title: type,
        status,
        ...extra,
      } as Problem,
      "failed",
    );

  it("explains the kill switch", () => {
    expect(pollErrorMessage(problem(423, "paused"))).toMatch(/kill switch/);
    expect(
      pollErrorMessage(
        problem(423, "paused", { paused_since: "2026-11-05T10:00:00Z" }),
      ),
    ).toMatch(/since/);
  });

  it("explains a poll already running", () => {
    expect(pollErrorMessage(problem(409, "invalid-state"))).toMatch(
      /already running/,
    );
  });

  it("falls back to the problem detail", () => {
    expect(
      pollErrorMessage(problem(500, "internal", { detail: "disk full" })),
    ).toBe("disk full");
  });

  it("explains a flow without sensors and a reset during a poll", () => {
    expect(runFlowErrorMessage(problem(422, "validation-failed"))).toMatch(
      /subscribes to no sensors/,
    );
    expect(runFlowErrorMessage(problem(423, "paused"))).toMatch(/kill switch/);
    expect(resetErrorMessage(problem(409, "invalid-state"))).toMatch(
      /poll of this sensor is running/,
    );
    expect(resetErrorMessage(problem(423, "paused"))).toMatch(/kill switch/);
  });
});
