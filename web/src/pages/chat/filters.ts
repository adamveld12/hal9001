// Filter state of the Chat page's list. It lives in the URL query
// (`source, flow, runtime, status, since, until, q`), so a view can be linked
// and the assistant can open one (internal/assist/uiroutes.go).

import type {
  ConversationQuery,
  ConversationSource,
  ConversationStatus,
} from "../../api/conversations";

export interface ChatFilters {
  /** Empty: both sources. */
  source: "" | ConversationSource;
  flow: string;
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

/** The `GET /api/conversations` query. `until` is the day after, because the API's bound is exclusive. */
export function filtersToQuery(f: ChatFilters): ConversationQuery {
  const q: ConversationQuery = {};
  if (f.source) q.source = f.source;
  if (f.flow) q.flow = f.flow;
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
