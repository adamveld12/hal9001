import { describe, expect, it } from "vitest";
import { conversationApprovalsQuery } from "../../api/approvals";
import {
  agentWritten,
  cardActions,
  conversationPath,
  decisionFor,
  detailSummary,
  inConversation,
  originKindLabel,
  originText,
  reasonText,
  requestPath,
  unlinkedApprovals,
} from "./card";
import { approval } from "./fixtures";

const tool = approval({ id: "t", type: "tool_call" });

describe("cardActions", () => {
  it("offers approve, deny and steering on a pending proposal, no stop", () => {
    expect(cardActions(approval({ id: "p" }))).toEqual({
      approve: true,
      deny: true,
      denyStop: false,
      steering: true,
    });
  });

  it("adds Deny + stop for tool calls only", () => {
    expect(cardActions(tool).denyStop).toBe(true);
    expect(cardActions(approval({ id: "r", type: "request" })).denyStop).toBe(
      false,
    );
  });

  it("hides the steering box when held, keeps the actions", () => {
    const a = cardActions({ ...tool, held: true });
    expect(a.steering).toBe(false);
    expect(a.approve && a.deny && a.denyStop).toBe(true);
  });

  it("offers nothing once decided", () => {
    expect(cardActions({ ...tool, status: "denied" })).toEqual({
      approve: false,
      deny: false,
      denyStop: false,
      steering: false,
    });
  });
});

describe("decisionFor", () => {
  it("sends the steering text with Deny, trimmed", () => {
    expect(decisionFor(tool, "deny", "  use make  ")).toEqual({
      action: "deny",
      comment: "use make",
    });
  });

  it("sends no comment for an empty box", () => {
    expect(decisionFor(tool, "deny", "  ")).toEqual({ action: "deny" });
  });

  it("never sends steering with Approve", () => {
    expect(decisionFor(tool, "approve", "use make")).toEqual({
      action: "approve",
    });
  });

  it("shares the box with Deny + stop on a tool call", () => {
    expect(decisionFor(tool, "deny_stop", "stop it")).toEqual({
      action: "deny",
      comment: "stop it",
      cancelRun: true,
    });
  });

  it("refuses Deny + stop for other types", () => {
    expect(decisionFor(approval({ id: "p" }), "deny_stop", "")).toBeNull();
  });

  it("drops the steering of a held approval", () => {
    expect(decisionFor({ ...tool, held: true }, "deny", "text")).toEqual({
      action: "deny",
    });
  });

  it("does nothing for a decided approval", () => {
    expect(decisionFor({ ...tool, status: "approved" }, "deny", "")).toBeNull();
  });
});

describe("origin", () => {
  const o = (
    kind: "flow" | "chat" | "continued_chat" | "mcp",
    label = "",
    client: string | null = null,
  ) => ({
    kind,
    label,
    client,
  });

  it("names every kind explicitly", () => {
    expect(originKindLabel(o("flow"))).toBe("Flow");
    expect(originKindLabel(o("chat"))).toBe("Chat");
    expect(originKindLabel(o("continued_chat"))).toBe("Continued chat");
    expect(originKindLabel(o("mcp", "", "claude-code"))).toBe(
      "MCP: claude-code",
    );
    expect(originKindLabel(o("mcp"))).toBe("MCP client");
  });

  it("adds the server label without repeating the kind", () => {
    expect(originText(o("flow", "reviews / triage"))).toBe(
      "Flow · reviews / triage",
    );
    expect(originText(o("chat", "Chat: Fix build"))).toBe("Chat: Fix build");
    expect(originText(o("mcp", "MCP: claude-code", "claude-code"))).toBe(
      "MCP: claude-code",
    );
    expect(originText(o("chat"))).toBe("Chat");
  });
});

describe("conversationPath", () => {
  const conversation = {
    id: "s1",
    kind: "chat" as const,
    anchor: "m1",
    path: "/chat/s1?message=m1",
  };

  it("links to the conversation path", () => {
    expect(conversationPath(approval({ id: "a", conversation }))).toBe(
      "/chat/s1?message=m1",
    );
  });

  it("has no link without a conversation or for an MCP approval", () => {
    expect(conversationPath(approval({ id: "a" }))).toBeNull();
    expect(
      conversationPath(
        approval({
          id: "a",
          conversation,
          origin: { kind: "mcp", label: "", client: "x" },
        }),
      ),
    ).toBeNull();
  });
});

describe("requestPath", () => {
  it("links an MCP approval to its request instead of a conversation", () => {
    const mcp = approval({
      id: "a1",
      origin: { kind: "mcp", label: "MCP: claude", client: "claude" },
    });
    expect(requestPath(mcp)).toBe("/inbox/approval/a1");
    expect(requestPath(approval({ id: "a2" }))).toBeNull();
  });
});

describe("description and details", () => {
  it("labels only agent-written descriptions", () => {
    expect(agentWritten(approval({ id: "a", description_by: "agent" }))).toBe(
      true,
    );
    expect(agentWritten(approval({ id: "a" }))).toBe(false);
  });

  it("summarises the command, else the first field, on one line", () => {
    const d = (over: object) => ({
      body_md: "",
      fields: [],
      command: null,
      tool: null,
      diffs: [],
      dry_run: false,
      ...over,
    });
    expect(
      detailSummary(
        approval({ id: "a", details: d({ command: "npm test\n--watch" }) }),
      ),
    ).toBe("npm test");
    expect(
      detailSummary(
        approval({
          id: "a",
          details: d({ fields: [{ key: "repo", value: "o/r" }] }),
        }),
      ),
    ).toBe("repo: o/r");
    expect(detailSummary(approval({ id: "a" }))).toBeNull();
  });
});

describe("conversation approvals", () => {
  it("filters by conversation and pending status", () => {
    expect(conversationApprovalsQuery("s1")).toEqual({
      conversation_id: "s1",
      status: ["pending"],
    });
  });

  it("leaves out approvals a message already shows", () => {
    const items = [approval({ id: "a" }), approval({ id: "b" })];
    expect(unlinkedApprovals(items, new Set(["a"])).map((x) => x.id)).toEqual([
      "b",
    ]);
  });
});

describe("inConversation", () => {
  const a = approval({
    id: "a",
    conversation: {
      id: "s1",
      kind: "chat",
      anchor: "m1",
      path: "/chat/s1?message=m1",
    },
  });
  it("is true only on the conversation's own page", () => {
    expect(inConversation(a, "/chat/s1")).toBe(true);
    expect(inConversation(a, "/chat/s2")).toBe(false);
    expect(inConversation(a, "/inbox")).toBe(false);
  });
});

describe("reasonText", () => {
  const withReason = (
    reason: string,
    command: string | null,
    status: "pending" | "approved" = "pending",
  ) =>
    approval({
      id: "t",
      type: "tool_call",
      status,
      details: {
        body_md: "",
        fields: [],
        command,
        tool: null,
        diffs: [],
        dry_run: false,
      },
      permission_request: { reason } as never,
    });
  it("shows a pending reason that adds something", () => {
    expect(reasonText(withReason("Emit a result to flow f.", null))).toBe(
      "Emit a result to flow f.",
    );
  });
  it("hides a reason that only restates the command, and decided ones", () => {
    expect(
      reasonText(withReason("run a shell command:\n\nnpm test", "npm test")),
    ).toBeNull();
    expect(
      reasonText(withReason("approved by user", null, "approved")),
    ).toBeNull();
  });
});
