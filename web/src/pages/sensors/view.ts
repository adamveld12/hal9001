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

export interface SensorRow {
  id: string;
  state: SensorState;
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
    schedule: scheduleLabel(s.sensor),
    lastPollAt: last?.started_at ?? null,
    newItems24h: s.status.new_items_24h,
    nextPollAt: s.status.next_poll_at,
    problems,
  };
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
