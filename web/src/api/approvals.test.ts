import { describe, expect, it } from "vitest";
import { ApiError, type Problem } from "./client";
import {
  isAlreadyDecided,
  replaceApprovalInPages,
  type Approval,
  type ApprovalList,
} from "./approvals";

const mk = (id: string, status: Approval["status"] = "pending") =>
  ({ id, status }) as Approval;

const page = (...items: Approval[]): ApprovalList => ({
  items,
  next_cursor: null,
});

describe("replaceApprovalInPages", () => {
  const decided = mk("a", "approved");

  it("removes a decided approval from the pending queue", () => {
    const out = replaceApprovalInPages([page(mk("a"), mk("b"))], decided, {
      status: ["pending"],
    });
    expect(out[0]?.items.map((i) => i.id)).toEqual(["b"]);
  });

  it("updates it in place where the list is not limited to pending", () => {
    const out = replaceApprovalInPages([page(mk("a"), mk("b"))], decided, {});
    expect(out[0]?.items.map((i) => i.status)).toEqual(["approved", "pending"]);
    const narrowed = replaceApprovalInPages([page(mk("a"))], decided, {
      status: ["approved"],
    });
    expect(narrowed[0]?.items).toHaveLength(1);
  });
});

describe("isAlreadyDecided", () => {
  it("recognises a 409 invalid-state problem and nothing else", () => {
    const conflict = new ApiError(
      409,
      {
        type: "urn:hal9001:problem:invalid-state",
        title: "x",
        status: 409,
      } as Problem,
      "409",
    );
    const other = new ApiError(
      409,
      {
        type: "urn:hal9001:problem:conflict",
        title: "x",
        status: 409,
      } as Problem,
      "409",
    );
    expect(isAlreadyDecided(conflict)).toBe(true);
    expect(isAlreadyDecided(other)).toBe(false);
    expect(isAlreadyDecided(new Error("boom"))).toBe(false);
  });
});
