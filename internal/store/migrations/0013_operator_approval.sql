-- hal9001 schema v13: operator approvals. A request approval may now have neither an agent run nor a node run: an
-- external MCP client (hal's operator tools, docs/mcp.md) asked to do something with side effects and the user decides in
-- the Inbox. The rule becomes "a request approval has no agent run" (node_run_id is optional); proposal and tool_call
-- approvals still need one. The table is rebuilt (the migrator runs this with foreign keys off and verifies them before
-- it commits).

CREATE TABLE approvals_new (
  id                    TEXT PRIMARY KEY,
  agent_run_id          TEXT REFERENCES agent_runs (id) ON DELETE CASCADE,
  node_run_id           TEXT REFERENCES node_runs (id) ON DELETE CASCADE,
  type                  TEXT NOT NULL CHECK (type IN ('proposal','tool_call','request')),
  permission_request_id TEXT REFERENCES permission_requests (id) ON DELETE CASCADE,
  proposal_text         TEXT NOT NULL DEFAULT '',
  proposal_ref          TEXT NOT NULL DEFAULT '',
  proposal_size         INTEGER NOT NULL DEFAULT 0,
  request               TEXT NOT NULL DEFAULT '',
  status                TEXT NOT NULL CHECK (status IN ('pending','approved','denied','expired')),
  comment               TEXT NOT NULL DEFAULT '',
  decided_by            TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  expires_at            TEXT,
  decided_at            TEXT,
  CHECK (type <> 'tool_call' OR permission_request_id IS NOT NULL),
  CHECK ((type = 'request') = (agent_run_id IS NULL))
);
INSERT INTO approvals_new (id, agent_run_id, node_run_id, type, permission_request_id, proposal_text, proposal_ref, proposal_size, request, status, comment, decided_by, created_at, expires_at, decided_at)
  SELECT id, agent_run_id, node_run_id, type, permission_request_id, proposal_text, proposal_ref, proposal_size, request, status, comment, decided_by, created_at, expires_at, decided_at FROM approvals;
DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;
CREATE INDEX approvals_status ON approvals (status, id);
CREATE INDEX approvals_run ON approvals (agent_run_id);
CREATE INDEX approvals_node ON approvals (node_run_id);
