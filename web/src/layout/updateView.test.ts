import { describe, expect, it } from "vitest";
import type { UpdateStatus } from "../api/update";
import { bannerView } from "./updateView";

const base = {
  enabled: true,
  disabled_reason: null,
  current: { bundle_id: "dev.hal9001", version: "0.1.1", build: 1 },
  channel: "stable",
  auto_install_when_idle: false,
  state: "up_to_date",
  latest: {
    version: "0.1.2",
    build: 2,
    notes_url: "https://example.com/n",
  },
  available: false,
  pending: false,
  phase: null,
  busy: { running_runs: 0 },
  installer_connected: true,
  last_checked_at: null,
  error: null,
} as unknown as UpdateStatus;

const with_ = (p: Partial<UpdateStatus>): UpdateStatus => ({ ...base, ...p });

describe("bannerView", () => {
  it("hides when disabled or up to date", () => {
    expect(bannerView(with_({ enabled: false }), null).kind).toBe("hidden");
    expect(bannerView(base, null).kind).toBe("hidden");
  });

  it("offers an available update and hints when no installer is connected", () => {
    const v = bannerView(
      with_({
        state: "available",
        available: true,
        installer_connected: false,
      }),
      null,
    );
    expect(v).toMatchObject({
      kind: "available",
      version: "0.1.2",
      hint: true,
    });
  });

  it("hides a dismissed version but not install progress or errors", () => {
    const avail = with_({ state: "available", available: true });
    expect(bannerView(avail, "0.1.2").kind).toBe("hidden");
    expect(bannerView(avail, "0.1.1").kind).toBe("available");
    expect(
      bannerView(with_({ state: "installing", phase: "downloading" }), "0.1.2"),
    ).toEqual({ kind: "installing", phase: "downloading" });
    expect(
      bannerView(with_({ state: "error", error: "boom" }), "0.1.2").kind,
    ).toBe("error");
  });

  it("shows the running flow count while pending", () => {
    const v = bannerView(
      with_({ state: "pending", pending: true, busy: { running_runs: 2 } }),
      null,
    );
    expect(v).toEqual({ kind: "pending", version: "0.1.2", running: 2 });
  });
});
