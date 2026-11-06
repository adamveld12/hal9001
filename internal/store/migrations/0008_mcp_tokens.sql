-- hal9001 schema v8: capability tokens of hal's per-run MCP server (hash only; the token itself is never stored).
-- A token is scoped to one agent run (or one assist session's agent process) and revoked when it ends.
CREATE TABLE mcp_tokens (
  token_hash   TEXT PRIMARY KEY,
  agent_run_id TEXT NOT NULL,
  kind         TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  revoked_at   TEXT
);
CREATE INDEX mcp_tokens_run ON mcp_tokens (agent_run_id);
