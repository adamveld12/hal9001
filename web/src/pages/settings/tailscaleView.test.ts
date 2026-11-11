import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "../../api/client";
import type { TailscaleStatus } from "../../api/tailscale";
import {
  allowedHostEntry,
  allowedHostView,
  conflictTitle,
  loginsView,
  parsePort,
  parseServeFailure,
  parseWarnings,
  serveCommand,
  shareControl,
  shareUrl,
  statusRows,
  stopCommand,
  warningTone,
} from "./tailscaleView";

const DNS = "hal-mac.example-tailnet.ts.net";

function status(over: Partial<TailscaleStatus> = {}): TailscaleStatus {
  return {
    cli: { found: true, path: "/usr/local/bin/tailscale", version: "1.80.2" },
    backend_state: "Running",
    logged_in: true,
    dns_name: DNS,
    tailnet: "example-tailnet.ts.net",
    owner_login: "ada@example.com",
    https_enabled: true,
    magic_dns_enabled: true,
    serve: {
      enabled: false,
      https_port: 443,
      target: "http://127.0.0.1:9001",
      active: false,
      points_at_hal: false,
      conflict: null,
      funnel: false,
    },
    url: `https://${DNS}/`,
    allowed_host: { entry: DNS, present: false, added_by_hal: false },
    allowed_logins: ["ada@example.com"],
    warnings: [],
    error: null,
    checked_at: "2026-11-11T10:00:00Z",
    ...over,
  };
}

function apiError(status: number, kind: string, extra = {}): ApiError {
  return new ApiError(
    status,
    {
      type: `urn:hal9001:problem:${kind}`,
      title: "t",
      status,
      detail: `detail of ${kind}`,
      ...extra,
    } as Problem,
    "fallback",
  );
}

describe("parsePort", () => {
  it("accepts whole numbers from 1 to 65535", () => {
    expect(parsePort("443")).toEqual({ ok: true, port: 443 });
    expect(parsePort(" 8443 ")).toEqual({ ok: true, port: 8443 });
    expect(parsePort("1")).toEqual({ ok: true, port: 1 });
    expect(parsePort("65535")).toEqual({ ok: true, port: 65535 });
  });

  it("rejects empty, non-numeric and out-of-range input", () => {
    for (const bad of ["", "  ", "abc", "44.3", "-1", "0", "65536", "1e3"]) {
      expect(parsePort(bad).ok).toBe(false);
    }
  });
});

describe("commands", () => {
  it("spells out the serve command with the daemon target", () => {
    expect(serveCommand(443, "http://127.0.0.1:9001")).toBe(
      "tailscale serve --bg --yes --https=443 http://127.0.0.1:9001",
    );
    expect(serveCommand(8443, "http://127.0.0.1:9100")).toBe(
      "tailscale serve --bg --yes --https=8443 http://127.0.0.1:9100",
    );
  });

  it("spells out the stop command for one port", () => {
    expect(stopCommand(443)).toBe("tailscale serve --https=443 off");
    expect(stopCommand(8443)).toBe("tailscale serve --https=8443 off");
  });

  it("never uses funnel", () => {
    expect(serveCommand(443, "http://127.0.0.1:9001")).not.toContain("funnel");
    expect(stopCommand(443)).not.toContain("funnel");
  });
});

describe("shareUrl and allowedHostEntry", () => {
  it("leaves the port out for 443", () => {
    expect(shareUrl(DNS, 443)).toBe(`https://${DNS}/`);
    expect(allowedHostEntry(DNS, 443)).toBe(DNS);
  });

  it("adds the port otherwise", () => {
    expect(shareUrl(DNS, 8443)).toBe(`https://${DNS}:8443/`);
    expect(allowedHostEntry(DNS, 8443)).toBe(`${DNS}:8443`);
  });
});

