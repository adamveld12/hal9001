-- hal9001 schema v6: a sensor's dedup state can be reset (Reset dedup state).
-- reset_at is when it was last reset and reset_by the principal that did it.
ALTER TABLE sensor_state ADD COLUMN reset_at TEXT;
ALTER TABLE sensor_state ADD COLUMN reset_by TEXT NOT NULL DEFAULT '';
