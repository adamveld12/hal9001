import type { EventsQuery } from "../../api/events";
import type { Schemas } from "../../api/client";

export const RUN_STATUSES = [
  "queued",
  "running",
  "awaiting_approval",
  "succeeded",
  "failed",
  "killed",
  "interrupted",
  "cancelled",
] as const satisfies readonly Schemas["FlowRunStatus"][];

export const DECISIONS = [
  "emitted",
  "dropped",
  "error",
  "skipped",
] as const satisfies readonly Schemas["NodeRunDecision"][];

export const KINDS = [
  "live",
  "replay",
  "test",
] as const satisfies readonly Schemas["FlowRunKind"][];

/** Timeline filters as held in the URL. Empty string = no filter. */
export interface TimelineFilters {
  sensor: string;
  flow: string;
  status: "" | Schemas["FlowRunStatus"];
  decision: "" | Schemas["NodeRunDecision"];
  /** Empty = the server default (live and replay; test runs hidden). */
  kind: "" | Schemas["FlowRunKind"];
  q: string;
}

export const NO_FILTERS: TimelineFilters = {
  sensor: "",
  flow: "",
  status: "",
  decision: "",
  kind: "",
  q: "",
};

function oneOf<T extends string>(
  value: string | null,
  allowed: readonly T[],
): "" | T {
  return allowed.find((a) => a === value) ?? "";
}

export function filtersFromParams(params: URLSearchParams): TimelineFilters {
  return {
    sensor: params.get("sensor") ?? "",
    flow: params.get("flow") ?? "",
    status: oneOf(params.get("status"), RUN_STATUSES),
    decision: oneOf(params.get("decision"), DECISIONS),
    kind: oneOf(params.get("kind"), KINDS),
    q: params.get("q") ?? "",
  };
}

/** URL params for the filters; unset filters are omitted so the default URL stays clean. */
export function filtersToParams(f: TimelineFilters): URLSearchParams {
  const p = new URLSearchParams();
  for (const key of Object.keys(NO_FILTERS) as (keyof TimelineFilters)[]) {
    if (f[key] !== "") p.set(key, f[key]);
  }
  return p;
}

/** Query for GET /api/events. Every filter is applied by the server. */
export function filtersToQuery(f: TimelineFilters): EventsQuery {
  const q: EventsQuery = {};
  if (f.sensor) q.sensor_id = f.sensor;
  if (f.flow) q.flow_id = f.flow;
  if (f.status) q.status = f.status;
  if (f.decision) q.decision = f.decision;
  if (f.kind) q.kind = f.kind;
  const text = f.q.trim();
  if (text) q.q = text;
  return q;
}

export function hasActiveFilters(f: TimelineFilters): boolean {
  return Object.values(f).some((v) => v !== "");
}
