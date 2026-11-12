// Filter state of the Chat page's list. It lives in the URL query
// (`source, flow, flow_run, runtime, status, since, until, q`), so a view can be linked
// and the assistant can open one (internal/assist/uiroutes.go).

import { shortId } from "../../lib/format";
import type {
  Conversation,
  ConversationQuery,
  ConversationSource,
  ConversationStatus,
} from "../../api/conversations";

export interface ChatFilters {
  /** Empty: both sources. */
  source: "" | ConversationSource;
  flow: string;
  /**
   * A flow run id: only that run's agent conversations, the chats forked from
   * them and the agent conversations of its continuation runs. Set by the
   * Inbox's "Agent chats" link; it has no control in the filter bar, but a
   * banner above the list.
   */
  flow_run: string;
  runtime: string;
  status: "" | ConversationStatus;
  /** A day (YYYY-MM-DD) or empty; activity on or after the start of that day. */
  since: string;
  /** A day (YYYY-MM-DD) or empty; activity up to the end of that day. */
  until: string;
  q: string;
}

export const NO_CHAT_FILTERS: ChatFilters = {
  source: "",
  flow: "",
  flow_run: "",
  runtime: "",
  status: "",
  since: "",
  until: "",
  q: "",
};

export const CHAT_FILTER_KEYS = Object.keys(
  NO_CHAT_FILTERS,
) as (keyof ChatFilters)[];

export const SOURCES = [
  { value: "", label: "All" },
  { value: "flow", label: "Flow runs" },
  { value: "chat", label: "Chats" },
] as const satisfies readonly {
  value: ChatFilters["source"];
  label: string;
}[];

export const STATUSES = [
  "running",
  "awaiting_approval",
  "idle",
  "succeeded",
  "failed",
  "killed",
  "denied",
  "interrupted",
  "timeout",
  "closed",
] as const satisfies readonly ConversationStatus[];

const MAX_Q = 200;

const ULID = /^[0-9A-HJKMNP-TV-Z]{26}$/;

/** Typing in the search box changes the URL (and the request) after this pause. */
export const Q_DEBOUNCE_MS = 300;
const DAY = /^(\d{4})-(\d{2})-(\d{2})$/;

/** Local midnight at the start of `day` (YYYY-MM-DD), or null when it is not a real date. */
function dayStart(day: string, plusDays = 0): Date | null {
  const m = DAY.exec(day);
  if (!m) return null;
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  const date = new Date(y, mo - 1, d + plusDays);
  const real = new Date(y, mo - 1, d);
  return real.getFullYear() === y &&
    real.getMonth() === mo - 1 &&
    real.getDate() === d
    ? date
    : null;
}

function validDay(raw: string | null): string {
  return raw !== null && dayStart(raw) !== null ? raw : "";
}

function validUlid(raw: string | null): string {
  const id = raw?.trim() ?? "";
  return ULID.test(id) ? id : "";
}

function oneOf<T extends string>(
  value: string | null,
  allowed: readonly T[],
): "" | T {
  return allowed.find((a) => a === value) ?? "";
}

/** The filters in a URL query; anything unknown or malformed counts as unset. */
export function filtersFromParams(p: URLSearchParams): ChatFilters {
  return {
    source: oneOf(p.get("source"), ["flow", "chat"]),
    flow: p.get("flow")?.trim() ?? "",
    flow_run: validUlid(p.get("flow_run")),
    runtime: p.get("runtime")?.trim() ?? "",
    status: oneOf(p.get("status"), STATUSES),
    since: validDay(p.get("since")),
    until: validDay(p.get("until")),
    q: (p.get("q") ?? "").slice(0, MAX_Q),
  };
}

/** `base` with the filter keys replaced by `f` (empty ones removed); other keys stay. */
export function filtersToParams(
  f: ChatFilters,
  base: URLSearchParams = new URLSearchParams(),
): URLSearchParams {
  const p = new URLSearchParams(base);
  for (const k of CHAT_FILTER_KEYS) {
    if (f[k] === "") p.delete(k);
    else p.set(k, f[k]);
  }
  return p;
}

export function hasActiveFilters(f: ChatFilters): boolean {
  return CHAT_FILTER_KEYS.some((k) => f[k] !== "");
}

/** Whether a filter of the filter bar is set; the run filter has its own banner and is not one of them. */
export function hasBarFilters(f: ChatFilters): boolean {
  return CHAT_FILTER_KEYS.some((k) => k !== "flow_run" && f[k] !== "");
}

/** `f` without the filter bar's filters; the run filter stays. */
export function clearBarFilters(f: ChatFilters): ChatFilters {
  return { ...NO_CHAT_FILTERS, flow_run: f.flow_run };
}

export interface RunBanner {
  /** `Run 6P8R0S1T of triage`, or `Run 6P8R0S1T` while the flow is not known. */
  text: string;
  /** The run's page. */
  runPath: string;
}

/** The banner above a list filtered by a flow run; null when no run filters. `flowId` is the run's flow, once known. */
export function runBanner(
  flowRun: string,
  flowId: string | null | undefined,
): RunBanner | null {
  if (flowRun === "") return null;
  const run = `Run ${shortId(flowRun)}`;
  return {
    text: flowId ? `${run} of ${flowId}` : run,
    runPath: `/runs/${flowRun}`,
  };
}

/**
 * How a conversation of a run-filtered list relates to the run: "fork" for a
 * chat forked from one of the run's agents, "continuation" for an agent
 * conversation in a continuation run started from it, null for the run's own
 * agent conversations.
 */
export function runRelation(
  c: Pick<Conversation, "flow_run_id" | "forked_from">,
  flowRun: string,
): "fork" | "continuation" | null {
  if (flowRun === "") return null;
  if (c.forked_from) return "fork";
  return c.flow_run_id !== null && c.flow_run_id !== flowRun
    ? "continuation"
    : null;
}

/** The `GET /api/conversations` query. `until` is the day after, because the API's bound is exclusive. */
export function filtersToQuery(f: ChatFilters): ConversationQuery {
  const q: ConversationQuery = {};
  if (f.source) q.source = f.source;
  if (f.flow) q.flow = f.flow;
  if (f.flow_run) q.flow_run = f.flow_run;
  if (f.runtime) q.runtime = f.runtime;
  if (f.status) q.status = f.status;
  const since = dayStart(f.since);
  if (since) q.since = since.toISOString();
  const until = dayStart(f.until, 1);
  if (until) q.until = until.toISOString();
  const text = f.q.trim();
  if (text) q.q = text;
  return q;
}
