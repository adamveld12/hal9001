-- hal9001 schema v18: continuation flow runs. A run of kind 'continuation' is started when the user approves an emit
-- from a chat that continues an agent run: it reuses the original run's upstream results and runs only what follows
-- the emitting agent node. The run records where it came from: the original flow run, the conversation (assist
-- session), the agent node, the emitted port and the approval that started it (all empty for other kinds).
--
-- SQLite cannot relax a CHECK constraint in place, so flow_runs is rebuilt (the migrator runs this with foreign keys
-- off and verifies them with foreign_key_check before it commits). The link columns carry no foreign keys: the
-- original run may be pruned by retention while the continuation run stays.

CREATE TABLE flow_runs_new (
  id                       TEXT PRIMARY KEY,
  flow_id                  TEXT NOT NULL,
  flow_hash                TEXT NOT NULL,
  event_id                 TEXT REFERENCES events (id) ON DELETE RESTRICT,
  kind                     TEXT NOT NULL CHECK (kind IN ('live','replay','test','continuation')),
  replay_of                TEXT REFERENCES flow_runs (id) ON DELETE SET NULL,
  dry_run                  INTEGER NOT NULL DEFAULT 0,
  status                   TEXT NOT NULL CHECK (status IN ('queued','running','awaiting_approval','succeeded','failed','killed','interrupted','cancelled')),
  error                    TEXT NOT NULL DEFAULT '',
  created_at               TEXT NOT NULL,
  started_at               TEXT,
  ended_at                 TEXT,
  cancelled_by             TEXT NOT NULL DEFAULT '',
  continuation_of_run_id   TEXT NOT NULL DEFAULT '',
  continuation_session_id  TEXT NOT NULL DEFAULT '',
  continuation_node_id     TEXT NOT NULL DEFAULT '',
  continuation_port        TEXT NOT NULL DEFAULT '',
  continuation_approval_id TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (flow_id, flow_hash) REFERENCES flow_versions (flow_id, hash) ON DELETE RESTRICT
);
INSERT INTO flow_runs_new (id, flow_id, flow_hash, event_id, kind, replay_of, dry_run, status, error, created_at, started_at, ended_at, cancelled_by)
  SELECT id, flow_id, flow_hash, event_id, kind, replay_of, dry_run, status, error, created_at, started_at, ended_at, cancelled_by FROM flow_runs;
DROP TABLE flow_runs;
ALTER TABLE flow_runs_new RENAME TO flow_runs;
CREATE INDEX flow_runs_flow ON flow_runs (flow_id, id);
CREATE INDEX flow_runs_event ON flow_runs (event_id);
CREATE INDEX flow_runs_status ON flow_runs (status, id);
CREATE INDEX flow_runs_created ON flow_runs (created_at);
CREATE INDEX flow_runs_replay_of ON flow_runs (replay_of);
CREATE INDEX flow_runs_continuation_of ON flow_runs (continuation_of_run_id) WHERE continuation_of_run_id <> '';
CREATE INDEX flow_runs_continuation_session ON flow_runs (continuation_session_id) WHERE continuation_session_id <> '';
