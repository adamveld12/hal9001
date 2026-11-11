import { describe, expect, it } from "vitest";
import type { AuthMethod, AuthOverview, Passkey } from "../../api/auth";
import {
  actionLabel,
  authPageContext,
  groupPasskeys,
  methodById,
  STATE_LABEL,
  STATE_TONE,
  stackSummaries,
} from "./authView";

const pk = (id: string, rp_id: string, created_at: string): Passkey => ({
  id,
  rp_id,
  created_at,
  sign_count: 0,
  label: id,
});

describe("groupPasskeys", () => {
  it("groups per host, current host first, newest first within a host", () => {
    const groups = groupPasskeys(
      [
        pk("a", "mac.tail.ts.net", "2026-01-01T00:00:00Z"),
        pk("b", "localhost", "2026-01-02T00:00:00Z"),
        pk("c", "mac.tail.ts.net", "2026-02-01T00:00:00Z"),
        pk("d", "alpha.ts.net", "2026-01-01T00:00:00Z"),
      ],
      "mac.tail.ts.net",
    );
    expect(groups.map((g) => g.rpId)).toEqual([
      "mac.tail.ts.net",
      "alpha.ts.net",
      "localhost",
    ]);
    expect(groups[0]?.passkeys.map((p) => p.id)).toEqual(["c", "a"]);
  });

  it("is empty without passkeys", () => {
    expect(groupPasskeys([], "localhost")).toEqual([]);
  });
});

describe("stackSummaries", () => {
  it("lists which services each stack guards and its modules in order, without options", () => {
    const views = stackSummaries({
      stacks: {
        default: [
          { module: "localhost-trust", control: "sufficient" },
          {
            module: "password",
            control: "sufficient",
            options: { hash: "«redacted»" },
          },
        ],
        strict: [{ module: "webauthn", control: "required" }],
      },
      services: {
        ui: "default",
        api: "default",
        approve: "default",
        admin: "strict",
      },
    });
    expect(views).toEqual([
      {
        name: "default",
        services: ["ui", "api", "approve"],
        modules: ["localhost-trust (sufficient)", "password (sufficient)"],
      },
      { name: "strict", services: ["admin"], modules: ["webauthn (required)"] },
    ]);
  });

  it("lists a stack a service names but the config lacks, with no modules", () => {
    expect(
      stackSummaries({
        stacks: {},
        services: { ui: "x", api: "x", approve: "x", admin: "x" },
      }),
    ).toEqual([
      { name: "x", services: ["ui", "api", "approve", "admin"], modules: [] },
    ]);
  });
});

const method = (
  id: AuthMethod["id"],
  state: AuthMethod["state"],
  over: Partial<AuthMethod> = {},
): AuthMethod => ({
  id,
  module: id,
  name: id,
  state,
  enabled: state !== "off",
  toggleable: id !== "localhost",
  custom: false,
  detail: "",
  ...over,
});

const overview: AuthOverview = {
  methods: [
    method("localhost", "on", { name: "On this Mac" }),
    method("tailscale", "attention", {
      name: "From your tailnet",
      custom: true,
    }),
    method("passkeys", "off", { name: "Passkeys" }),
    method("password", "on", { name: "Password" }),
  ],
  current: {
    method: "localhost",
    modules: ["localhost-trust"],
    remote: false,
    source: "stack",
    principal: "local",
    session_id: null,
  },
  password: { set: true, enabled: true, last_used_at: null },
  passkey_count: 0,
  allowed_logins: ["ada@example.com"],
};

describe("authPageContext", () => {
  it("waits for the overview", () => {
    expect(
      authPageContext({
        overview: undefined,
        stacks: undefined,
        tailscale: undefined,
      }),
    ).toBeNull();
  });

  it("states which ways in are on and whether a password is set, never the logins or options", () => {
    const ctx = authPageContext({
      overview,
      stacks: {
        stacks: {
          default: [
            {
              module: "password",
              control: "sufficient",
              options: { hash: "$argon2id$secret" },
            },
          ],
        },
        services: {
          ui: "default",
          api: "default",
          approve: "default",
          admin: "default",
        },
      },
      tailscale: undefined,
    });
    const facts = Object.fromEntries(
      (ctx?.facts ?? []).map((f) => [f.label, f.value]),
    );
    expect(facts).toMatchObject({
      "On this Mac": "on",
      "From your tailnet": "attention (custom stacks)",
      Passkeys: "off",
      Password: "on",
      "password set": "true",
      "tailnet logins listed": "1",
    });
    const text = JSON.stringify(ctx);
    expect(text).not.toContain("argon2");
    expect(text).not.toContain("ada@example.com");
    expect(ctx?.entities).toEqual([
      {
        kind: "auth_stack",
        id: "default",
        detail: "guards ui, api, approve, admin; modules password (sufficient)",
      },
    ]);
  });
});

describe("methodById and state labels", () => {
  it("finds a method and labels its state", () => {
    expect(methodById(overview, "passkeys")?.state).toBe("off");
    expect(STATE_LABEL.attention).toBe("Needs attention");
    expect(STATE_TONE).toEqual({ on: "ok", off: "muted", attention: "warn" });
  });
});

describe("actionLabel", () => {
  it("names the sign-in changes and keeps the other actions readable", () => {
    expect(actionLabel("auth_password_change")).toBe("password changed");
    expect(actionLabel("auth_stacks_change")).toBe("auth stacks changed");
    expect(actionLabel("login_begin")).toBe("login begin");
  });
});
