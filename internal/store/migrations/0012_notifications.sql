-- hal9001 schema v12: the notification log. Every notification the daemon raises is stored with the channel that
-- delivered it (app, terminal-notifier, none) and the delivery status.

CREATE TABLE notifications (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN ('note','proposal','tool_call','request')),
  flow_id       TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '',
  subtitle      TEXT NOT NULL DEFAULT '',
  body          TEXT NOT NULL DEFAULT '',
  priority      TEXT NOT NULL CHECK (priority IN ('low','normal','high','urgent')),
  dry_run       INTEGER NOT NULL DEFAULT 0,
  thread_id     TEXT NOT NULL DEFAULT '',
  url           TEXT NOT NULL DEFAULT '',
  item_id       TEXT NOT NULL DEFAULT '',
  approval_id   TEXT NOT NULL DEFAULT '',
  approval_type TEXT NOT NULL DEFAULT '',
  editable      INTEGER NOT NULL DEFAULT 0,
  channel       TEXT NOT NULL CHECK (channel IN ('app','terminal-notifier','none')),
  status        TEXT NOT NULL CHECK (status IN ('sent','no_sink','failed','logged')),
  error         TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);
CREATE INDEX notifications_flow ON notifications (flow_id, id);
