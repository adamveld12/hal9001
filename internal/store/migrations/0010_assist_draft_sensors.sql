-- hal9001 schema v10: the sensors an agent drafted in a flow assist session ({id, yaml, exists, base_hash, diagnostics}).
ALTER TABLE assist_sessions ADD COLUMN draft_sensors TEXT NOT NULL DEFAULT '[]';
