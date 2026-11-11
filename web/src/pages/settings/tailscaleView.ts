import { ApiError } from "../../api/client";
import type { TailscaleStatus, TailscaleWarning } from "../../api/tailscale";
import type { Tone } from "../../lib/status";

export const TAILSCALE_DOWNLOAD_URL = "https://tailscale.com/download";
export const TAILSCALE_DNS_ADMIN_URL = "https://login.tailscale.com/admin/dns";
export const DEFAULT_HTTPS_PORT = 443;

type Port = { ok: true; port: number } | { ok: false; error: string };

/** The HTTPS port field: a whole number from 1 to 65535. */
export function parsePort(text: string): Port {
  const t = text.trim();
  if (t === "") return { ok: false, error: "Enter a port." };
  if (!/^\d+$/.test(t)) return { ok: false, error: "Use a whole number." };
  const port = Number(t);
  if (port < 1 || port > 65535) {
    return { ok: false, error: "Use a port from 1 to 65535." };
  }
  return { ok: true, port };
}

/** The command `POST /api/tailscale/serve` runs; `target` is `serve.target` (`http://127.0.0.1:<hal port>`). */
export function serveCommand(port: number, target: string): string {
  return `tailscale serve --bg --yes --https=${port} ${target}`;
}

/** The command `DELETE /api/tailscale/serve` runs, for hal's own mapping only. */
export function stopCommand(port: number): string {
  return `tailscale serve --https=${port} off`;
}

/** Turns funnel off on an HTTPS port, which hal9001 requires before it shares on it. */
export function funnelOffCommand(port: number): string {
  return `tailscale funnel --https=${port} off`;
}

/** Why hal will not share on a port that funnel exposes to the public internet. */
export function funnelMessage(port: number): string {
  return `HTTPS port ${port} is exposed to the public internet with tailscale funnel; hal only shares on your tailnet. Turn funnel off or pick another port.`;
}

/** `https://<name>/`, or `https://<name>:<port>/` for a port other than 443. */
export function shareUrl(dnsName: string, port: number): string {
  return port === DEFAULT_HTTPS_PORT
    ? `https://${dnsName}/`
    : `https://${dnsName}:${port}/`;
}

/** The `allowed_hosts` entry for the URL: `<name>`, or `<name>:<port>` for a port other than 443. */
export function allowedHostEntry(dnsName: string, port: number): string {
  return port === DEFAULT_HTTPS_PORT ? dnsName : `${dnsName}:${port}`;
}

export interface ShareControl {
  /** The mapping on the HTTPS port proxies to hal. */
  shared: boolean;
  /** Why "Share" cannot be used now; null when it can. */
  shareDisabledReason: string | null;
  /** hal keeps a mapping (config) or has one live: "Stop sharing" has something to do. */
  canStop: boolean;
}

/**
 * Whether sharing can start or stop, from the status alone. `port` is the HTTPS port typed in the form (default:
 * the configured one); the status knows funnel only for the configured port, and the server checks any other.
 */
export function shareControl(
  s: TailscaleStatus,
  port: number = s.serve.https_port,
): ShareControl {
  const shared = s.serve.points_at_hal;
  return {
    shared,
    shareDisabledReason: shareDisabledReason(s, port),
    canStop: shared || s.serve.enabled,
  };
}

function shareDisabledReason(s: TailscaleStatus, port: number): string | null {
  if (!s.cli.found) {
    return "The tailscale command-line tool is not installed.";
  }
  if (s.backend_state === null) {
    return "tailscaled is not reachable.";
  }
  if (!s.logged_in) {
    return `Tailscale is not signed in and running (state: ${s.backend_state}).`;
  }
  if (!s.https_enabled) {
    return "HTTPS certificates are disabled for your tailnet.";
  }
  if (s.dns_name === null) {
    return "This Mac has no MagicDNS name yet.";
  }
  if (s.serve.funnel_on_port && port === s.serve.https_port) {
    return funnelMessage(port);
  }
  return null;
}

export interface StatusRow {
  key: "cli" | "signed-in" | "machine" | "https";
  label: string;
  text: string;
  tone: Tone;
  /** Secondary line under the text. */
  detail?: string;
  link?: { href: string; label: string };
}

/** The status rows: CLI, sign-in, machine name and tailnet, HTTPS certificates. */
export function statusRows(s: TailscaleStatus): StatusRow[] {
  const cli: StatusRow = s.cli.found
    ? {
        key: "cli",
        label: "Tailscale CLI",
        text: s.cli.version ? `Found, version ${s.cli.version}` : "Found",
        tone: "ok",
        detail: s.cli.path ?? undefined,
      }
    : {
        key: "cli",
        label: "Tailscale CLI",
        text: "Not installed",
        tone: "bad",
        detail: "Install Tailscale, then check again.",
        link: { href: TAILSCALE_DOWNLOAD_URL, label: "Download Tailscale" },
      };

  const signedIn: StatusRow = s.logged_in
    ? {
        key: "signed-in",
        label: "Signed in",
        text: `Yes (${s.backend_state ?? "Running"})`,
        tone: "ok",
      }
    : {
        key: "signed-in",
        label: "Signed in",
        text: s.backend_state ? `No (${s.backend_state})` : "Unknown",
        tone: s.cli.found ? "warn" : "muted",
        detail: s.error ?? undefined,
      };

  const machine: StatusRow = {
    key: "machine",
    label: "Machine name",
    text: s.dns_name ?? "–",
    tone: s.dns_name ? "ok" : "muted",
    detail: s.tailnet ? `Tailnet: ${s.tailnet}` : undefined,
  };

  const https: StatusRow = !s.logged_in
    ? { key: "https", label: "HTTPS certificates", text: "–", tone: "muted" }
    : s.https_enabled
      ? {
          key: "https",
          label: "HTTPS certificates",
          text: "Enabled",
          tone: "ok",
        }
      : {
          key: "https",
          label: "HTTPS certificates",
          text: "Disabled",
          tone: "warn",
          detail:
            "`tailscale serve` over HTTPS needs them. Turn them on in the admin console under DNS.",
          link: { href: TAILSCALE_DNS_ADMIN_URL, label: "Open DNS settings" },
        };

  return [cli, signedIn, machine, https];
}

