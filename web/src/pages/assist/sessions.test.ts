import { describe, expect, it } from "vitest";
import {
  clampDrawerWidth,
  DEFAULT_DRAWER_WIDTH,
  isBusy,
  MAX_DRAWER_WIDTH,
  MIN_DRAWER_WIDTH,
  parseStoredWidth,
} from "./sessions";

describe("drawer width", () => {
  it("stays within its limits", () => {
    expect(clampDrawerWidth(100, 1600)).toBe(MIN_DRAWER_WIDTH);
    expect(clampDrawerWidth(5000, 3000)).toBe(MAX_DRAWER_WIDTH);
  });

  it("leaves room for the editor on a narrower window", () => {
    expect(clampDrawerWidth(700, 1000)).toBe(600);
  });

  it("falls back to the default for missing or unusable stored values", () => {
    expect(parseStoredWidth(null)).toBe(DEFAULT_DRAWER_WIDTH);
    expect(parseStoredWidth("abc")).toBe(DEFAULT_DRAWER_WIDTH);
    expect(parseStoredWidth("-5")).toBe(DEFAULT_DRAWER_WIDTH);
    expect(parseStoredWidth("480")).toBe(480);
  });
});

describe("isBusy", () => {
  it("is true while a turn runs or waits on an approval", () => {
    expect(isBusy("running")).toBe(true);
    expect(isBusy("awaiting_approval")).toBe(true);
    expect(isBusy("idle")).toBe(false);
    expect(isBusy("failed")).toBe(false);
  });
});
