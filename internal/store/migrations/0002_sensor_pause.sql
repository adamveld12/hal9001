-- hal9001 schema v2: per-sensor pause. A sensor with paused_at set is never
-- polled by the scheduler; paused_by is the principal that paused it.
ALTER TABLE sensor_state ADD COLUMN paused_at TEXT;
ALTER TABLE sensor_state ADD COLUMN paused_by TEXT NOT NULL DEFAULT '';
