import { describe, expect, it } from "vitest";
import { isExternalHref } from "./links";

const o = "http://localhost:9001";
describe("isExternalHref", () => {
  it.each([
    ["https://github.com/x", true],
    ["http://other.test/a", true],
    ["//cdn.example.com/a", true],
    ["mailto:a@b.co", true],
    ["http://localhost:9001/inbox", false],
    ["http://localhost:9002/", true],
    ["/inbox/1", false],
    ["inbox", false],
    ["#top", false],
    ["", false],
    ["javascript:alert(1)", false],
  ])("%s -> %s", (h, want) => expect(isExternalHref(h, o)).toBe(want));
});
