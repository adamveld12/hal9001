import { describe, expect, it } from "vitest";
import {
  byteLength,
  MAX_SYSTEM_PROMPT_BYTES,
  systemPromptError,
} from "./contextForm";

describe("systemPromptError", () => {
  it("counts UTF-8 bytes, not characters", () => {
    expect(byteLength("é")).toBe(2);
    expect(
      systemPromptError("x".repeat(MAX_SYSTEM_PROMPT_BYTES)),
    ).toBeUndefined();
    expect(
      systemPromptError("é".repeat(MAX_SYSTEM_PROMPT_BYTES / 2 + 1)),
    ).toMatch(/over the 16384 byte limit/);
  });
});
