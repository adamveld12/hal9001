import type { Schemas } from "../../api/client";
import { KINDS, RUN_STATUSES } from "../timeline/filters";

export { KINDS, RUN_STATUSES };

export const AGENT_RUN_STATUSES = [
  "queued",
  "running",
  "awaiting_approval",
  "succeeded",
  "failed",
  "denied",
  "killed",
  "interrupted",
  "timeout",
] as const satisfies readonly Schemas["AgentRunStatus"][];

type Kind = Schemas["FlowRunKind"];
type FlowRunStatus = Schemas["FlowRunStatus"];
type AgentRunStatus = Schemas["AgentRunStatus"];

function oneOf<T extends string>(
  value: string | null,
  allowed: readonly T[],
): "" | T {
  return allowed.find((a) => a === value) ?? "";
}

/** Filters of the flow-run list. Empty string = no filter (kind: server default, live and replay). */
export interface FlowRunFilters {
  flow: string;
  status: "" | FlowRunStatus;
  kind: "" | Kind;
}

export const NO_FLOW_RUN_FILTERS: FlowRunFilters = {
  flow: "",
  status: "",
  kind: "",
};

export function flowRunFiltersFromParams(p: URLSearchParams): FlowRunFilters {
  return {
    flow: p.get("flow") ?? "",
    status: oneOf(p.get("status"), RUN_STATUSES),
    kind: oneOf(p.get("kind"), KINDS),
  };
}

/** Query for GET /api/flow-runs; `flowId` pins the flow (a flow's own Runs tab) over the filter. */
export function flowRunFiltersToQuery(
  f: FlowRunFilters,
  flowId?: string,
): {
  flow_id?: string;
  status?: FlowRunStatus;
  kind?: Kind;
} {
  const q: { flow_id?: string; status?: FlowRunStatus; kind?: Kind } = {};
  const flow = flowId ?? f.flow;
  if (flow) q.flow_id = flow;
  if (f.status) q.status = f.status;
  if (f.kind) q.kind = f.kind;
  return q;
}

/** Filters of the agent-run list. */
export interface AgentRunFilters {
  status: "" | AgentRunStatus;
  runtime: string;
}

export const NO_AGENT_RUN_FILTERS: AgentRunFilters = {
  status: "",
  runtime: "",
};

export function agentRunFiltersFromParams(p: URLSearchParams): AgentRunFilters {
  return {
    status: oneOf(p.get("status"), AGENT_RUN_STATUSES),
    runtime: p.get("runtime") ?? "",
  };
}

export function agentRunFiltersToQuery(f: AgentRunFilters): {
  status?: AgentRunStatus;
  runtime?: string;
} {
  const q: { status?: AgentRunStatus; runtime?: string } = {};
  if (f.status) q.status = f.status;
  if (f.runtime) q.runtime = f.runtime;
  return q;
}

/** URL params for a filter object; empty filters are omitted. Other params in `base` are kept. */
export function filtersToParams(
  f: object,
  base: URLSearchParams = new URLSearchParams(),
): URLSearchParams {
  const p = new URLSearchParams(base);
  for (const [k, v] of Object.entries(f) as [string, string][]) {
    if (v === "") p.delete(k);
    else p.set(k, v);
  }
  return p;
}

export function hasActive(f: object): boolean {
  return Object.values(f).some((v) => v !== "");
}

/** What an agent run belongs to: a flow node, an assist session, or nothing known. */
export type AgentRunOwner =
  | { kind: "flow"; flowId: string; flowRunId: string | null }
  | { kind: "assist"; sessionId: string }
  | { kind: "none" };

export function agentRunOwner(r: {
  flow_id: string | null;
  flow_run_id: string | null;
  assist_session_id: string | null;
}): AgentRunOwner {
  if (r.flow_id) {
    return { kind: "flow", flowId: r.flow_id, flowRunId: r.flow_run_id };
  }
  if (r.assist_session_id) {
    return { kind: "assist", sessionId: r.assist_session_id };
  }
  return { kind: "none" };
}
