import { describe, expect, it } from "vitest";
import { defaultTitle } from "../../lib/routeTitle";
import { scrollTargetId, TAILSCALE_REDIRECT } from "./authRoutes";

describe("the old Tailscale page", () => {
  it("redirects to the Auth page, scrolled to the Tailscale card", () => {
    const url = new URL(TAILSCALE_REDIRECT, "http://hal.test");
    expect(url.pathname).toBe("/settings/auth");
    expect(defaultTitle(url.pathname)).toBe("Auth");
    expect(scrollTargetId(url.hash)).toBe("tailscale");
  });

  it("is no longer a titled route of its own", () => {
    expect(defaultTitle("/settings/tailscale")).toBe("hal9001");
  });
});

describe("scrollTargetId", () => {
  it("reads the id from a hash", () => {
    expect(scrollTargetId("#tailscale")).toBe("tailscale");
    expect(scrollTargetId("#a%20b")).toBe("a b");
  });

  it("is null without a usable hash", () => {
    expect(scrollTargetId("")).toBeNull();
    expect(scrollTargetId("#")).toBeNull();
    expect(scrollTargetId("tailscale")).toBeNull();
  });

  it("keeps a hash that is not valid percent-encoding as it is", () => {
    expect(scrollTargetId("#100%")).toBe("100%");
  });
});
