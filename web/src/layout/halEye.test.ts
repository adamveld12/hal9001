import { expect, test } from "vitest";
import { eyeLabel, eyeThrobs } from "./halEye";

test("eye throbs only while flows run", () => {
  expect(eyeThrobs(undefined)).toBe(false);
  expect(eyeThrobs(0)).toBe(false);
  expect(eyeThrobs(2)).toBe(true);
  expect(eyeLabel(1)).toBe("1 flow running");
  expect(eyeLabel(2)).toBe("2 flows running");
  expect(eyeLabel(0)).toBe("hal9001");
});
