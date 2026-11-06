-- hal9001 schema v7: flow runs and node runs can end as 'cancelled' (removed from the queue).
--
-- flow_runs also gains cancelled_by (the principal who removed the run; empty otherwise). SQLite cannot relax
-- a CHECK constraint in place, so both tables are rebuilt (the migrator runs this with foreign keys off and
-- verifies them with foreign_key_check before it commits).

CREATE TABLE flow_runs_new (
  id           TEXT PRIMARY KEY,
  flow_id      TEXT NOT NULL,
  flow_hash    TEXT NOT NULL,
  event_id     TEXT REFERENCES events (id) ON DELETE RESTRICT,
  kind         TEXT NOT NULL CHECK (kind IN ('live','replay','test')),
  replay_of    TEXT REFERENCES flow_runs (id) ON DELETE SET NULL,
  dry_run      INTEGER NOT NULL DEFAULT 0,
  status       TEXT NOT NULL CHECK (status IN ('queued','running','awaiting_approval','succeeded','failed','killed','interrupted','cancelled')),
  error        TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  started_at   TEXT,
  ended_at     TEXT,
  cancelled_by TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (flow_id, flow_hash) REFERENCES flow_versions (flow_id, hash) ON DELETE RESTRICT
);
INSERT INTO flow_runs_new (id, flow_id, flow_hash, event_id, kind, replay_of, dry_run, status, error, created_at, started_at, ended_at)
  SELECT id, flow_id, flow_hash, event_id, kind, replay_of, dry_run, status, error, created_at, started_at, ended_at FROM flow_runs;
DROP TABLE flow_runs;
ALTER TABLE flow_runs_new RENAME TO flow_runs;
CREATE INDEX flow_runs_flow ON flow_runs (flow_id, id);
CREATE INDEX flow_runs_event ON flow_runs (event_id);
CREATE INDEX flow_runs_status ON flow_runs (status, id);
CREATE INDEX flow_runs_created ON flow_runs (created_at);
CREATE INDEX flow_runs_replay_of ON flow_runs (replay_of);

CREATE TABLE node_runs_new (
  id                 TEXT PRIMARY KEY,
  flow_run_id        TEXT NOT NULL REFERENCES flow_runs (id) ON DELETE CASCADE,
  node_id            TEXT NOT NULL,
  node_type          TEXT NOT NULL CHECK (node_type IN ('command','agent','inbox')),
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
