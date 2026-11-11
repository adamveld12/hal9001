import { describe, expect, it } from "vitest";
import { fuzzyMatch } from "./fuzzy";

const score = (q: string, t: string) => fuzzyMatch(q, t)?.score ?? null;

describe("fuzzyMatch", () => {
  it("needs the characters in order", () => {
    expect(fuzzyMatch("abc", "a-b-c")).not.toBeNull();
    expect(fuzzyMatch("cba", "abc")).toBeNull();
    expect(fuzzyMatch("xyz", "Go to Inbox")).toBeNull();
  });

  it("ignores case and spaces in the query", () => {
    expect(fuzzyMatch("GO TO", "Go to Inbox")?.indices).toEqual([0, 1, 3, 4]);
  });

  it("reports where the characters matched", () => {
    expect(fuzzyMatch("nf", "New flow")?.indices).toEqual([0, 4]);
  });

  it("ranks a prefix above a scattered match", () => {
    expect(score("flow", "Flows")).toBeGreaterThan(
      score("flow", "Stop all running flows") ?? 0,
    );
  });

  it("prefers word starts and consecutive characters", () => {
    expect(score("gi", "Go to Inbox")).toBeGreaterThan(
      score("gi", "Agent runs big") ?? -Infinity,
    );
    expect(score("ins", "Inbox settings")).toBeGreaterThan(
      score("ins", "Check for updates in sensors") ?? -Infinity,
    );
  });

  it("ranks an exact match first", () => {
    expect(score("chat", "Chat")).toBeGreaterThan(
      score("chat", "Chatter") ?? 0,
    );
  });

  it("ranks an exact match above a longer title that starts with it, spaces ignored", () => {
    expect(score("new sensor", "New sensor")).toBeGreaterThan(
      score("new sensor", "New sensor with AI…") ?? 0,
    );
    expect(score("new sensor", "New sensor with AI…")).toBeGreaterThan(
      score("new sensor", "Go to Settings: Sensors, newest") ?? -Infinity,
    );
  });

  it("matches everything for an empty query", () => {
    expect(fuzzyMatch("", "anything")).toEqual({ score: 0, indices: [] });
  });
});