describe("shareControl", () => {
  it("can share when Tailscale is ready", () => {
    expect(shareControl(status())).toEqual({
      shared: false,
      shareDisabledReason: null,
      canStop: false,
    });
  });

  it("is shared, and can stop, when the mapping points at hal", () => {
    const c = shareControl(
      status({
        serve: {
          ...status().serve,
          enabled: true,
          active: true,
          points_at_hal: true,
        },
      }),
    );
    expect(c.shared).toBe(true);
    expect(c.canStop).toBe(true);
  });

  it("can stop when hal keeps sharing but the mapping is gone", () => {
    const c = shareControl(
      status({ serve: { ...status().serve, enabled: true } }),
    );
    expect(c.shared).toBe(false);
    expect(c.canStop).toBe(true);
    expect(c.shareDisabledReason).toBeNull();
  });

  it("says why sharing is disabled", () => {
    expect(
      shareControl(
        status({
          cli: { found: false, path: null, version: null },
          backend_state: null,
          logged_in: false,
        }),
      ).shareDisabledReason,
    ).toMatch(/not installed/);
    expect(
      shareControl(status({ backend_state: null, logged_in: false }))
        .shareDisabledReason,
    ).toMatch(/not reachable/);
    expect(
      shareControl(status({ backend_state: "NeedsLogin", logged_in: false }))
        .shareDisabledReason,
    ).toMatch(/not signed in.*NeedsLogin/);
    expect(
      shareControl(status({ https_enabled: false })).shareDisabledReason,
    ).toMatch(/HTTPS certificates/);
    expect(
      shareControl(status({ dns_name: null })).shareDisabledReason,
    ).toMatch(/MagicDNS/);
  });
});

describe("statusRows", () => {
  it("shows the CLI version and path, machine name, tailnet and certificates", () => {
    const rows = statusRows(status());
    expect(rows.map((r) => r.key)).toEqual([
      "cli",
      "signed-in",
      "machine",
      "https",
    ]);
    expect(rows[0]).toMatchObject({
      text: "Found, version 1.80.2",
      tone: "ok",
      detail: "/usr/local/bin/tailscale",
    });
    expect(rows[1]).toMatchObject({ text: "Yes (Running)", tone: "ok" });
    expect(rows[2]).toMatchObject({
      text: DNS,
      detail: "Tailnet: example-tailnet.ts.net",
    });
    expect(rows[3]).toMatchObject({ text: "Enabled", tone: "ok" });
  });

  it("links the download page when the CLI is missing", () => {
    const [cli] = statusRows(
      status({ cli: { found: false, path: null, version: null } }),
    );
    expect(cli).toMatchObject({ text: "Not installed", tone: "bad" });
    expect(cli?.link?.href).toBe("https://tailscale.com/download");
  });

  it("links the DNS admin page when HTTPS certificates are disabled", () => {
    const https = statusRows(status({ https_enabled: false }))[3];
    expect(https).toMatchObject({ text: "Disabled", tone: "warn" });
    expect(https?.link?.href).toBe("https://login.tailscale.com/admin/dns");
  });

  it("shows the state and error when signed out", () => {
    const rows = statusRows(
      status({
        logged_in: false,
        backend_state: "NeedsLogin",
        error: "Logged out.",
      }),
    );
    expect(rows[1]).toMatchObject({
      text: "No (NeedsLogin)",
      tone: "warn",
      detail: "Logged out.",
    });
    expect(rows[3]?.text).toBe("–");
  });
});

describe("allowedHostView", () => {
  it("reports a missing entry", () => {
    const v = allowedHostView(status());
    expect(v).toMatchObject({ entry: DNS, text: "Missing", tone: "warn" });
  });

  it("notes an entry hal added", () => {
    const v = allowedHostView(
      status({
        allowed_host: { entry: DNS, present: true, added_by_hal: true },
      }),
    );
    expect(v).toMatchObject({ text: "Present", tone: "ok" });
    expect(v.note).toMatch(/Added by hal/);
  });

  it("notes an entry the user wrote", () => {
    const v = allowedHostView(
      status({
        allowed_host: { entry: DNS, present: true, added_by_hal: false },
      }),
    );
    expect(v.note).not.toMatch(/Added by hal/);
  });

  it("handles a missing machine name", () => {
    const v = allowedHostView(
      status({
        allowed_host: { entry: null, present: false, added_by_hal: false },
      }),
    );
    expect(v).toMatchObject({ entry: null, tone: "muted" });
  });
});

