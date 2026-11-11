-- hal9001 schema v17: conversations (assistant chats and agent runs in one list) and forks of agent runs.
--
-- A fork is an assist session continued from the agent run of a flow node: forked_from_agent_run_id names that run (a
-- plain id, no foreign key: the run may be pruned before the chat) and fork_context says how the first turn restored
-- its context ('' until then): 'resumed' (session/load) or 'replayed' (a compacted transcript in the first prompt).
-- assist_messages.kind marks entries that are not ordinary chat messages: 'divider' (the "continued from run" line)
-- and 'notice' (a system notice such as "the sandbox was recreated").

ALTER TABLE assist_sessions ADD COLUMN forked_from_agent_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE assist_sessions ADD COLUMN fork_context TEXT NOT NULL DEFAULT '' CHECK (fork_context IN ('','resumed','replayed'));
ALTER TABLE assist_messages ADD COLUMN kind TEXT NOT NULL DEFAULT '' CHECK (kind IN ('','divider','notice'));

-- The conversation list is ordered by last activity and paged with a keyset cursor (activity, id).
CREATE INDEX assist_sessions_updated ON assist_sessions (updated_at, id);
-- Forks of one agent run (fork_count, the forks list, the forked_from filter).
CREATE INDEX assist_sessions_forked_from ON assist_sessions (forked_from_agent_run_id);
-- A propose-phase run that has a successor belongs to the successor's conversation: the list asks whether a run is
-- the predecessor of another.
CREATE INDEX agent_runs_prev ON agent_runs (prev_agent_run_id) WHERE prev_agent_run_id IS NOT NULL;
