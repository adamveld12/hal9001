import type { Schemas } from "./client";
import type { QueryRoot } from "./keys";

export type StreamType = Schemas["StreamEventType"];
type ControlType = "stream.ready" | "stream.resync";
export type DataStreamType = Exclude<StreamType, ControlType>;

/**
 * Query families to refetch when a stream event arrives. `status.changed`
 * carries the new header counters, so it is applied directly (see stream.tsx)
 * and refetches nothing.
 */
const ROOTS: Record<DataStreamType, readonly QueryRoot[]> = {
  "status.changed": [],
  "killswitch.changed": ["killswitch", "status", "sensors"],
  "sensor.changed": ["sensors", "sensor"],
  "config.changed": [
    "sensors",
    "sensor",
    "sensor-env",
    "flows",
    "flow",
    "prompts",
    "prompt",
    "runtimes",
    "settings",
    "env",
  ],
  "sensor_run.created": ["sensors", "sensor", "sensor-runs"],
  "sensor_run.updated": ["sensors", "sensor", "sensor-runs"],
  "event.created": ["events", "sensors"],
  "flow_run.created": ["flow-runs", "flow-run", "event", "flows"],
  "flow_run.updated": ["flow-runs", "flow-run", "event", "flows"],
  "node_run.created": ["flow-run", "node-run"],
  "node_run.updated": ["flow-run", "node-run"],
  "agent_run.created": ["agent-runs", "agent-run", "node-run"],
  "agent_run.updated": ["agent-runs", "agent-run", "node-run"],
  "agent_event.appended": ["agent-events"],
  "permission_request.created": ["permission-requests"],
  "permission_request.updated": ["permission-requests"],
  "approval.created": ["approvals", "approval"],
  "approval.updated": ["approvals", "approval"],
  "inbox.created": ["inbox", "inbox-item"],
  "inbox.updated": ["inbox", "inbox-item"],
  "auth_event.created": ["auth-events", "auth-sessions"],
};

export function rootsFor(type: DataStreamType): readonly QueryRoot[] {
  return ROOTS[type];
}

/** Every event name the stream can send. */
export const STREAM_TYPES: readonly StreamType[] = [
  "stream.ready",
  "stream.resync",
  ...(Object.keys(ROOTS) as DataStreamType[]),
];

/** Union of the roots touched by a batch of event types. */
export function rootsForBatch(types: Iterable<DataStreamType>): Set<QueryRoot> {
  const out = new Set<QueryRoot>();
  for (const t of types) for (const r of rootsFor(t)) out.add(r);
  return out;
}

/**
 * Whether a stream event carries nothing the screen needs: a successful
 * routine request decision (`request`, `whoami`). The server records one for
 * every API call, including the refetch an earlier one triggered, so reacting
 * to them would make the Auth settings refetch itself in a loop. Logins,
 * logouts, revocations, passkey changes and every failure still refetch.
 */
export function isRoutineAuthEvent(event: {
  type: string;
  data?: unknown;
}): boolean {
  if (event.type !== "auth_event.created") return false;
  const d = event.data as { action?: unknown; outcome?: unknown } | null;
  return (
    d != null &&
    (d.action === "request" || d.action === "whoami") &&
    d.outcome === "success"
  );
}
