-- hal9001 schema v21: the MCP tool calls a sensor run made.
--
-- A sensor that lists mcp_servers gets a run token, and every `hal9001 mcp call` it makes with it is logged on the run:
-- a JSON array of {server, tool, at, duration_ms, ok, error}.

ALTER TABLE sensor_runs ADD COLUMN mcp_calls TEXT NOT NULL DEFAULT '[]';
