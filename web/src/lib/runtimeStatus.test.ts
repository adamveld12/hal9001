import { describe, expect, it } from "vitest";
import {
  adapterAction,
  adapterSummary,
  nodeBlocker,
  orderWarnings,
  reduceProgress,
  runtimeLabel,
  runtimeOption,
  runtimeOptions,
  statusLabel,
  statusTone,
  type ProgressByRuntime,
  type RuntimeDetection,
} from "./runtimeStatus";

const detection = (over: Partial<RuntimeDetection> = {}): RuntimeDetection => ({
  status: "ready",
  cli: { name: "claude", found: true, path: "/usr/bin/claude", version: "2.1" },
  auth: { status: "ok", command: null },
  adapter: {
    source: "managed",
    installed: false,
    version: null,
    pinned_version: "0.100.0",
    path: null,
    installable: true,
    installing: false,
  },
  node: { found: true, version: "22.4.0", ok: true },
  command: null,
  warnings: [],
  checked_at: "2026-01-01T00:00:00Z",
  ...over,
});

const warn = (code: string) => ({
  code: code as never,
  message: code,
  command: null,
  url: null,
});

describe("status mapping", () => {
  it("names and colours the three statuses", () => {
    expect(statusLabel("ready")).toBe("Configured");
    expect(statusLabel("warning")).toBe("Needs attention");
    expect(statusLabel("unavailable")).toBe("Not available");
    expect([
      statusTone("ready"),
      statusTone("warning"),
      statusTone("unavailable"),
    ]).toEqual(["ok", "warn", "bad"]);
  });

  it("orders warnings by urgency and keeps the server order within a code", () => {
    const sorted = orderWarnings([
      warn("auth_unknown"),
      warn("auth_missing"),
      warn("mystery"),
      warn("cli_missing"),
      warn("node_old"),
    ]);
    expect(sorted.map((w) => w.code)).toEqual([
      "cli_missing",
      "node_old",
      "auth_missing",
      "auth_unknown",
      "mystery",
    ]);
  });
});

describe("dropdown labels", () => {
  it("adds a status hint for runtimes that are not ready", () => {
    expect(runtimeLabel("omp", detection())).toBe("omp");
    expect(runtimeLabel("codex", detection({ status: "unavailable" }))).toBe(
      "codex (Not set up)",
    );
    expect(
      runtimeLabel(
        "claude",
        detection({ status: "warning", warnings: [warn("auth_missing")] }),
      ),
    ).toBe("claude (Sign-in needed)");
    expect(
      runtimeLabel(
        "omp",
        detection({ status: "warning", warnings: [warn("providers_missing")] }),
      ),
    ).toBe("omp (No providers)");
    expect(
      runtimeLabel(
        "x",
        detection({ status: "warning", warnings: [warn("auth_unknown")] }),
      ),
    ).toBe("x (Needs attention)");
  });

  it("falls back to the plain name without detection data", () => {
    expect(runtimeLabel("fake", undefined)).toBe("fake");
  });

  it("keeps an unconfigured current value selectable, listed first", () => {
    const opts = [runtimeOption({ name: "a" })];
    expect(runtimeOptions(opts, "a")).toEqual(opts);
    expect(runtimeOptions(opts, "")).toEqual(opts);
    expect(runtimeOptions(opts, "gone").map((o) => o.label)).toEqual([
      "gone (unknown)",
      "a",
    ]);
  });
});

describe("adapter install state", () => {
  it("offers install when installable and missing", () => {
    expect(adapterAction(detection(), undefined)).toEqual({
      kind: "install",
      label: "Install adapter",
    });
  });

  it("disables install without Node 22+, with the reason", () => {
    const missing = adapterAction(
      detection({ node: { found: false, version: null, ok: false } }),
      undefined,
    );
    expect(missing).toMatchObject({ kind: "install_disabled" });
    expect(
      nodeBlocker({ found: true, version: "18.0.0", ok: false }),
    ).toContain("18.0.0");
    expect(
      nodeBlocker({ found: true, version: "22.1.0", ok: true }),
    ).toBeNull();
  });

  it("shows live progress while installing, from the server flag or an event", () => {
    const d = detection();
    const installing = detection({
      adapter: { ...d.adapter, installing: true },
    });
    expect(adapterAction(installing, undefined)).toEqual({
      kind: "installing",
      lines: [],
    });
    expect(
      adapterAction(d, { phase: "installing", lines: ["added 3 packages"] }),
    ).toEqual({ kind: "installing", lines: ["added 3 packages"] });
  });

  it("offers retry after a failure", () => {
    expect(adapterAction(detection(), { phase: "failed", lines: [] })).toEqual({
      kind: "install",
      label: "Retry install",
    });
    expect(
      adapterAction(
        detection({ warnings: [warn("adapter_install_failed")] }),
        undefined,
      ),
    ).toMatchObject({ label: "Retry install" });
  });

  it("offers removal only for an installed managed adapter", () => {
    const d = detection();
    expect(
      adapterAction(
        detection({ adapter: { ...d.adapter, installed: true } }),
        undefined,
      ),
    ).toEqual({ kind: "remove" });
    expect(
      adapterAction(
        detection({
          adapter: { ...d.adapter, source: "custom", installable: false },
        }),
        undefined,
      ),
    ).toEqual({ kind: "none" });
    expect(
      adapterAction(
        detection({
          adapter: { ...d.adapter, source: "builtin", installable: false },
        }),
        undefined,
      ),
    ).toEqual({ kind: "none" });
  });

  it("summarises the adapter", () => {
    const a = detection().adapter;
    expect(adapterSummary({ ...a, source: "builtin" })).toBe("Built in");
    expect(adapterSummary({ ...a, installed: true, version: "0.100.0" })).toBe(
      "Installed by hal · v0.100.0",
    );
    expect(adapterSummary({ ...a, source: "custom" })).toBe("Custom command");
    expect(adapterSummary({ ...a, source: "none" })).toBe("Not installed");
  });
});

describe("reduceProgress", () => {
  it("keeps the last lines, starts a fresh log for a new install", () => {
    let s: ProgressByRuntime = {};
    for (let i = 1; i <= 7; i++)
      s = reduceProgress(s, {
        name: "claude",
        phase: "installing",
        line: `l${i}`,
      });
    expect(s.claude).toMatchObject({
      phase: "installing",
      lines: ["l3", "l4", "l5", "l6", "l7"],
    });
    s = reduceProgress(s, { name: "claude", phase: "failed", error: "boom" });
    expect(s.claude).toMatchObject({
      phase: "failed",
      error: "boom",
    });
    s = reduceProgress(s, {
      name: "claude",
      phase: "installing",
      line: "again",
    });
    expect(s.claude.lines).toEqual(["again"]);
  });
});
