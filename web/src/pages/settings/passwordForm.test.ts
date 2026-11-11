import { describe, expect, it } from "vitest";
import { hasErrors, passwordFormErrors } from "./passwordForm";

describe("passwordFormErrors", () => {
  it("passes a matching password of at least 12 characters", () => {
    const e = passwordFormErrors({
      current: null,
      password: "correct horse",
      repeat: "correct horse",
    });
    expect(e).toEqual({});
    expect(hasErrors(e)).toBe(false);
  });

  it("asks for 12 characters, counting an emoji as one", () => {
    expect(
      passwordFormErrors({
        current: null,
        password: "elevenchars",
        repeat: "elevenchars",
      }).password,
    ).toMatch(/at least 12/);
    const twelve = "🙂".repeat(12);
    expect(
      passwordFormErrors({ current: null, password: twelve, repeat: twelve }),
    ).toEqual({});
  });

  it("flags a mismatch under the repeat field", () => {
    expect(
      passwordFormErrors({
        current: null,
        password: "correct horse",
        repeat: "correct horsf",
      }),
    ).toEqual({ repeat: "The two passwords do not match." });
  });

  it("does not trim: spaces count", () => {
    expect(
      passwordFormErrors({
        current: null,
        password: "            ",
        repeat: "            ",
      }),
    ).toEqual({});
  });

  it("needs the current password only when asked for it", () => {
    const form = { password: "correct horse", repeat: "correct horse" };
    expect(passwordFormErrors({ ...form, current: "" }).current).toMatch(
      /current password/,
    );
    expect(passwordFormErrors({ ...form, current: "old" })).toEqual({});
    expect(passwordFormErrors({ ...form, current: null })).toEqual({});
  });
});
