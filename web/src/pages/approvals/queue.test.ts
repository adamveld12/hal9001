import { describe, expect, it } from "vitest";
import type { Approval, ApprovalList } from "../../api/approvals";
import {
  approvalTitle,
  describeDecision,
  mergeApprovalPages,
  viewFromParams,
  viewToParams,
  viewToQuery,
  DEFAULT_VIEW,
} from "./queue";

function approval(over: Partial<Approval> & { id: string }): Approval {
  return {
    agent_run_id: "ar",
    node_run_id: "nr",
    flow_run_id: "fr",
    flow_id: "flow",
    node_id: "node",
    assist_session_id: null,
    event_id: null,
    runtime: "claude",
    type: "proposal",
    permission_request_id: null,
    permission_request: null,
    proposal_md: "# Plan",
    request: null,
    status: "pending",
    comment: null,
    cancel_run: null,
    decided_by: null,
    decided_at: null,
    created_at: "2026-11-05T10:00:00Z",
    expires_at: null,
    ...over,
  };
}

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
  it("titles a proposal by its first real line and a tool call by its tool title", () => {
    expect(
      approvalTitle(
        approval({ id: "x", proposal_md: "\n## **Fix** the `build`\nmore" }),
      ),
    ).toBe("Fix the build");
    expect(approvalTitle(approval({ id: "x", proposal_md: null }))).toBe(
      "Proposal",
    );
    expect(
      approvalTitle(approval({ id: "x", proposal_md: "a".repeat(200) })),
    ).toHaveLength(100);
    expect(
      approvalTitle(
        approval({
          id: "x",
          type: "tool_call",
          permission_request: {
            title: "Run go test",
          } as Approval["permission_request"],
        }),
      ),
    ).toBe("Run go test");
  });

  it("describes decisions, including a cancelled run", () => {
    const base = { id: "x", decided_by: "owner" };
    expect(describeDecision(approval({ ...base, status: "approved" }))).toBe(
      "Approved by owner",
    );
    expect(
      describeDecision(
        approval({ ...base, status: "approved", type: "tool_call" }),
      ),
    ).toBe("Allowed by owner");
    expect(
      describeDecision(
        approval({
          ...base,
          status: "denied",
          type: "tool_call",
          cancel_run: true,
        }),
      ),
    ).toBe("Denied by owner, run cancelled");
    expect(describeDecision(approval({ id: "x", status: "expired" }))).toBe(
      "Timed out without a decision",
    );
  });
});

describe("request approvals", () => {
  const request = {
    title: "Ship api?",
    body_md: "details",
    fields: [],
    priority: "high" as const,
    dry_run: false,
    on_timeout: "denied" as const,
  };
  const req = (over: Partial<Approval> = {}) =>
    approval({
      id: "r",
      type: "request",
      agent_run_id: null,
      proposal_md: null,
      request,
      ...over,
    });

  it("titles a row with the request title, marking dry runs", () => {
    expect(approvalTitle(req())).toBe("Ship api?");
    expect(approvalTitle(req({ request: { ...request, dry_run: true } }))).toBe(
      "[dry run] Ship api?",
    );
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
      describeDecision(
        req({
          status: "expired",
          request: { ...request, on_timeout: "error" },
        }),
      ),
    ).toBe("Timed out; the flow continued on the error port");
  });
});
