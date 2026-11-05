import { describe, expect, it } from "vitest";
import {
  durationBetween,
  formatBytes,
  formatDuration,
  relativeTime,
  shortId,
} from "./format";

describe("format", () => {
  const now = Date.parse("2026-11-05T12:00:00Z");

  it("describes past and future times relative to now", () => {
    expect(relativeTime("2026-11-05T11:59:58Z", now)).toBe("just now");
    expect(relativeTime("2026-11-05T11:59:30Z", now)).toBe("30s ago");
    expect(relativeTime("2026-11-05T11:15:00Z", now)).toBe("45m ago");
    expect(relativeTime("2026-11-05T09:00:00Z", now)).toBe("3h ago");
    expect(relativeTime("2026-11-02T12:00:00Z", now)).toBe("3d ago");
    expect(relativeTime("2026-11-05T12:10:00Z", now)).toBe("in 10m");
    expect(relativeTime(null, now)).toBe("–");
  });

  it("formats durations across units", () => {
    expect(formatDuration(850)).toBe("850ms");
    expect(formatDuration(2400)).toBe("2.4s");
    expect(formatDuration(45_000)).toBe("45s");
    expect(formatDuration(185_000)).toBe("3m 05s");
    expect(formatDuration(7_800_000)).toBe("2h 10m");
  });

  it("measures a running span up to now", () => {
    expect(durationBetween("2026-11-05T11:59:00Z", null, now)).toBe("1m 00s");
    expect(
      durationBetween("2026-11-05T11:59:00Z", "2026-11-05T11:59:03Z", now),
    ).toBe("3.0s");
    expect(durationBetween(null, null, now)).toBe("–");
  });

  it("formats byte sizes and ids", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(4096)).toBe("4.0 KiB");
    expect(shortId("01JC7Z3K9Q8V5T2M4N6P8R0S1T")).toBe("N6P8R0S1T".slice(1));
  });
});