export interface AllowedHostView {
  entry: string | null;
  text: string;
  tone: Tone;
  note: string;
}

/** The `allowed_hosts` row: the entry that matches the URL, present or missing, and who added it. */
export function allowedHostView(s: TailscaleStatus): AllowedHostView {
  const { entry, present, added_by_hal } = s.allowed_host;
  if (entry === null) {
    return {
      entry,
      text: "No machine name yet",
      tone: "muted",
      note: "The entry is known once Tailscale reports a MagicDNS name.",
    };
  }
  if (present) {
    return {
      entry,
      text: "Present",
      tone: "ok",
      note: added_by_hal
        ? "Added by hal; stopping sharing removes it."
        : "Listed in allowed_hosts in config.yaml.",
    };
  }
  return {
    entry,
    text: "Missing",
    tone: "warn",
    note: "Requests through Tailscale are rejected until it is in allowed_hosts in config.yaml. Sharing adds it.",
  };
}

export interface LoginsView {
  logins: string[];
  note: string;
}

/** Who `tailscale-identity` accepts. `allowed_logins` is already the owner when config.yaml lists none. */
export function loginsView(s: TailscaleStatus): LoginsView {
  const owner = s.owner_login;
  if (s.allowed_logins.length === 0) {
    return {
      logins: [],
      note: owner
        ? "No login is accepted. Set allowed_logins in config.yaml."
        : "No login is accepted: this Mac's owner is unknown. Set allowed_logins in config.yaml.",
    };
  }
  return {
    logins: s.allowed_logins,
    note: owner
      ? `When allowed_logins in config.yaml is empty, only this Mac's owner (${owner}) is accepted.`
      : "Set by allowed_logins in config.yaml.",
  };
}

const BAD_WARNINGS: Partial<Record<TailscaleWarning["code"], true>> = {
  funnel_enabled: true,
  funnel_on_port: true,
  port_conflict: true,
  reapply_failed: true,
};

export function warningTone(code: TailscaleWarning["code"]): Tone {
  return BAD_WARNINGS[code] ? "bad" : "warn";
}

/** `TailscaleWarning[]` from an untyped problem extension; entries without a code or message are dropped. */
export function parseWarnings(value: unknown): TailscaleWarning[] {
  if (!Array.isArray(value)) return [];
  const out: TailscaleWarning[] = [];
  for (const item of value as unknown[]) {
    if (typeof item !== "object" || item === null) continue;
    const w = item as Record<string, unknown>;
    if (typeof w.code !== "string" || typeof w.message !== "string") continue;
    out.push({
      code: w.code as TailscaleWarning["code"],
      message: w.message,
      command: typeof w.command === "string" ? w.command : null,
      url: typeof w.url === "string" ? w.url : null,
    });
  }
  return out;
}

export type ServeFailure =
  /** 409 `already-exists`: the HTTPS port already serves something else. */
  | { kind: "conflict"; target: string; httpsPort: number | null }
  /** 409 `invalid-state`: Tailscale is not ready; `warnings` lists the fixes. */
  | { kind: "not-ready"; message: string; warnings: TailscaleWarning[] }
  /** 409 `invalid-state` with `funnel_on_port`: funnel exposes the HTTPS port to the internet. */
  | { kind: "funnel"; message: string; httpsPort: number | null }
  /** 503 `unavailable` or anything else: `message` has the command output. */
  | { kind: "failed"; message: string };

/** What went wrong with `POST /api/tailscale/serve`. */
export function parseServeFailure(e: unknown): ServeFailure {
  if (!(e instanceof ApiError)) {
    return { kind: "failed", message: String(e) };
  }
  const extra: Record<string, unknown> = e.problem ?? {};
  if (e.status === 409 && e.kind === "already-exists") {
    return {
      kind: "conflict",
      target: typeof extra.target === "string" ? extra.target : "",
      httpsPort: typeof extra.https_port === "number" ? extra.https_port : null,
    };
  }
  if (e.status === 409 && e.kind === "invalid-state") {
    if (extra.funnel_on_port === true) {
      return {
        kind: "funnel",
        message: e.message,
        httpsPort:
          typeof extra.https_port === "number" ? extra.https_port : null,
      };
    }
    return {
      kind: "not-ready",
      message: e.message,
      warnings: parseWarnings(extra.warnings),
    };
  }
  return { kind: "failed", message: e.message };
}

/** Title of the replace dialog. `fallbackPort` is the port that was requested. */
export function conflictTitle(
  failure: Extract<ServeFailure, { kind: "conflict" }>,
  fallbackPort: number,
): string {
  const port = failure.httpsPort ?? fallbackPort;
  return failure.target
    ? `Port ${port} already serves ${failure.target}`
    : `Port ${port} already serves something else`;
}
