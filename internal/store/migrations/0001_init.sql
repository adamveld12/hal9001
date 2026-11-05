-- hal9001 schema v1. See the package doc in doc.go for invariants.
--
-- Conventions
--   * ids are ULID strings (sortable by creation time).
--   * timestamps are fixed-width UTC text (2006-01-02T15:04:05.000000000Z) so
--     lexicographic order equals chronological order.
--   * "content" columns come as a triple <name>_text / <name>_ref / <name>_size:
--     inline text, or a blob ref when the payload exceeded 64 KiB (text is then '').
--   * booleans are INTEGER 0/1; JSON values are TEXT.

CREATE TABLE sensor_runs (
  id           TEXT PRIMARY KEY,
  sensor_id    TEXT NOT NULL,
  origin       TEXT NOT NULL DEFAULT 'schedule' CHECK (origin IN ('schedule','manual','inject')),
  status       TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','interrupted')),
  baseline     INTEGER NOT NULL DEFAULT 0,
  started_at   TEXT NOT NULL,
  ended_at     TEXT,
  duration_ms  INTEGER,
  exit_code    INTEGER,
  error        TEXT NOT NULL DEFAULT '',
  item_count   INTEGER NOT NULL DEFAULT 0,
  new_count    INTEGER NOT NULL DEFAULT 0,
  stdout_text  TEXT NOT NULL DEFAULT '',
  stdout_ref   TEXT NOT NULL DEFAULT '',
  stdout_size  INTEGER NOT NULL DEFAULT 0,
  stderr_text  TEXT NOT NULL DEFAULT '',
  stderr_ref   TEXT NOT NULL DEFAULT '',
  stderr_size  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX sensor_runs_sensor ON sensor_runs (sensor_id, id);
CREATE INDEX sensor_runs_started ON sensor_runs (started_at);

CREATE TABLE sensor_state (
  sensor_id            TEXT PRIMARY KEY,
  last_run_id          TEXT NOT NULL DEFAULT '',
  last_run_at          TEXT,
  last_success_at      TEXT,
  last_error           TEXT NOT NULL DEFAULT '',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  updated_at           TEXT NOT NULL
);

-- Dedup keys outlive events: events are pruned at retention_days, seen_keys at
-- dedup_retention_days, so old items do not re-fire after their event is gone.
CREATE TABLE seen_keys (
  sensor_id  TEXT NOT NULL,
  dedup_key  TEXT NOT NULL,
  first_seen TEXT NOT NULL,
  PRIMARY KEY (sensor_id, dedup_key)
) WITHOUT ROWID;
CREATE INDEX seen_keys_first_seen ON seen_keys (first_seen);

CREATE TABLE events (
  id            TEXT PRIMARY KEY,
  sensor_id     TEXT NOT NULL,
  sensor_run_id TEXT REFERENCES sensor_runs (id) ON DELETE SET NULL,
  dedup_key     TEXT NOT NULL,
  payload_text  TEXT NOT NULL DEFAULT '',
  payload_ref   TEXT NOT NULL DEFAULT '',
  payload_size  INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL
);
CREATE INDEX events_sensor ON events (sensor_id, id);
CREATE INDEX events_created ON events (created_at);
CREATE INDEX events_sensor_run ON events (sensor_run_id);

CREATE TABLE flow_versions (
  flow_id    TEXT NOT NULL,
  hash       TEXT NOT NULL,
  yaml       TEXT NOT NULL,
  first_seen TEXT NOT NULL,
  PRIMARY KEY (flow_id, hash)
) WITHOUT ROWID;

CREATE TABLE flow_runs (
  id         TEXT PRIMARY KEY,
  flow_id    TEXT NOT NULL,
  flow_hash  TEXT NOT NULL,
  event_id   TEXT REFERENCES events (id) ON DELETE RESTRICT,
  kind       TEXT NOT NULL CHECK (kind IN ('live','replay','test')),
  replay_of  TEXT REFERENCES flow_runs (id) ON DELETE SET NULL,
  dry_run    INTEGER NOT NULL DEFAULT 0,
  status     TEXT NOT NULL CHECK (status IN ('queued','running','awaiting_approval','succeeded','failed','killed','interrupted')),
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  ended_at   TEXT,
  FOREIGN KEY (flow_id, flow_hash) REFERENCES flow_versions (flow_id, hash) ON DELETE RESTRICT
);
CREATE INDEX flow_runs_flow ON flow_runs (flow_id, id);
CREATE INDEX flow_runs_event ON flow_runs (event_id);
CREATE INDEX flow_runs_status ON flow_runs (status, id);
CREATE INDEX flow_runs_created ON flow_runs (created_at);
CREATE INDEX flow_runs_replay_of ON flow_runs (replay_of);

CREATE TABLE node_runs (
  id                 TEXT PRIMARY KEY,
  flow_run_id        TEXT NOT NULL REFERENCES flow_runs (id) ON DELETE CASCADE,
  node_id            TEXT NOT NULL,
  node_type          TEXT NOT NULL CHECK (node_type IN ('command','agent','inbox')),
  parent_node_run_id TEXT REFERENCES node_runs (id) ON DELETE CASCADE,
  status             TEXT NOT NULL CHECK (status IN ('queued','running','awaiting_approval','done','failed','killed','interrupted','skipped')),
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
CREATE INDEX node_runs_flow_run ON node_runs (flow_run_id, id);
CREATE INDEX node_runs_parent ON node_runs (parent_node_run_id);
CREATE INDEX node_runs_status ON node_runs (status, node_type, id);
CREATE INDEX node_runs_node ON node_runs (node_id, id);

CREATE TABLE agent_runs (
  id                 TEXT PRIMARY KEY,
  node_run_id        TEXT NOT NULL REFERENCES node_runs (id) ON DELETE CASCADE,
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
  ended_at           TEXT
);
CREATE INDEX agent_runs_node_run ON agent_runs (node_run_id, id);
CREATE INDEX agent_runs_status ON agent_runs (status, id);

CREATE TABLE agent_events (
  agent_run_id TEXT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
  seq          INTEGER NOT NULL,
  ts           TEXT NOT NULL,
  kind         TEXT NOT NULL,
  tool_call_id TEXT NOT NULL DEFAULT '',
  payload_text TEXT NOT NULL DEFAULT '',
  payload_ref  TEXT NOT NULL DEFAULT '',
  payload_size INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (agent_run_id, seq)
) WITHOUT ROWID;

CREATE TABLE permission_requests (
  id            TEXT PRIMARY KEY,
  agent_run_id  TEXT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
  tool_call_id  TEXT NOT NULL DEFAULT '',
  tool_kind     TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '',
  raw_input_text TEXT NOT NULL DEFAULT '',
  raw_input_ref  TEXT NOT NULL DEFAULT '',
  raw_input_size INTEGER NOT NULL DEFAULT 0,
  options       TEXT NOT NULL DEFAULT 'null',
  decision      TEXT CHECK (decision IS NULL OR decision IN ('allow','reject','cancelled','expired')),
  decided_by    TEXT CHECK (decided_by IS NULL OR decided_by IN ('policy','user','timeout')),
  matched_rule  TEXT NOT NULL DEFAULT '',
  reason        TEXT NOT NULL DEFAULT '',
  requested_at  TEXT NOT NULL,
  decided_at    TEXT
);
CREATE INDEX permission_requests_run ON permission_requests (agent_run_id, id);

CREATE TABLE approvals (
  id                    TEXT PRIMARY KEY,
  agent_run_id          TEXT NOT NULL REFERENCES agent_runs (id) ON DELETE CASCADE,
  type                  TEXT NOT NULL CHECK (type IN ('proposal','tool_call')),
  permission_request_id TEXT REFERENCES permission_requests (id) ON DELETE CASCADE,
  proposal_text         TEXT NOT NULL DEFAULT '',
  proposal_ref          TEXT NOT NULL DEFAULT '',
  proposal_size         INTEGER NOT NULL DEFAULT 0,
  status                TEXT NOT NULL CHECK (status IN ('pending','approved','denied','expired')),
  comment               TEXT NOT NULL DEFAULT '',
  decided_by            TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  expires_at            TEXT,
  decided_at            TEXT,
  CHECK (type = 'proposal' OR permission_request_id IS NOT NULL)
);
CREATE INDEX approvals_status ON approvals (status, id);
CREATE INDEX approvals_run ON approvals (agent_run_id);

-- Inbox links to its source by plain ids (no foreign keys) so it can outlive
-- pruned runs; starred items protect their runs from pruning.
CREATE TABLE inbox_items (
  id              TEXT PRIMARY KEY,
  source_type     TEXT NOT NULL CHECK (source_type IN ('agent_run','node_run')),
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
  created_at      TEXT NOT NULL
);
CREATE INDEX inbox_items_starred ON inbox_items (starred, flow_run_id);
CREATE INDEX inbox_items_created ON inbox_items (created_at);
CREATE INDEX inbox_items_flow_run ON inbox_items (flow_run_id);

CREATE TABLE auth_events (
  id          TEXT PRIMARY KEY,
  ts          TEXT NOT NULL,
  action      TEXT NOT NULL DEFAULT '',
  service     TEXT NOT NULL DEFAULT '',
  endpoint    TEXT NOT NULL DEFAULT '',
  results     TEXT NOT NULL DEFAULT '[]',
  outcome     TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  principal   TEXT NOT NULL DEFAULT '',
  remote_addr TEXT NOT NULL DEFAULT '',
  host        TEXT NOT NULL DEFAULT '',
  session_id  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX auth_events_ts ON auth_events (ts);

-- id is the SHA-256 (hex) of the session token; the raw token is never stored.
CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  principal    TEXT NOT NULL,
  modules      TEXT NOT NULL DEFAULT '[]',
  services     TEXT NOT NULL DEFAULT '[]',
  host         TEXT NOT NULL DEFAULT '',
  remote_addr  TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  revoked_at   TEXT
);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE webauthn_credentials (
  id           TEXT PRIMARY KEY,
  rp_id        TEXT NOT NULL,
  principal    TEXT NOT NULL DEFAULT '',
  public_key   BLOB NOT NULL,
  sign_count   INTEGER NOT NULL DEFAULT 0,
  label        TEXT NOT NULL DEFAULT '',
  data         TEXT NOT NULL DEFAULT 'null',
  created_at   TEXT NOT NULL,
  last_used_at TEXT
);
CREATE INDEX webauthn_credentials_rp ON webauthn_credentials (rp_id);

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
) WITHOUT ROWID;
