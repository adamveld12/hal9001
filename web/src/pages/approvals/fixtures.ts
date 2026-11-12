import type { Approval } from "../../api/approvals";

/** A pending flow proposal; tests override what they exercise. */
export function approval(over: Partial<Approval> & { id: string }): Approval {
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
    title: "Title",
    description: "Description",
    description_by: "hal",
    priority: "normal",
    origin: { kind: "flow", label: "flow / node", client: null },
    details: {
      body_md: "",
      fields: [],
      command: null,
      tool: null,
      diffs: [],
      dry_run: false,
    },
    editable: [],
    edits: null,
    conversation: null,
    held: false,
    held_note: null,
    gate: false,
    on_timeout: null,
    permission_request_id: null,
    permission_request: null,
    proposal_md: "# Plan",
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

/** A pending approval of the assistant's `mcp_call` tool: calling a tool of a remote MCP server from a chat. */
export const mcpCallApproval: Approval = approval({
  id: "mcp-call",
  assist_session_id: "01JASSISTSESSION0000000000",
  type: "tool_call",
  title: "Call notion-search on notion",
  description:
    'Call the "notion-search" tool on the MCP server "notion" with arguments {"page_size":10,"query":"roadmap"}',
  origin: { kind: "chat", label: "Chat: Weekly planning", client: null },
  details: {
    body_md: "",
    fields: [
      { key: "server", value: "notion" },
      { key: "tool", value: "notion-search" },
      {
        key: "tool description",
        value:
          "Search the workspace for pages and databases by title or content.",
      },
    ],
    command:
      'notion.notion-search\n{\n  "page_size": 10,\n  "query": "roadmap"\n}',
    tool: "mcp_call",
    diffs: [],
    dry_run: false,
  },
  proposal_md: null,
  conversation: {
    id: "01JASSISTSESSION0000000000",
    kind: "chat",
    anchor: null,
    path: "/chat/01JASSISTSESSION0000000000",
  },
});
