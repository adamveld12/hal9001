-- hal9001 schema v20: one shape for every approval. An approval gets a title, a required description (hal-written or
-- agent-written), a priority, an explicit origin (flow, chat, continued_chat or mcp) and, when an agent run raised it,
-- the conversation it belongs to (chat session or flow agent run) with the message it was raised at. What will
-- actually happen (fields, command, diffs, markdown) moves into the details JSON. The request JSON column keeps only
-- the request machinery: editable, edits, on_timeout, gate, detached, principal.
--
-- A request approval may now have an agent run (an agent asked through request_approval), so the check that tied
-- type = 'request' to a missing agent run goes; proposal and tool_call approvals still need one. The table is rebuilt
-- (the migrator runs this with foreign keys off and verifies them before it commits) and the existing rows are
-- backfilled:
--   description  tool_call: the permission request's reason while the approval is pending (a decided one has the
--                decision text there), else its title; proposal: the first non-empty line, markdown stripped;
--                request: the first line of the body, else the title; a gate: "Run node <node> (<type>) of flow <flow>".
--   origin       a request approval with source mcp: mcp; a request approval of a node: flow; an agent run of an assist
--                session: chat; any other agent run: flow.
--   conversation chat: the assist session (anchor: the message that shows the approval); flow: the agent run.

CREATE TABLE approvals_new (
  id                    TEXT PRIMARY KEY,
  agent_run_id          TEXT REFERENCES agent_runs (id) ON DELETE CASCADE,
  node_run_id           TEXT REFERENCES node_runs (id) ON DELETE CASCADE,
  type                  TEXT NOT NULL CHECK (type IN ('proposal','tool_call','request')),
  permission_request_id TEXT REFERENCES permission_requests (id) ON DELETE CASCADE,
  title                 TEXT NOT NULL DEFAULT '',
  description           TEXT NOT NULL DEFAULT '',
  description_by        TEXT NOT NULL DEFAULT 'hal' CHECK (description_by IN ('hal','agent')),
  priority              TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
  origin                TEXT NOT NULL DEFAULT 'flow' CHECK (origin IN ('flow','chat','continued_chat','mcp')),
  origin_label          TEXT NOT NULL DEFAULT '',
  origin_client         TEXT NOT NULL DEFAULT '',
  conversation_id       TEXT NOT NULL DEFAULT '',
  conversation_kind     TEXT NOT NULL DEFAULT '' CHECK (conversation_kind IN ('','chat','flow')),
  anchor                TEXT NOT NULL DEFAULT '',
  held_note             TEXT NOT NULL DEFAULT '',
  details               TEXT NOT NULL DEFAULT '{}',
  proposal_text         TEXT NOT NULL DEFAULT '',
  proposal_ref          TEXT NOT NULL DEFAULT '',
  proposal_size         INTEGER NOT NULL DEFAULT 0,
  request               TEXT NOT NULL DEFAULT '',
  status                TEXT NOT NULL CHECK (status IN ('pending','approved','denied','expired')),
  comment               TEXT NOT NULL DEFAULT '',
  decided_by            TEXT NOT NULL DEFAULT '',
  created_at            TEXT NOT NULL,
  expires_at            TEXT,
  decided_at            TEXT,
  CHECK (type <> 'tool_call' OR permission_request_id IS NOT NULL),
  CHECK (type = 'request' OR agent_run_id IS NOT NULL)
);

INSERT INTO approvals_new (id, agent_run_id, node_run_id, type, permission_request_id, title, description, description_by, priority,
    origin, origin_label, origin_client, conversation_id, conversation_kind, anchor, held_note, details,
    proposal_text, proposal_ref, proposal_size, request, status, comment, decided_by, created_at, expires_at, decided_at)
