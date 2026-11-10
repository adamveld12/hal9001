-- hal9001 schema v15: sandbox run log. One sandbox_sessions row per sandbox an agent run opened (backend, sandbox
-- id/name, image, repo, status, timing) and the log entries it produced (lifecycle steps, every command and file
-- operation with output, denied calls, backend messages).
--
-- agent_run_id is a plain id, not a foreign key: the sandbox is provisioned before the agent run row is written, and when
-- provisioning fails the agent run never exists. Retention follows the node run (ON DELETE CASCADE), so pruning a flow run
-- removes its sandbox log. Entry payloads are capped by the writer, so they are stored inline (no blob refs).

CREATE TABLE sandbox_sessions (
  id           TEXT PRIMARY KEY,
  node_run_id  TEXT NOT NULL REFERENCES node_runs (id) ON DELETE CASCADE,
  agent_run_id TEXT NOT NULL,
  backend      TEXT NOT NULL,
  status       TEXT NOT NULL CHECK (status IN ('provisioning','ready','failed','closed','interrupted')),
  sandbox_id   TEXT NOT NULL DEFAULT '',
  sandbox_name TEXT NOT NULL DEFAULT '',
  image        TEXT NOT NULL DEFAULT '',
  repo         TEXT NOT NULL DEFAULT '',
  ref          TEXT NOT NULL DEFAULT '',
  lifecycle    TEXT NOT NULL DEFAULT '',
  workdir      TEXT NOT NULL DEFAULT '',
  env_names    TEXT NOT NULL DEFAULT '[]',
  error        TEXT NOT NULL DEFAULT '',
  truncated    INTEGER NOT NULL DEFAULT 0,
  entry_count  INTEGER NOT NULL DEFAULT 0,
  started_at   TEXT NOT NULL,
  ready_at     TEXT,
  ended_at     TEXT
);
CREATE INDEX sandbox_sessions_node ON sandbox_sessions (node_run_id, id);
CREATE INDEX sandbox_sessions_agent ON sandbox_sessions (agent_run_id, id);

CREATE TABLE sandbox_log_entries (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES sandbox_sessions (id) ON DELETE CASCADE,
  ts         TEXT NOT NULL,
  kind       TEXT NOT NULL,
  level      TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  data       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX sandbox_log_entries_session ON sandbox_log_entries (session_id, id);
