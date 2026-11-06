import { describe, expect, it } from "vitest";
import type { AssistMessage } from "../../api/assist";
import {
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
