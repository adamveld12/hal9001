import { describe, expect, it } from "vitest";
import { faviconFrame, faviconTitle } from "./faviconActivity";

describe("faviconFrame", () => {
  it("stays normal when nothing runs", () => {
    expect(faviconFrame(0, 0, false)).toBe("normal");
    expect(faviconFrame(undefined, 3, false)).toBe("normal");
  });
  it("alternates while running", () => {
    expect(faviconFrame(2, 0, false)).toBe("active");
    expect(faviconFrame(2, 1, false)).toBe("normal");
    expect(faviconFrame(2, 2, false)).toBe("active");
  });
  it("is static active with reduced motion", () => {
    expect(faviconFrame(1, 1, true)).toBe("active");
    expect(faviconFrame(0, 1, true)).toBe("normal");
  });
});

describe("faviconTitle", () => {
  it("prefixes the count only while running", () => {
    expect(faviconTitle("hal9001", 2)).toBe("(2) hal9001");
    expect(faviconTitle("hal9001", 0)).toBe("hal9001");
  });
});
