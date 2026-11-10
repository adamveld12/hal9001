// Query-key convention: the first element of every react-query key is one of
// QUERY_ROOTS, so a stream event can invalidate a whole family (invalidate.ts).
// Build keys with qk(root, ...params).

export const QUERY_ROOTS = [
  "status",
  "killswitch",
  "sensors",
  "sensor",
  "sensor-runs",
  "sensor-env",
  "tools",
  "tool",
  "tool-env",
  "flows",
  "flow",
  "flow-versions",
  "flow-version",
  "flow-runs",
  "queue",
  "flow-run",
  "node-run",
  "events",
  "event",
  "agent-runs",
  "agent-run",
  "agent-events",
  "permission-requests",
  "approvals",
  "approval",
  "inbox",
  "inbox-item",
  "prompts",
  "prompt",
  "runtimes",
  "settings",
  "update",
  "env",
  "sandboxes",
  "sandbox",
  "sandbox-env",
  "whoami",
  "auth-sessions",
  "auth-events",
  "passkeys",
  "tailscale",
  "flow-samples",
  "transform-preview",
  "blob",
  "assist-sessions",
  "assist-session",
  "assist-test",
] as const;

export type QueryRoot = (typeof QUERY_ROOTS)[number];

export function qk(root: QueryRoot, ...params: unknown[]): readonly unknown[] {
  return [root, ...params];
}
