import { describe, expect, it } from "vitest";
import type { Passkey } from "../../api/auth";
import { groupPasskeys, stackViews } from "./authView";

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

describe("stackViews", () => {
  it("lists which services each stack guards and its modules in order", () => {
    const views = stackViews({
      stacks: {
        default: [
          { module: "localhost-trust", control: "sufficient" },
          {
            module: "password",
            control: "sufficient",
            options: { password_hash: "«redacted»", attempts: 5 },
          },
        ],
        strict: [{ module: "webauthn", control: "required" }],
      },
      services: { ui: "default", api: "default", admin: "strict" },
    });
    expect(views.map((v) => [v.name, v.services])).toEqual([
      ["default", ["ui", "api", "approve"]],
      ["strict", ["admin"]],
    ]);
    expect(views[0]?.entries.map((e) => e.module)).toEqual([
      "localhost-trust",
      "password",
    ]);
    expect(views[0]?.entries[1]?.options).toEqual([
      ["password_hash", "«redacted»"],
      ["attempts", "5"],
    ]);
  });

  it("covers a config that omits auth: every service uses the default stack", () => {
    const views = stackViews(undefined);
    expect(views).toEqual([
      {
        name: "default",
        services: ["ui", "api", "approve", "admin"],
        entries: [],
      },
    ]);
  });
});