WITH src AS (
  SELECT a.*,
    CASE WHEN a.type = 'request' AND json_valid(a.request) THEN a.request ELSE '{}' END AS rq,
    ar.assist_session_id AS session_id,
    nr.node_id AS node_id, nr.node_type AS node_type, fr.flow_id AS flow_id, COALESCE(fr.dry_run, 0) AS flow_dry,
    s.title AS session_title,
    pr.title AS perm_title, pr.tool_kind AS perm_kind, pr.reason AS perm_reason,
    CASE WHEN json_valid(pr.raw_input_text) THEN pr.raw_input_text ELSE '{}' END AS perm_raw
  FROM approvals a
  LEFT JOIN agent_runs ar ON ar.id = a.agent_run_id
  LEFT JOIN node_runs nr ON nr.id = COALESCE(a.node_run_id, ar.node_run_id)
  LEFT JOIN flow_runs fr ON fr.id = nr.flow_run_id
  LEFT JOIN assist_sessions s ON s.id = ar.assist_session_id
  LEFT JOIN permission_requests pr ON pr.id = a.permission_request_id
), body AS (
  SELECT src.*,
    ltrim(CASE src.type WHEN 'proposal' THEN src.proposal_text WHEN 'request' THEN COALESCE(json_extract(src.rq, '$.body_md'), '') ELSE '' END,
          ' ' || char(9) || char(10) || char(13)) AS lead
  FROM src
), line AS (
  SELECT body.*,
    trim(ltrim(replace(replace(replace(replace(
      CASE WHEN instr(body.lead, char(10)) > 0 THEN substr(body.lead, 1, instr(body.lead, char(10)) - 1) ELSE body.lead END,
      '**', ''), '__', ''), '`', ''), char(13), ''), '#>*-+ ')) AS first_line
  FROM body
)
SELECT id, agent_run_id, node_run_id, type, permission_request_id,
  -- title
  CASE type
    WHEN 'tool_call' THEN COALESCE(NULLIF(trim(perm_title), ''), 'Tool call')
    WHEN 'proposal' THEN substr(COALESCE(NULLIF(first_line, ''), 'Proposal'), 1, 600)
    ELSE COALESCE(NULLIF(trim(json_extract(rq, '$.title')), ''), 'Approval request')
  END,
  -- description
  substr(CASE
    WHEN type = 'tool_call' AND status = 'pending' THEN COALESCE(NULLIF(trim(perm_reason), ''), NULLIF(trim(perm_title), ''), 'Tool call')
    WHEN type = 'tool_call' THEN COALESCE(NULLIF(trim(perm_title), ''), 'Tool call')
    WHEN type = 'proposal' THEN COALESCE(NULLIF(first_line, ''), 'Proposal')
    WHEN json_extract(rq, '$.gate') AND node_id IS NOT NULL AND flow_id IS NOT NULL THEN 'Run node ' || node_id || ' (' || node_type || ') of flow ' || flow_id
    ELSE COALESCE(NULLIF(first_line, ''), NULLIF(trim(json_extract(rq, '$.title')), ''), 'Approval request')
  END, 1, 600),
  'hal',
  CASE type
    WHEN 'tool_call' THEN 'high'
    WHEN 'request' THEN COALESCE(NULLIF(json_extract(rq, '$.priority'), ''), 'normal')
    ELSE 'normal'
  END,
  -- origin
  CASE
    WHEN type = 'request' AND json_extract(rq, '$.source') = 'mcp' THEN 'mcp'
    WHEN type = 'request' THEN 'flow'
    WHEN session_id IS NOT NULL THEN 'chat'
    ELSE 'flow'
  END,
  CASE
    WHEN type = 'request' AND json_extract(rq, '$.source') = 'mcp' THEN 'MCP: ' || COALESCE(NULLIF(json_extract(rq, '$.client'), ''), 'client')
    WHEN type <> 'request' AND session_id IS NOT NULL THEN 'Chat: ' || COALESCE(NULLIF(session_title, ''), 'untitled')
    WHEN flow_id IS NOT NULL AND node_id IS NOT NULL THEN flow_id || ' / ' || node_id
    ELSE ''
  END,
  CASE WHEN type = 'request' THEN COALESCE(json_extract(rq, '$.client'), '') ELSE '' END,
  -- conversation
  CASE WHEN type = 'request' THEN '' WHEN session_id IS NOT NULL THEN session_id ELSE agent_run_id END,
  CASE WHEN type = 'request' THEN '' WHEN session_id IS NOT NULL THEN 'chat' ELSE 'flow' END,
  CASE WHEN type <> 'request' AND session_id IS NOT NULL
       THEN COALESCE((SELECT m.id FROM assist_messages m WHERE m.approval_id = line.id LIMIT 1), '') ELSE '' END,
  '',
  -- details
  json_object(
    'body_md', CASE type WHEN 'request' THEN COALESCE(json_extract(rq, '$.body_md'), '') ELSE '' END,
    'fields', json(COALESCE(json_extract(rq, '$.fields'), '[]')),
    'command', CASE WHEN type = 'tool_call'
                    THEN COALESCE(json_extract(perm_raw, '$.command'), json_extract(perm_raw, '$.arguments.command'), json_extract(perm_raw, '$.arguments.script'), '')
                    ELSE '' END,
    'tool', CASE WHEN type = 'tool_call' THEN COALESCE(perm_kind, '') ELSE '' END,
    'diffs', json('[]'),
    'dry_run', json(CASE WHEN (type = 'request' AND json_extract(rq, '$.dry_run')) OR (type <> 'request' AND flow_dry) THEN 'true' ELSE 'false' END)
  ),
  proposal_text, proposal_ref, proposal_size,
  CASE WHEN type = 'request' THEN json_remove(rq, '$.title', '$.body_md', '$.fields', '$.priority', '$.dry_run', '$.source', '$.client') ELSE '' END,
  status, comment, decided_by, created_at, expires_at, decided_at
FROM line;

DROP TABLE approvals;
ALTER TABLE approvals_new RENAME TO approvals;
CREATE INDEX approvals_status ON approvals (status, id);
CREATE INDEX approvals_run ON approvals (agent_run_id);
CREATE INDEX approvals_node ON approvals (node_run_id);
CREATE INDEX approvals_conversation ON approvals (conversation_id, id);
