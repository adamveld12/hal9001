-- hal9001 schema v21: inbox actions. A note written by an agent can carry questions and actions (JSON, validated against
-- the note schema before they get here) and a reply box. The first interaction with a note forks the agent's
-- conversation once; conversation_id is that fork (an assist session id, no foreign key: sessions are pruned on their
-- own schedule). Every interaction (a batch of answers, an action, a reply) is a row of inbox_interactions with the
-- message that was sent to the fork and the state of the turn it started.

ALTER TABLE inbox_items ADD COLUMN questions TEXT NOT NULL DEFAULT '[]';
ALTER TABLE inbox_items ADD COLUMN actions TEXT NOT NULL DEFAULT '[]';
ALTER TABLE inbox_items ADD COLUMN conversation_id TEXT NOT NULL DEFAULT '';

CREATE TABLE inbox_interactions (
  id              TEXT PRIMARY KEY,
  item_id         TEXT NOT NULL REFERENCES inbox_items(id) ON DELETE CASCADE,
  conversation_id TEXT NOT NULL,
  kind            TEXT NOT NULL CHECK (kind IN ('answers','action','reply')),
  -- action: the index into the note's actions (an action runs once per note).
  action_index    INTEGER,
  -- answers: JSON array of {question_id, choices, other}.
  answers         TEXT NOT NULL DEFAULT '[]',
  -- message is the exact text sent to the fork.
  message         TEXT NOT NULL,
  status          TEXT NOT NULL CHECK (status IN ('running','waiting_for_approval','done','failed')),
  approval_id     TEXT NOT NULL DEFAULT '',
  result          TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL
);
CREATE INDEX inbox_interactions_item ON inbox_interactions (item_id, created_at);
CREATE INDEX inbox_interactions_conversation ON inbox_interactions (conversation_id, status);
CREATE UNIQUE INDEX inbox_interactions_action ON inbox_interactions (item_id, action_index) WHERE kind = 'action';