describe("loginsView", () => {
  it("lists the logins and explains the owner default", () => {
    const v = loginsView(status());
    expect(v.logins).toEqual(["ada@example.com"]);
    expect(v.note).toMatch(/empty, only this Mac's owner \(ada@example.com\)/);
  });

  it("says when nobody can sign in", () => {
    const v = loginsView(status({ allowed_logins: [], owner_login: null }));
    expect(v.logins).toEqual([]);
    expect(v.note).toMatch(/owner is unknown/);
  });
});

describe("warnings", () => {
  it("marks funnel, port conflicts and re-apply failures as bad", () => {
    expect(warningTone("funnel_enabled")).toBe("bad");
    expect(warningTone("port_conflict")).toBe("bad");
    expect(warningTone("reapply_failed")).toBe("bad");
    expect(warningTone("https_disabled")).toBe("warn");
  });

  it("reads warnings from a problem extension and drops malformed entries", () => {
    expect(
      parseWarnings([
        {
          code: "logged_out",
          message: "Not signed in",
          command: "tailscale up",
          url: "https://login.tailscale.com",
        },
        { code: "stopped", message: "Stopped" },
        { code: 3, message: "bad code" },
        { code: "stopped" },
        "nope",
        null,
      ]),
    ).toEqual([
      {
        code: "logged_out",
        message: "Not signed in",
        command: "tailscale up",
        url: "https://login.tailscale.com",
      },
      { code: "stopped", message: "Stopped", command: null, url: null },
    ]);
    expect(parseWarnings(undefined)).toEqual([]);
    expect(parseWarnings({})).toEqual([]);
  });
});

describe("parseServeFailure", () => {
  it("reads the target and port of a 409 already-exists", () => {
    const f = parseServeFailure(
      apiError(409, "already-exists", {
        target: "http://127.0.0.1:3000",
        https_port: 8443,
      }),
    );
    expect(f).toEqual({
      kind: "conflict",
      target: "http://127.0.0.1:3000",
      httpsPort: 8443,
    });
    if (f.kind !== "conflict") throw new Error("expected conflict");
    expect(conflictTitle(f, 443)).toBe(
      "Port 8443 already serves http://127.0.0.1:3000",
    );
  });

  it("falls back to the requested port and a generic title", () => {
    const f = parseServeFailure(apiError(409, "already-exists"));
    expect(f).toEqual({ kind: "conflict", target: "", httpsPort: null });
    if (f.kind !== "conflict") throw new Error("expected conflict");
    expect(conflictTitle(f, 443)).toBe(
      "Port 443 already serves something else",
    );
  });

  it("reads the warnings of a 409 invalid-state", () => {
    const f = parseServeFailure(
      apiError(409, "invalid-state", {
        warnings: [{ code: "logged_out", message: "Not signed in" }],
      }),
    );
    expect(f).toEqual({
      kind: "not-ready",
      message: "detail of invalid-state",
      warnings: [
        {
          code: "logged_out",
          message: "Not signed in",
          command: null,
          url: null,
        },
      ],
    });
  });

  it("keeps the command output of a 503 unavailable", () => {
    expect(parseServeFailure(apiError(503, "unavailable"))).toEqual({
      kind: "failed",
      message: "detail of unavailable",
    });
  });

  it("does not treat a plain conflict as a port conflict", () => {
    expect(parseServeFailure(apiError(409, "conflict")).kind).toBe("failed");
  });

  it("handles errors that are not ApiError", () => {
    expect(parseServeFailure(new Error("x"))).toEqual({
      kind: "failed",
      message: "Error: x",
    });
  });
});
