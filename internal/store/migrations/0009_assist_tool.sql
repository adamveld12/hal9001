-- hal9001 schema v9: assist sessions can target a custom tool.
--
-- target_kind admits 'tool'. SQLite cannot relax a CHECK constraint in place, so the table is rebuilt (the
-- migrator runs this with foreign keys off and verifies them with foreign_key_check before it commits).

CREATE TABLE assist_sessions_new (
  id                TEXT PRIMARY KEY,
  target_kind       TEXT NOT NULL CHECK (target_kind IN ('sensor','flow','tool')),
  target_id         TEXT NOT NULL DEFAULT '',
  runtime           TEXT NOT NULL,
  model             TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL CHECK (status IN ('idle','running','awaiting_approval','failed','closed')),
  error             TEXT NOT NULL DEFAULT '',
  draft_yaml        TEXT NOT NULL DEFAULT '',
  draft_hash        TEXT NOT NULL,
  draft_id          TEXT NOT NULL DEFAULT '',
  draft_diagnostics TEXT NOT NULL DEFAULT '[]',
  draft_prompts     TEXT NOT NULL DEFAULT '[]',
  draft_updated_at  TEXT NOT NULL,
  draft_updated_by  TEXT NOT NULL CHECK (draft_updated_by IN ('agent','user')),
  last_agent_run_id TEXT NOT NULL DEFAULT '',
  acp_session_id    TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  closed_at         TEXT,
  approved_script      TEXT NOT NULL DEFAULT '',
  approved_script_hash TEXT NOT NULL DEFAULT ''
);
INSERT INTO assist_sessions_new (id, target_kind, target_id, runtime, model, status, error, draft_yaml, draft_hash, draft_id, draft_diagnostics,
    draft_prompts, draft_updated_at, draft_updated_by, last_agent_run_id, acp_session_id, created_at, updated_at, closed_at, approved_script, approved_script_hash)
  SELECT id, target_kind, target_id, runtime, model, status, error, draft_yaml, draft_hash, draft_id, draft_diagnostics,
    draft_prompts, draft_updated_at, draft_updated_by, last_agent_run_id, acp_session_id, created_at, updated_at, closed_at, approved_script, approved_script_hash
  FROM assist_sessions;
DROP TABLE assist_sessions;
ALTER TABLE assist_sessions_new RENAME TO assist_sessions;
CREATE INDEX assist_sessions_target ON assist_sessions (target_kind, target_id, id);
CREATE INDEX assist_sessions_status ON assist_sessions (status, id);
