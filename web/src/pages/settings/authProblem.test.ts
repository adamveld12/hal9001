import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "../../api/client";
import {
  changeErrorLines,
  confirmReasons,
  diagnosticsByPath,
  parseConfirmation,
  parseRateLimit,
  rateLimitMessage,
  waitText,
} from "./authProblem";

const err = (status: number, kind: string, extra: Record<string, unknown>) =>
  new ApiError(
    status,
    {
      type: `urn:hal9001:problem:${kind}`,
      title: kind,
      status,
      ...extra,
    } as Problem,
    "fallback",
  );

describe("parseConfirmation", () => {
  it("reads the reasons and whether the session ends", () => {
    const c = parseConfirmation(
      err(409, "confirmation-required", {
        reasons: ["Passkeys sign-in goes off.", 3, ""],
        ends_session: true,
      }),
    );
    expect(c).toEqual({
      reasons: ["Passkeys sign-in goes off."],
      endsSession: true,
    });
  });

  it("treats a missing ends_session as false and missing reasons as none", () => {
    expect(parseConfirmation(err(409, "confirmation-required", {}))).toEqual({
      reasons: [],
      endsSession: false,
    });
  });

  it("ignores other conflicts and other errors", () => {
    expect(parseConfirmation(err(409, "conflict", {}))).toBeNull();
    expect(parseConfirmation(err(409, "invalid-state", {}))).toBeNull();
    expect(parseConfirmation(err(422, "confirmation-required", {}))).toBeNull();
    expect(parseConfirmation(new Error("boom"))).toBeNull();
  });
});

describe("rate limits", () => {
  it("reads retry_after from a 429", () => {
    const rl = parseRateLimit(err(429, "rate-limited", { retry_after: 600 }));
    expect(rl).toEqual({ retryAfter: 600 });
    expect(rateLimitMessage(rl!)).toBe(
      "Too many failed attempts. Try again in 10 minutes.",
    );
  });

  it("copes with a 429 that names no wait", () => {
    const rl = parseRateLimit(err(429, "rate-limited", {}));
    expect(rl).toEqual({ retryAfter: null });
    expect(rateLimitMessage(rl!)).toBe(
      "Too many failed attempts. Try again later.",
    );
  });

  it("is null for other statuses", () => {
    expect(parseRateLimit(err(422, "validation-failed", {}))).toBeNull();
  });

  it("rounds the wait up to the unit", () => {
    expect(waitText(1)).toBe("1 second");
    expect(waitText(0.2)).toBe("1 second");
    expect(waitText(59)).toBe("59 seconds");
    expect(waitText(61)).toBe("2 minutes");
    expect(waitText(900)).toBe("15 minutes");
    expect(waitText(3601)).toBe("2 hours");
  });
});

describe("diagnosticsByPath", () => {
  it("groups the messages by path and keeps the ones without a path apart", () => {
    const d = diagnosticsByPath(
      err(422, "validation-failed", {
        diagnostics: [
          {
            severity: "error",
            code: "invalid",
            message: "wrong password",
            path: "current_password",
          },
          {
            severity: "error",
            code: "invalid",
            message: "too short",
            path: "password",
          },
          {
            severity: "error",
            code: "invalid",
            message: "bad again",
            path: "password",
          },
          { severity: "error", code: "x", message: "general" },
          { message: 3 },
          null,
        ],
      }),
    );
    expect(d.byPath).toEqual({
      current_password: ["wrong password"],
      password: ["too short", "bad again"],
    });
    expect(d.general).toEqual(["general"]);
  });

  it("is empty for other errors", () => {
    expect(diagnosticsByPath(new Error("x"))).toEqual({
      byPath: {},
      general: [],
    });
  });
});

describe("changeErrorLines", () => {
  it("prefers the wait of a rate limit", () => {
    expect(
      changeErrorLines(err(429, "rate-limited", { retry_after: 120 })),
    ).toEqual(["Too many failed attempts. Try again in 2 minutes."]);
  });

  it("lists diagnostics, with their path", () => {
    expect(
      changeErrorLines(
        err(422, "validation-failed", {
          diagnostics: [
            { message: "no", path: "logins[1]" },
            { message: "general" },
          ],
        }),
      ),
    ).toEqual(["general", "logins[1]: no"]);
  });

  it("falls back to the message", () => {
    expect(
      changeErrorLines(
        err(409, "invalid-state", { detail: "set a password first" }),
      ),
    ).toEqual(["set a password first"]);
    expect(changeErrorLines(new Error("offline"))).toEqual(["offline"]);
  });
});

describe("changeErrorLines shownElsewhere", () => {
  it("leaves out the diagnostics a form field shows, and says nothing when only those exist", () => {
    const e = err(422, "validation-failed", {
      diagnostics: [{ message: "wrong password", path: "current_password" }],
    });
    expect(changeErrorLines(e, ["current_password"])).toEqual([]);
    expect(changeErrorLines(e)).toEqual(["current_password: wrong password"]);
  });
});

describe("confirmReasons", () => {
  const ends =
    "This ends the session you are using now, so you will have to sign in again.";
  it("drops the reason that restates the ends-session warning, once the warning is shown", () => {
    expect(
      confirmReasons({
        reasons: [ends, "Password sign-in will be off."],
        endsSession: true,
      }),
    ).toEqual(["Password sign-in will be off."]);
  });
  it("keeps it when the change does not end the session, and drops repeats", () => {
    expect(
      confirmReasons({ reasons: [ends, ends], endsSession: false }),
    ).toEqual([ends]);
  });
});
