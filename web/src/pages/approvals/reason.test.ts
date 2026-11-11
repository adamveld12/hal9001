import { describe, expect, it } from "vitest";
import { clipReason } from "./reason";

describe("clipReason", () => {
  it("keeps a short reason", () => {
    expect(clipReason("a\nb", 3)).toEqual({ text: "a\nb", clipped: false });
  });
  it("clips after the line limit", () => {
    expect(clipReason("a\nb\nc\nd", 2)).toEqual({
      text: "a\nb",
      clipped: true,
    });
  });
});
