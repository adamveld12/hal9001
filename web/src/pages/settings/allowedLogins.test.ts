import { describe, expect, it } from "vitest";
import {
  addLogin,
  emptyListNote,
  loginError,
  MAX_LOGINS,
  removeLogin,
  sameLogins,
} from "./allowedLogins";

describe("loginError", () => {
  it("accepts name@domain, with or without a dot in the domain", () => {
    expect(loginError("ada@example.com")).toBeNull();
    expect(loginError("  ada@example.com  ")).toBeNull();
    expect(loginError("ada@github")).toBeNull();
  });

  it("rejects what is not name@domain", () => {
    for (const bad of ["", "   ", "ada", "@example.com", "ada@", "a@b@c"]) {
      expect(loginError(bad), bad).not.toBeNull();
    }
  });

  it("rejects spaces, commas and control characters", () => {
    expect(loginError("ada lovelace@example.com")).toMatch(
      /one login at a time/,
    );
    expect(loginError("a@x.com,b@x.com")).toMatch(/one login at a time/);
    expect(loginError("ada@exa\u0007mple.com")).toMatch(/control/);
  });

  it("limits a login to 254 bytes", () => {
    const ok = `${"a".repeat(240)}@example.com`;
    expect(ok.length).toBeLessThanOrEqual(254);
    expect(loginError(ok)).toBeNull();
    expect(loginError(`${"a".repeat(250)}@example.com`)).toMatch(/254 bytes/);
    // 2 bytes per character.
    expect(loginError(`${"é".repeat(130)}@x.io`)).toMatch(/254 bytes/);
  });
});

describe("addLogin", () => {
  it("trims and appends", () => {
    expect(addLogin(["a@x.com"], "  b@x.com ")).toEqual({
      logins: ["a@x.com", "b@x.com"],
      error: null,
    });
  });

  it("drops a duplicate in any case without an error", () => {
    expect(addLogin(["Ada@Example.com"], "ada@example.com")).toEqual({
      logins: ["Ada@Example.com"],
      error: null,
    });
  });

  it("keeps the list and reports an invalid login", () => {
    const r = addLogin(["a@x.com"], "nope");
    expect(r.logins).toEqual(["a@x.com"]);
    expect(r.error).toMatch(/name@domain/);
  });

  it("stops at the maximum", () => {
    const full = Array.from({ length: MAX_LOGINS }, (_, i) => `u${i}@x.com`);
    expect(addLogin(full, "more@x.com").error).toMatch(/At most 100/);
  });
});

describe("removeLogin and sameLogins", () => {
  it("removes without regard to case", () => {
    expect(removeLogin(["a@x.com", "B@x.com"], "b@X.com")).toEqual(["a@x.com"]);
  });

  it("detects changes in content, order and case-insensitively equal lists", () => {
    expect(sameLogins(["a@x.com"], ["A@x.com"])).toBe(true);
    expect(sameLogins(["a@x.com", "b@x.com"], ["b@x.com", "a@x.com"])).toBe(
      false,
    );
    expect(sameLogins([], ["a@x.com"])).toBe(false);
  });
});

describe("emptyListNote", () => {
  it("names the owner", () => {
    expect(emptyListNote("ada@example.com")).toMatch(
      /only the owner of this Mac \(ada@example.com\)/,
    );
  });

  it("copes with an unknown owner", () => {
    expect(emptyListNote(null)).toMatch(/has not said who/);
  });
});
