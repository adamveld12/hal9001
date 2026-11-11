import { describe, expect, it } from "vitest";
import { DEFAULT_ORIGIN, rememberOrigin, safeOrigin } from "./origin";

describe("rememberOrigin", () => {
  it("follows every page that is not a chat page", () => {
    expect(
      rememberOrigin(null, { pathname: "/flows/triage", search: "?x=1" }),
    ).toBe("/flows/triage?x=1");
  });

  it("keeps the page the user came from while on the Chat page", () => {
    expect(
      rememberOrigin("/runs/abc", { pathname: "/chat/01JC", search: "?q=a" }),
    ).toBe("/runs/abc");
    expect(rememberOrigin(null, { pathname: "/chat", search: "" })).toBeNull();
  });
});

describe("safeOrigin", () => {
  it("returns an in-app page", () => {
    expect(safeOrigin("/timeline?status=failed")).toBe(
      "/timeline?status=failed",
    );
  });

  it("falls back to the inbox for nothing, a chat page or a path that leaves the app", () => {
    expect(safeOrigin(null)).toBe(DEFAULT_ORIGIN);
    expect(safeOrigin("/chat/01JC")).toBe(DEFAULT_ORIGIN);
    expect(safeOrigin("/chat?q=x")).toBe(DEFAULT_ORIGIN);
    expect(safeOrigin("//evil.example")).toBe(DEFAULT_ORIGIN);
    expect(safeOrigin("https://evil.example")).toBe(DEFAULT_ORIGIN);
    expect(safeOrigin("/\\evil")).toBe(DEFAULT_ORIGIN);
  });
});
