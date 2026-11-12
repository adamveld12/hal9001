import type { RemoteMcpServerDetail } from "../../api/mcpServers";

/**
 * Fake servers for rendering every state of Settings → Context → MCP servers
 * (screenshots, manual checks). Nothing here is real: names, URLs and tools are
 * made up and no value is a secret.
 *
 * The approval fixture for the assistant's `mcp_call` is `mcpCallApproval` in
 * `pages/approvals/fixtures.ts`.
 */

const NESTED_SEARCH_SCHEMA = {
  type: "object",
  properties: {
    query: { type: "string", description: "Text to search for." },
    filter: {
      type: "object",
      description: "Narrow the search.",
      properties: {
        created_by: { type: "array", items: { type: "string" } },
        created_time: {
          type: "object",
          properties: {
            after: { type: "string", format: "date-time" },
            before: { type: "string", format: "date-time" },
          },
        },
        object: { type: "string", enum: ["page", "database"] },
      },
    },
    page_size: { type: "integer", minimum: 1, maximum: 100, default: 25 },
  },
  required: ["query"],
};

export const connectedNotion: RemoteMcpServerDetail = {
  name: "notion",
  url: "https://mcp.notion.example/mcp",
  auth: "oauth",
  scopes: [],
  has_client_secret: false,
  has_api_key: false,
  status: "connected",
  tool_count: 4,
  tools_refreshed_at: "2026-11-11T09:30:00Z",
  token_expires_at: "2026-11-11T19:30:00Z",
  server_name: "Notion MCP",
  server_version: "1.4.2",
  transport: "streamable_http",
  tools: [
    {
      name: "notion-search",
      title: "Search",
      description:
        "Search the workspace for pages and databases by title or content. Results are ranked by relevance and recency. Use the filter to restrict the search to pages created by particular people or inside a time window; combine it with page_size to page through large result sets. Returns the page or database ID, title, URL and a short excerpt of the matching text. Searching does not modify anything and is safe to call repeatedly, but results can lag behind very recent edits by up to a minute.",
      input_schema: NESTED_SEARCH_SCHEMA,
      output_schema: {
        type: "object",
        properties: {
          results: {
            type: "array",
            items: {
              type: "object",
              properties: {
                id: { type: "string" },
                title: { type: "string" },
                url: { type: "string" },
              },
            },
          },
        },
      },
    },
    {
      name: "notion-fetch",
      title: "Fetch page",
      description: "Retrieve a page or database by ID or URL.",
      input_schema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "notion-create-pages",
      description: "Create one or more pages under a parent page or database.",
      input_schema: {
        type: "object",
        properties: {
          parent: {
            type: "object",
            properties: {
              page_id: { type: "string" },
              database_id: { type: "string" },
            },
          },
          pages: {
            type: "array",
            items: {
              type: "object",
              properties: {
                title: { type: "string" },
                content: { type: "string" },
              },
              required: ["title"],
            },
          },
        },
        required: ["pages"],
      },
    },
    {
      name: "notion-update-page",
      title: "Update page",
      description: "Update properties or content of an existing page.",
      input_schema: {
        type: "object",
        properties: {
          page_id: { type: "string" },
          properties: { type: "object", additionalProperties: true },
        },
        required: ["page_id"],
      },
    },
  ],
};

export const needsReconnectLinear: RemoteMcpServerDetail = {
  name: "linear",
  url: "https://mcp.linear.example/mcp",
  auth: "oauth",
  scopes: ["read", "write"],
  has_client_secret: false,
  has_api_key: false,
  status: "needs_reconnect",
  error:
    "The server rejected the stored credentials (401 invalid_token) and refreshing them failed.",
  error_at: "2026-11-11T08:12:00Z",
  tool_count: 1,
  tools_refreshed_at: "2026-11-10T16:00:00Z",
  token_expires_at: "2026-11-11T08:00:00Z",
  server_name: "Linear",
  server_version: "0.9.0",
  transport: "streamable_http",
  tools: [
    {
      name: "list_issues",
      description: "List issues assigned to a user or in a team.",
      input_schema: {
        type: "object",
        properties: { assignee: { type: "string" } },
      },
    },
  ],
};

export const disconnectedOAuth: RemoteMcpServerDetail = {
  name: "calendar",
  url: "https://calendar.example/mcp",
  auth: "oauth_client",
  client_id: "hal9001-local",
  scopes: [],
  has_client_secret: true,
  has_api_key: false,
  status: "disconnected",
  tool_count: 0,
  tools: [],
};

export const apiKeyServer: RemoteMcpServerDetail = {
  name: "docs",
  url: "https://docs.example/mcp",
  auth: "header",
  header: "Authorization",
  scopes: [],
  has_client_secret: false,
  has_api_key: true,
  status: "unknown",
  tool_count: 0,
  tools: [],
};

export const errorServer: RemoteMcpServerDetail = {
  name: "local-dev",
  url: "http://localhost:3333/mcp",
  auth: "none",
  scopes: [],
  has_client_secret: false,
  has_api_key: false,
  status: "error",
  error: "dial tcp 127.0.0.1:3333: connect: connection refused",
  error_at: "2026-11-11T09:45:00Z",
  tool_count: 0,
  tools: [],
};

export const mcpServerFixtures: readonly RemoteMcpServerDetail[] = [
  connectedNotion,
  needsReconnectLinear,
  disconnectedOAuth,
  apiKeyServer,
  errorServer,
];
