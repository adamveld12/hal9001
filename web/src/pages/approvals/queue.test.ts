import { describe, expect, it } from "vitest";
import type { Approval, ApprovalList } from "../../api/approvals";
import { approval } from "./fixtures";
import {
  approvalOrigin,
  approvalTitle,
  decisionNotice,
  describeDecision,
  isGateApproval,
  mergeApprovalPages,
  viewFromParams,
  viewToParams,
  viewToQuery,
  DEFAULT_VIEW,
} from "./queue";

const page = (...items: Approval[]): ApprovalList => ({
  items,
  next_cursor: null,
});

describe("pending queue order", () => {
  it("puts approvals with the nearest deadline first, then the rest oldest first", () => {
    const soon = approval({
      id: "soon",
      type: "tool_call",
      expires_at: "2026-11-05T12:05:00Z",
      created_at: "2026-11-05T11:00:00Z",
    });
    const later = approval({
      id: "later",
      type: "tool_call",
      expires_at: "2026-11-05T12:30:00Z",
      created_at: "2026-11-05T09:00:00Z",
    });
    const oldProposal = approval({
      id: "old",
      created_at: "2026-11-05T08:00:00Z",
    });
    const newProposal = approval({
      id: "new",
      created_at: "2026-11-05T11:30:00Z",
    });
    const out = mergeApprovalPages(
      [page(newProposal, later), page(oldProposal, soon)],
      "pending",
    );
    expect(out.map((a) => a.id)).toEqual(["soon", "later", "old", "new"]);
  });

  it("lists an approval once when it shifted between pages", () => {
    const a = approval({ id: "a" });
    const b = approval({ id: "b" });
    const out = mergeApprovalPages([page(a, b), page(a)], "pending");
    expect(out.map((x) => x.id)).toEqual(["a", "b"]);
  });
});

describe("history", () => {
  it("lists the latest decision first", () => {
    const first = approval({
      id: "1",
      status: "approved",
      decided_at: "2026-11-05T09:00:00Z",
    });
    const second = approval({
      id: "2",
      status: "denied",
      decided_at: "2026-11-05T11:00:00Z",
    });
    const expired = approval({
      id: "4",
      status: "expired",
      decided_at: null,
      created_at: "2026-11-05T10:00:00Z",
    });
    const out = mergeApprovalPages([page(first, second, expired)], "history");
    expect(out.map((a) => a.id)).toEqual(["2", "4", "1"]);
  });
});

describe("view <-> URL <-> query", () => {
  it("keeps the default view's URL empty and round-trips history filters", () => {
    expect(viewToParams(DEFAULT_VIEW).toString()).toBe("");
    const v = {
      tab: "history" as const,
      status: "denied" as const,
      type: "tool_call" as const,
    };
    expect(viewFromParams(viewToParams(v))).toEqual(v);
  });

  it("ignores unknown values", () => {
    expect(
      viewFromParams(new URLSearchParams("tab=archive&status=pending&type=x")),
    ).toEqual(DEFAULT_VIEW);
  });

  it("asks the API for pending only on the queue; history is open or narrowed", () => {
    expect(viewToQuery(DEFAULT_VIEW)).toEqual({ status: ["pending"] });
    expect(viewToQuery({ tab: "history", status: null, type: null })).toEqual({
      status: ["approved", "denied", "expired"],
    });
    expect(
      viewToQuery({ tab: "history", status: "expired", type: "proposal" }),
    ).toEqual({ status: ["expired"], type: "proposal" });
  });

  it("does not carry a history outcome into the queue URL", () => {
    expect(
      viewToParams({
        tab: "pending",
        status: "approved",
        type: null,
      }).toString(),
    ).toBe("");
  });
});

describe("labels", () => {
  it("titles a row with the approval's own title", () => {
    expect(approvalTitle(approval({ id: "x", title: "Run go test" }))).toBe(
      "Run go test",
    );
  });

  it("describes decisions, including steering and a stopped run", () => {
    const base = { id: "x", decided_by: "owner" };
    expect(describeDecision(approval({ ...base, status: "approved" }))).toBe(
      "Approved by owner",
    );
    expect(
      describeDecision(
        approval({ ...base, status: "approved", type: "tool_call" }),
      ),
    ).toBe("Approved by owner");
    expect(
      describeDecision(
        approval({
          ...base,
          status: "denied",
          type: "tool_call",
          cancel_run: true,
        }),
      ),
    ).toBe("Denied by owner, run stopped");
    expect(
      describeDecision(
        approval({ ...base, status: "denied", comment: "use make" }),
      ),
    ).toBe("Denied by owner (steering sent)");
    expect(describeDecision(approval({ id: "x", status: "expired" }))).toBe(
      "Timed out without a decision",
    );
  });

  it("confirms a steered denial", () => {
    expect(decisionNotice(approval({ id: "x", status: "denied" }))).toBe(
      "Denied.",
    );
    expect(
      decisionNotice(
        approval({ id: "x", status: "denied", comment: "use make" }),
      ),
    ).toBe("Denied. Your steering was sent.");
    expect(
      decisionNotice(approval({ id: "x", status: "denied", cancel_run: true })),
    ).toBe("Denied. The run was stopped.");
  });
});

describe("request approvals", () => {
  const req = (over: Partial<Approval> = {}) =>
    approval({
      id: "r",
      type: "request",
      agent_run_id: null,
      proposal_md: null,
      on_timeout: "denied",
      ...over,
    });

  it("accepts request as a type filter", () => {
    const v = viewFromParams(new URLSearchParams("type=request"));
    expect(v.type).toBe("request");
    expect(viewToQuery(v).type).toBe("request");
  });

  it("says where a timed-out request went", () => {
    expect(describeDecision(req({ status: "expired" }))).toBe(
      "Timed out; the flow continued on port denied",
    );
    expect(
      describeDecision(req({ status: "expired", on_timeout: "error" })),
    ).toBe("Timed out; the flow continued on the error port");
  });

  it("treats an expired node gate as a denial", () => {
    const gate = req({
      status: "expired",
      gate: true,
      on_timeout: null,
      expires_at: "2026-11-05T11:00:00Z",
    });
    expect(isGateApproval(gate)).toBe(true);
    expect(isGateApproval(req())).toBe(false);
    expect(describeDecision(gate)).toBe(
      "Timed out; the node was denied and did not run",
    );
  });

  it("says what a decided gate did to its node", () => {
    const gate = (status: "approved" | "denied") =>
      req({ status, decided_by: "owner", gate: true, on_timeout: null });
    expect(describeDecision(gate("denied"))).toBe(
      "Denied by owner; the node did not run",
    );
    expect(decisionNotice(gate("approved"))).toBe(
      "Approved. The node will run.",
    );
    expect(decisionNotice(gate("denied"))).toBe(
      "Denied. The node did not run.",
    );
  });

  it("tells an MCP client's timeout apart by origin, not by empty ids", () => {
    const mcp = req({
      status: "expired",
      origin: { kind: "mcp", label: "MCP: claude-code", client: "claude-code" },
    });
    expect(describeDecision(mcp)).toBe("Timed out; the MCP client was told");
  });
});

describe("approvalOrigin", () => {
  it("reads the explicit origin", () => {
    expect(approvalOrigin(approval({ id: "a" }))).toBe("Flow · flow / node");
    expect(
      approvalOrigin(
        approval({
          id: "b",
          origin: {
            kind: "mcp",
            label: "MCP: claude-code",
            client: "claude-code",
          },
        }),
      ),
    ).toBe("MCP: claude-code");
  });
});
