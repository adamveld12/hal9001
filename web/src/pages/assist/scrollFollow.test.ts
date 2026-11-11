import { describe, expect, it } from "vitest";
import type { AssistMessage } from "../../api/assist";
import {
  anchorMessage,
  isBelowView,
  pendingApprovalMessage,
  shouldFollow,
} from "./scrollFollow";

const tool = (
  id: string,
  approval: string | null,
  status: string,
): AssistMessage =>
  ({
    id,
    role: "tool",
    approval_id: approval,
    tool_call: { status },
  }) as unknown as AssistMessage;

describe("shouldFollow", () => {
  it("follows near the end and not after scrolling up", () => {
    expect(shouldFollow(1000, 560, 400)).toBe(true);
    expect(shouldFollow(1000, 300, 400)).toBe(false);
  });
});

describe("pendingApprovalMessage", () => {
  it("finds the newest undecided approval", () => {
    const msgs = [
      tool("a", "x", "pending"),
      tool("b", null, "pending"),
      tool("c", "y", "in_progress"),
      tool("d", "z", "completed"),
    ];
    expect(pendingApprovalMessage(msgs)?.id).toBe("c");
    expect(pendingApprovalMessage([tool("d", "z", "completed")])).toBe(
      undefined,
    );
  });
});

describe("isBelowView", () => {
  it("is true only when the item starts past the visible area", () => {
    expect(isBelowView(900, 800)).toBe(true);
    expect(isBelowView(500, 800)).toBe(false);
  });
});

describe("anchorMessage", () => {
  const ids = [
    { id: "run1:00000000" },
    { id: "run1:00000004" },
    { id: "run1:00000009" },
  ];
  it("picks the last message not after the anchor", () => {
    expect(anchorMessage(ids, "run1:00000006")).toBe("run1:00000004");
    expect(anchorMessage(ids, "run1:00000009")).toBe("run1:00000009");
    expect(anchorMessage(ids, "run1:00000099")).toBe("run1:00000009");
  });
  it("finds nothing without an anchor or before every message", () => {
    expect(anchorMessage(ids, null)).toBeUndefined();
    expect(anchorMessage(ids, "")).toBeUndefined();
    expect(anchorMessage(ids, "run0:00000001")).toBeUndefined();
  });
});
