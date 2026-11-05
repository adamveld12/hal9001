import { describe, expect, it } from "vitest";
import {
  rowDisplay,
  saveInput,
  secretForKey,
  toggleInput,
  type EnvKeyRow,
} from "./envView";

const secretRow: EnvKeyRow = { key: "TOKEN", secret: true, value: null };
const plainRow: EnvKeyRow = { key: "HOST", secret: false, value: "imap.x.io" };

describe("toggleInput", () => {
  it("flips the flag without sending a value", () => {
    expect(toggleInput(secretRow)).toEqual({ key: "TOKEN", secret: false });
    expect(toggleInput(plainRow)).toEqual({ key: "HOST", secret: true });
    expect("value" in toggleInput(secretRow)).toBe(false);
  });
});

describe("saveInput", () => {
  it("always sends secret explicitly, including false", () => {
    expect(saveInput("HOST", "v", false)).toEqual({
      key: "HOST",
      value: "v",
      secret: false,
    });
    expect(saveInput("TOKEN", "v", true).secret).toBe(true);
  });
});

describe("secretForKey", () => {
  const rows = [secretRow, plainRow];
  it("follows the existing key's state on a name match", () => {
    expect(secretForKey(rows, "HOST", true)).toBe(false);
    expect(secretForKey(rows, "TOKEN", false)).toBe(true);
  });
  it("keeps the fallback for a new name", () => {
    expect(secretForKey(rows, "NEW", true)).toBe(true);
    expect(secretForKey(rows, "NEW", false)).toBe(false);
  });
});

describe("rowDisplay", () => {
  it("hides secret values", () => {
    expect(rowDisplay(secretRow)).toEqual({ kind: "secret" });
  });
  it("shows plain values, including an empty one", () => {
    expect(rowDisplay(plainRow)).toEqual({
      kind: "plain",
      text: "imap.x.io",
      empty: false,
    });
    expect(rowDisplay({ ...plainRow, value: "" })).toMatchObject({
      kind: "plain",
      empty: true,
    });
  });
  it("never shows a value for a secret row even if one is present", () => {
    expect(rowDisplay({ ...secretRow, value: "leak" })).toEqual({
      kind: "secret",
    });
  });
});
