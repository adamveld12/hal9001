-- hal9001 schema v10: the approval flow node. Node runs may be of type 'approval', and approvals gain type
-- 'request': one is raised by a flow node (node_run_id, request = the rendered title, body, fields and priority as
-- JSON) rather than by an agent run, so agent_run_id becomes optional. Both tables are rebuilt (the migrator runs
-- this with foreign keys off and verifies them before it commits).

CREATE TABLE node_runs_new (
  id                 TEXT PRIMARY KEY,
  flow_run_id        TEXT NOT NULL REFERENCES flow_runs (id) ON DELETE CASCADE,
  node_id            TEXT NOT NULL,
  node_type          TEXT NOT NULL CHECK (node_type IN ('command','agent','inbox','approval')),
  parent_node_run_id TEXT REFERENCES node_runs (id) ON DELETE CASCADE,
  status             TEXT NOT NULL CHECK (status IN ('queued','running','awaiting_approval','done','failed','killed','interrupted','skipped','cancelled')),
  decision           TEXT CHECK (decision IS NULL OR decision IN ('emitted','dropped','error','skipped')),
  reason             TEXT NOT NULL DEFAULT '',
  ports              TEXT NOT NULL DEFAULT '[]',
  output_count       INTEGER NOT NULL DEFAULT 0,
  lang               TEXT NOT NULL DEFAULT '',
  script             TEXT NOT NULL DEFAULT '',
  argv               TEXT NOT NULL DEFAULT '[]',
  env_keys           TEXT NOT NULL DEFAULT '[]',
  stdin_text         TEXT NOT NULL DEFAULT '',
  stdin_ref          TEXT NOT NULL DEFAULT '',
  stdin_size         INTEGER NOT NULL DEFAULT 0,
  stdout_text        TEXT NOT NULL DEFAULT '',
  stdout_ref         TEXT NOT NULL DEFAULT '',
  stdout_size        INTEGER NOT NULL DEFAULT 0,
  stderr_text        TEXT NOT NULL DEFAULT '',
  stderr_ref         TEXT NOT NULL DEFAULT '',
  stderr_size        INTEGER NOT NULL DEFAULT 0,
  exit_code          INTEGER,
  error              TEXT NOT NULL DEFAULT '',
  queued_at          TEXT NOT NULL,
  started_at         TEXT,
  ended_at           TEXT,
  duration_ms        INTEGER
);
INSERT INTO node_runs_new (id, flow_run_id, node_id, node_type, parent_node_run_id, status, decision, reason, ports, output_count, lang, script, argv, env_keys,
    stdin_text, stdin_ref, stdin_size, stdout_text, stdout_ref, stdout_size, stderr_text, stderr_ref, stderr_size, exit_code, error, queued_at, started_at, ended_at, duration_ms)
  SELECT id, flow_run_id, node_id, node_type, parent_node_run_id, status, decision, reason, ports, output_count, lang, script, argv, env_keys,
    stdin_text, stdin_ref, stdin_size, stdout_text, stdout_ref, stdout_size, stderr_text, stderr_ref, stderr_size, exit_code, error, queued_at, started_at, ended_at, duration_ms
  FROM node_runs;
DROP TABLE node_runs;
ALTER TABLE node_runs_new RENAME TO node_runs;
CREATE INDEX node_runs_flow_run ON node_runs (flow_run_id, id);
CREATE INDEX node_runs_parent ON node_runs (parent_node_run_id);
CREATE INDEX node_runs_status ON node_runs (status, node_type, id);
CREATE INDEX node_runs_node ON node_runs (node_id, id);

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
  CHECK ((type = 'request') = (agent_run_id IS NULL AND node_run_id IS NOT NULL))
);
INSERT INTO approvals_new (id, agent_run_id, type, permission_request_id, proposal_text, proposal_ref, proposal_size, status, comment, decided_by, created_at, expires_at, decided_at)
  SELECT id, agent_run_id, type, permission_request_id, proposal_text, proposal_ref, proposal_size, status, comment, decided_by, created_at, expires_at, decided_at FROM approvals;
DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;
CREATE INDEX approvals_status ON approvals (status, id);
CREATE INDEX approvals_run ON approvals (agent_run_id);
CREATE INDEX approvals_node ON approvals (node_run_id);
