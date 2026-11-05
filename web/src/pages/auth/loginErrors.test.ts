import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "../../api/client";
import { loginErrorMessage, methodsFrom } from "./loginErrors";

function problem(kind: string, extra: Record<string, unknown> = {}): ApiError {
  const status = kind === "login-expired" ? 410 : 401;
  return new ApiError(
    status,
    {
      type: `urn:hal9001:problem:${kind}`,
      title: "t",
      status,
      ...extra,
    } as Problem,
    "fallback",
  );
}

describe("methodsFrom", () => {
  it("reads the interactive methods a 401 reports", () => {
    expect(methodsFrom({ methods: ["webauthn", "password"] })).toEqual([
      "webauthn",
      "password",
    ]);
  });

  it("distinguishes 'none reported' from 'unknown'", () => {
    expect(methodsFrom({ methods: [] })).toEqual([]);
    expect(methodsFrom({})).toBeUndefined();
    expect(methodsFrom({ methods: [1] })).toBeUndefined();
    expect(methodsFrom(undefined)).toBeUndefined();
  });
});

describe("loginErrorMessage", () => {
  it("reports a wrong password", () => {
    expect(
      loginErrorMessage(problem("authentication-failed"), "password"),
    ).toBe("Wrong password.");
  });

  it("explains a passkey login when no passkey exists for the host", () => {
    const e = problem("authentication-failed", {
      trace: [{ module: "webauthn", result: "ignore" }],
    });
    expect(loginErrorMessage(e, "webauthn")).toBe(
      "No passkey is registered for this host name.",
    );
  });

  it("reports a rejected assertion differently from a missing passkey", () => {
    const e = problem("authentication-failed", {
      trace: [{ module: "webauthn", result: "fail" }],
    });
    expect(loginErrorMessage(e, "webauthn")).toBe(
      "The passkey was not accepted.",
    );
  });

  it("asks to retry when the exchange expired", () => {
    expect(loginErrorMessage(problem("login-expired"), "webauthn")).toMatch(
      /Try again/,
    );
  });

  it("maps a dismissed passkey prompt, and passes other errors through", () => {
    const dismissed = Object.assign(new Error("x"), {
      name: "NotAllowedError",
    });
    expect(loginErrorMessage(dismissed, "webauthn")).toMatch(/cancelled/);
    expect(loginErrorMessage(new Error("offline"), "password")).toBe("offline");
  });
});
