import type { SensorRun, SensorSummary } from "../../api/sensors";
import { ApiError, errorMessage } from "../../api/client";
import type { Tone } from "../../lib/status";

export type SensorState = SensorSummary["status"]["state"];

export function sensorStateTone(state: SensorState): Tone {
  switch (state) {
    case "idle":
      return "ok";
    case "running":
      return "run";
    case "paused":
      return "warn";
    case "failing":
    case "invalid":
      return "bad";
  }
}

/** A finished poll that did not succeed. */
export function isFailedRun(run: Pick<SensorRun, "status">): boolean {
  switch (run.status) {
    case "failed":
    case "timeout":
    case "killed":
    case "interrupted":
      return true;
    case "running":
    case "succeeded":
      return false;
  }
}

/** "every 2m", "cron <expression>", or "–" when the file does not parse. */
export function scheduleLabel(sensor: SensorSummary["sensor"]): string {
  if (!sensor) return "–";
  if (sensor.interval) return `every ${sensor.interval}`;
  if (sensor.cron) return `cron ${sensor.cron}`;
  return "–";
}

/**
 * Why a sensor has no next poll ("not scheduled": no enabled flow subscribes
 * to it, so only "Poll now" runs it). Text for a sensor with no next poll that is not paused or invalid: the engine
 * only schedules sensors an enabled flow subscribes to. Null when a time (or
 * the state itself) already explains the empty cell.
 */
export function unscheduledLabel(
  state: SensorState,
  nextPollAt: string | null,
): string | null {
  if (nextPollAt !== null) return null;
  return state === "paused" || state === "invalid" ? null : "not scheduled";
}

export const UNSCHEDULED_TITLE =
  "No enabled flow subscribes to this sensor, so it is not polled on a schedule. Poll now still works.";

/**
 * Who paused the sensor and when, for a tooltip. Null when the sensor is not
 * paused individually (a sensor held by the kill switch is not "paused" here).
 */
export function pausedTitle(status: SensorSummary["status"]): string | null {
  if (!status.paused) return null;
  const by = status.paused_by ? ` by ${status.paused_by}` : "";
  const at = status.paused_at
    ? ` on ${new Date(status.paused_at).toLocaleString()}`
    : "";
  return `Paused${by}${at}. Scheduled polls are off; Poll now still works.`;
}

export interface SensorRow {
  id: string;
  state: SensorState;
  /** This sensor is paused on its own (not only by the kill switch). */
  paused: boolean;
  pausedTitle: string | null;
  schedule: string;
  lastPollAt: string | null;
  newItems24h: number;
  nextPollAt: string | null;
  /** Short reasons the sensor is unhealthy: last poll failure class, validation errors. */
  problems: string[];
}

export function sensorRow(s: SensorSummary): SensorRow {
  const problems: string[] = [];
  const last = s.status.last_run;
  if (last && isFailedRun(last)) problems.push(last.error ?? last.status);
  const errors = s.diagnostics.filter((d) => d.severity === "error").length;
  if (errors > 0) {
    problems.push(`${errors} validation error${errors > 1 ? "s" : ""}`);
  }
  return {
    id: s.id,
    state: s.status.state,
    paused: s.status.paused,
    pausedTitle: pausedTitle(s.status),
    schedule: scheduleLabel(s.sensor),
    lastPollAt: last?.started_at ?? null,
    newItems24h: s.status.new_items_24h,
    nextPollAt: s.status.next_poll_at,
    problems,
  };
}

/** Message for a failed pause or resume request. */
export function pauseErrorMessage(e: unknown, resuming: boolean): string {
  return `Could not ${resuming ? "resume" : "pause"} the sensor: ${errorMessage(e)}`;
}

/** Message for a failed "Poll now" request. */
export function pollErrorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 423) {
      const since = e.problem?.paused_since;
      return typeof since === "string"
        ? `Paused by the kill switch since ${new Date(since).toLocaleString()}. Resume to poll.`
        : "Paused by the kill switch. Resume to poll.";
    }
    if (e.kind === "invalid-state") {
      return "A poll of this sensor is already running.";
    }
  }
  return errorMessage(e);
}
