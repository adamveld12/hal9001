-- hal9001 schema v19: sandbox log of a conversation.
--
-- A chat continued from an agent run (or any assist session with a sandbox) records its sandbox sessions on the chat:
-- assist_session_id is the assist session id, empty for the sandbox of an agent run. node_run_id stays the node run of
-- the original run, so retention still follows it (ON DELETE CASCADE). The agent-run and node-run sandbox logs list
-- only the sessions with an empty assist_session_id; the conversation log lists the others.

ALTER TABLE sandbox_sessions ADD COLUMN assist_session_id TEXT NOT NULL DEFAULT '';
CREATE INDEX sandbox_sessions_assist ON sandbox_sessions (assist_session_id, id);
