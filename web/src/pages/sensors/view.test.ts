import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "../../api/client";
import type { SensorRun, SensorSummary } from "../../api/sensors";
import {
  isFailedRun,
  pollErrorMessage,
  scheduleLabel,
  sensorRow,
  sensorStateTone,
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
      last_run: run({}),
      last_success_at: "2026-11-05T10:00:02Z",
      next_poll_at: "2026-11-05T10:02:00Z",
      new_items_24h: 7,
    },
    ...over,
  };
}

describe("sensorRow", () => {
  it("reports a healthy sensor without problems", () => {
    expect(sensorRow(summary({}))).toMatchObject({
      state: "idle",
      schedule: "every 2m",
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
          last_run: run({ status: "failed", error: "exit_nonzero" }),
          last_success_at: null,
          next_poll_at: null,
          new_items_24h: 0,
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
          last_run: null,
          last_success_at: null,
          next_poll_at: null,
          new_items_24h: 0,
        },
      }),
    );
    expect(row.lastPollAt).toBeNull();
    expect(row.schedule).toBe("–");
    expect(sensorStateTone(row.state)).toBe("bad");
  });
});

describe("scheduleLabel", () => {
  it("shows cron when there is no interval", () => {
    expect(scheduleLabel({ id: "s", lang: "bash", cron: "0 * * * *" })).toBe(
      "cron 0 * * * *",
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
});
