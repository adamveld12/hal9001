import { describe, expect, it } from "vitest";
import { formatCountdown, timeLeft } from "./timeLeft";

const now = Date.parse("2026-11-05T12:00:00Z");
const at = (seconds: number) => new Date(now + seconds * 1000).toISOString();

describe("formatCountdown", () => {
  it("shows minutes and seconds, rounding partial seconds up", () => {
    expect(formatCountdown(42_000)).toBe("0:42");
    expect(formatCountdown(41_001)).toBe("0:42");
    expect(formatCountdown(725_000)).toBe("12:05");
    expect(formatCountdown(1)).toBe("0:01");
  });

  it("switches to hours past an hour", () => {
    expect(formatCountdown(3_720_000)).toBe("1h 02m");
  });

  it("reads expired at or below zero", () => {
    expect(formatCountdown(0)).toBe("expired");
    expect(formatCountdown(-5000)).toBe("expired");
  });
});

describe("timeLeft", () => {
  it("has no countdown without a deadline or with an unreadable one", () => {
    expect(timeLeft(null, now)).toBeNull();
    expect(timeLeft("not a time", now)).toBeNull();
  });

  it("counts down to the deadline", () => {
    const left = timeLeft(at(90), now);
    expect(left).toMatchObject({ ms: 90_000, expired: false, label: "1:30" });
  });

  it("is expired exactly at the deadline and after it, never negative", () => {
    for (const s of [0, -1, -3600]) {
      expect(timeLeft(at(s), now)).toMatchObject({
        ms: 0,
        expired: true,
        label: "expired",
        tone: "bad",
      });
    }
  });

  it("raises urgency as the deadline nears", () => {
    expect(timeLeft(at(30 * 60), now)?.tone).toBe("muted");
    expect(timeLeft(at(4 * 60), now)?.tone).toBe("warn");
    expect(timeLeft(at(59), now)?.tone).toBe("bad");
  });
});
