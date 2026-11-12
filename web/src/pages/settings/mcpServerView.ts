import type { Schemas } from "../../api/client";
import type { Tone } from "../../lib/status";

type Status = Schemas["RemoteMcpStatus"];
type Server = Schemas["RemoteMcpServer"];
type Tool = Schemas["RemoteMcpTool"];
type Changes = Schemas["RemoteMcpToolChanges"];

export const STATUS_LABEL: Record<Status, string> = {
  connected: "Connected",
  needs_reconnect: "Needs reconnect",
  disconnected: "Not connected",
  error: "Error",
  unknown: "Not tested",
};

export const STATUS_TONE: Record<Status, Tone> = {
  connected: "ok",
  needs_reconnect: "warn",
  disconnected: "muted",
  error: "bad",
  unknown: "muted",
};

/** What the Connect button on a card says, or null when the server has nothing to connect. */
export function connectLabel(
  s: Pick<Server, "auth" | "status">,
): string | null {
  if (s.auth !== "oauth" && s.auth !== "oauth_client") return null;
  if (s.status === "needs_reconnect" || s.status === "error") {
    return "Reconnect";
  }
  if (s.status === "disconnected") return "Connect";
  return null;
}

/** Whether the card shows the last error text. */
export function showsError(s: Pick<Server, "status" | "error">): boolean {
  return !!s.error && (s.status === "error" || s.status === "needs_reconnect");
}

/** "2 added, 1 changed, 1 removed", or "No changes". */
export function changesSummary(c: Changes): string {
  const parts = [
    [c.added.length, "added"],
    [c.changed.length, "changed"],
    [c.removed.length, "removed"],
  ] as const;
  const out = parts.filter(([n]) => n > 0).map(([n, w]) => `${n} ${w}`);
  return out.length > 0 ? out.join(", ") : "No changes";
}

export function noChanges(c: Changes): boolean {
  return c.added.length + c.changed.length + c.removed.length === 0;
}

/** Tools whose name, title or description contains `query` (case-insensitive). */
export function filterTools(tools: readonly Tool[], query: string): Tool[] {
  const q = query.trim().toLowerCase();
  if (q === "") return [...tools];
  return tools.filter((t) =>
    `${t.name} ${t.title ?? ""} ${t.description}`.toLowerCase().includes(q),
  );
}

/** More tools than this get a filter box. */
export const FILTER_THRESHOLD = 8;

export function toolsLabel(n: number): string {
  return `${n} ${n === 1 ? "tool" : "tools"}`;
}

/** Pretty-printed JSON for a schema. */
export function schemaText(schema: unknown): string {
  return JSON.stringify(schema, null, 2);
}

/** `name: Connected, other: Error`: names and statuses only, never secrets. */
export function serversSummary(
  servers: readonly Pick<Server, "name" | "status">[],
): string {
  return servers.map((s) => `${s.name}: ${STATUS_LABEL[s.status]}`).join(", ");
}
