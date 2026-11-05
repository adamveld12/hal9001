-- hal9001 schema v3: AI-assisted sensor authoring (assist sessions).
--
-- An assist session is a chat with an agent that edits a draft sensor file. Every turn of the
-- conversation is an agent run, so agent_runs gains assist_session_id and node_run_id becomes
-- nullable (a turn belongs to a session, not to a flow's node run). SQLite cannot relax a NOT NULL
-- column in place, so the table is rebuilt (the migrator runs this with foreign keys off and
-- verifies them with foreign_key_check before it commits).

CREATE TABLE assist_sessions (
  id                TEXT PRIMARY KEY,
  target_kind       TEXT NOT NULL CHECK (target_kind IN ('sensor')),
  target_id         TEXT NOT NULL DEFAULT '',
  runtime           TEXT NOT NULL,
  model             TEXT NOT NULL DEFAULT '',
  status            TEXT NOT NULL CHECK (status IN ('idle','running','awaiting_approval','failed','closed')),
  error             TEXT NOT NULL DEFAULT '',
  draft_yaml        TEXT NOT NULL DEFAULT '',
  draft_hash        TEXT NOT NULL,
  draft_sensor_id   TEXT NOT NULL DEFAULT '',
  draft_diagnostics TEXT NOT NULL DEFAULT '[]',
  draft_updated_at  TEXT NOT NULL,
  draft_updated_by  TEXT NOT NULL CHECK (draft_updated_by IN ('agent','user')),
  last_agent_run_id TEXT NOT NULL DEFAULT '',
  acp_session_id    TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  closed_at         TEXT
);
CREATE INDEX assist_sessions_target ON assist_sessions (target_kind, target_id, id);
CREATE INDEX assist_sessions_status ON assist_sessions (status, id);

CREATE TABLE assist_messages (
  id            TEXT PRIMARY KEY,
  session_id    TEXT NOT NULL REFERENCES assist_sessions (id) ON DELETE CASCADE,
  role          TEXT NOT NULL CHECK (role IN ('user','assistant','tool','system')),
  text          TEXT,
  agent_run_id  TEXT NOT NULL DEFAULT '',
  tool_call_id  TEXT NOT NULL DEFAULT '',
  tool_call     TEXT,
  approval_id   TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);
CREATE INDEX assist_messages_session ON assist_messages (session_id, id);

CREATE TABLE agent_runs_new (
  id                 TEXT PRIMARY KEY,
  node_run_id        TEXT REFERENCES node_runs (id) ON DELETE CASCADE,
  assist_session_id  TEXT REFERENCES assist_sessions (id) ON DELETE CASCADE,
  prev_agent_run_id  TEXT,
  runtime            TEXT NOT NULL,
  agent_info         TEXT NOT NULL DEFAULT 'null',
  capabilities       TEXT NOT NULL DEFAULT 'null',
  mode               TEXT NOT NULL CHECK (mode IN ('auto','approve','readonly')),
  approve_style      TEXT CHECK (approve_style IS NULL OR approve_style IN ('proposal','per_tool')),
  policy             TEXT NOT NULL CHECK (policy IN ('readonly','gated','auto')),
  phase              TEXT NOT NULL CHECK (phase IN ('propose','execute')),
  model              TEXT NOT NULL DEFAULT '',
  session_id         TEXT NOT NULL DEFAULT '',
  resumed_via        TEXT CHECK (resumed_via IS NULL OR resumed_via IN ('load','new_session')),
  cwd                TEXT NOT NULL DEFAULT '',
  worktree_path      TEXT NOT NULL DEFAULT '',
  prompt_text        TEXT NOT NULL DEFAULT '',
  prompt_ref         TEXT NOT NULL DEFAULT '',
  prompt_size        INTEGER NOT NULL DEFAULT 0,
  argv               TEXT NOT NULL DEFAULT '[]',
  status             TEXT NOT NULL CHECK (status IN ('queued','running','awaiting_approval','succeeded','failed','denied','killed','interrupted','timeout')),
  stop_reason        TEXT NOT NULL DEFAULT '',
  error              TEXT NOT NULL DEFAULT '',
  result_text        TEXT NOT NULL DEFAULT '',
  result_ref         TEXT NOT NULL DEFAULT '',
  result_size        INTEGER NOT NULL DEFAULT 0,
  raw_jsonrpc_text   TEXT NOT NULL DEFAULT '',
  raw_jsonrpc_ref    TEXT NOT NULL DEFAULT '',
  raw_jsonrpc_size   INTEGER NOT NULL DEFAULT 0,
  created_at         TEXT NOT NULL,
  started_at         TEXT,
  ended_at           TEXT,
  CHECK (node_run_id IS NOT NULL OR assist_session_id IS NOT NULL)
);
INSERT INTO agent_runs_new (id, node_run_id, prev_agent_run_id, runtime, agent_info, capabilities, mode, approve_style, policy, phase, model,
    session_id, resumed_via, cwd, worktree_path, prompt_text, prompt_ref, prompt_size, argv, status, stop_reason, error,
    result_text, result_ref, result_size, raw_jsonrpc_text, raw_jsonrpc_ref, raw_jsonrpc_size, created_at, started_at, ended_at)
  SELECT id, node_run_id, prev_agent_run_id, runtime, agent_info, capabilities, mode, approve_style, policy, phase, model,
    session_id, resumed_via, cwd, worktree_path, prompt_text, prompt_ref, prompt_size, argv, status, stop_reason, error,
    result_text, result_ref, result_size, raw_jsonrpc_text, raw_jsonrpc_ref, raw_jsonrpc_size, created_at, started_at, ended_at
  FROM agent_runs;
DROP TABLE agent_runs;
ALTER TABLE agent_runs_new RENAME TO agent_runs;
CREATE INDEX agent_runs_node_run ON agent_runs (node_run_id, id);
CREATE INDEX agent_runs_status ON agent_runs (status, id);
CREATE INDEX agent_runs_assist ON agent_runs (assist_session_id, id);
