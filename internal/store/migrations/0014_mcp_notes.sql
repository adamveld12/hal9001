-- hal9001 schema v14: notes from external MCP clients. An inbox item may now come from an MCP client's send_note call
-- (docs/mcp.md): source_type 'mcp', no run ids. `client` is the client's name (from its initialize clientInfo, or its
-- User-Agent) and `thread_key` the optional grouping key it passed. The table is rebuilt because SQLite cannot relax a
-- CHECK in place (the migrator runs this with foreign keys off and verifies them before it commits).

CREATE TABLE inbox_items_new (
  id              TEXT PRIMARY KEY,
  source_type     TEXT NOT NULL CHECK (source_type IN ('agent_run','node_run','mcp')),
  flow_run_id     TEXT NOT NULL DEFAULT '',
  node_run_id     TEXT NOT NULL DEFAULT '',
  agent_run_id    TEXT NOT NULL DEFAULT '',
  title           TEXT NOT NULL,
  priority        TEXT NOT NULL CHECK (priority IN ('low','normal','high','urgent')),
  summary_md      TEXT NOT NULL DEFAULT '',
  suggested_reply TEXT NOT NULL DEFAULT '',
  links           TEXT NOT NULL DEFAULT '[]',
  parse_error     INTEGER NOT NULL DEFAULT 0,
  is_read         INTEGER NOT NULL DEFAULT 0,
  starred         INTEGER NOT NULL DEFAULT 0,
  archived        INTEGER NOT NULL DEFAULT 0,
  dry_run         INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT NOT NULL,
  client          TEXT NOT NULL DEFAULT '',
  thread_key      TEXT NOT NULL DEFAULT ''
);
INSERT INTO inbox_items_new (id, source_type, flow_run_id, node_run_id, agent_run_id, title, priority, summary_md, suggested_reply, links, parse_error, is_read, starred, archived, dry_run, created_at)
  SELECT id, source_type, flow_run_id, node_run_id, agent_run_id, title, priority, summary_md, suggested_reply, links, parse_error, is_read, starred, archived, dry_run, created_at FROM inbox_items;
DROP TABLE inbox_items;
ALTER TABLE inbox_items_new RENAME TO inbox_items;
CREATE INDEX inbox_items_starred ON inbox_items (starred, flow_run_id);
CREATE INDEX inbox_items_created ON inbox_items (created_at);
CREATE INDEX inbox_items_flow_run ON inbox_items (flow_run_id);
