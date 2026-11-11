package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A conversation is either an assist session (source chat) or the agent run of a flow node (source flow). Both are
// listed together: ListConversations reads them with one query. The turns of a chat are agent runs too, but they
// belong to their session and are never listed. A propose-phase agent run that has a successor (the execute phase,
// prev_agent_run_id = it) is part of the successor's conversation and is not listed on its own.

// Conversation sources.
const (
	ConversationChat = "chat"
	ConversationFlow = "flow"
)

// ConversationKindAgent is the Kind of a flow conversation; a chat's Kind is its assist target kind.
const ConversationKindAgent = "agent"

// ConversationRow is one entry of the conversation list: the columns both kinds have in common. Status is already
// normalised (see ConversationFilter.Status).
type ConversationRow struct {
	Source string
	ID     string
	Kind   string
	// Title is the chat's stored title; empty for a flow conversation.
	Title   string
	Status  string
	Runtime string
	Model   string
	// FlowID, FlowRunID, NodeID and NodeRunID locate a flow conversation. A fork chat carries those of the run it was
	// forked from, and a chat that drafts a flow has the flow's id in FlowID.
	FlowID    string
	FlowRunID string
	NodeID    string
	NodeRunID string
	// AgentRunID is the head agent run of a flow conversation; empty for a chat.
	AgentRunID      string
	AssistSessionID string
	// ForkedFromAgentRunID and ForkContext are those of a fork chat.
	ForkedFromAgentRunID string
	ForkContext          string
	StartedAt            time.Time
	// UpdatedAt is the last activity: a chat's updated_at, a run's end (or start, while it runs).
	UpdatedAt time.Time
	EndedAt   *time.Time
}

// ConversationFilter selects conversations for ListConversations.
type ConversationFilter struct {
	// ID selects one conversation by its id (an assist session id or the head agent run id).
	ID string
	// Source is ConversationChat, ConversationFlow or empty for both.
	Source string
	// Kind is a chat's target kind, or ConversationKindAgent for flow conversations.
	Kind      string
	FlowID    string
	NodeID    string
	FlowRunID string
	Runtime   string
	// Status is the normalised status: running (an agent run that is queued or running), awaiting_approval, idle,
	// succeeded, failed, killed, denied, interrupted, timeout or closed.
	Status string
	// ForkedFrom selects the fork chats of these agent runs.
	ForkedFrom []string
	// Q is a case-insensitive substring searched in titles, ids and flow/node ids, the messages of chats and the
	// prompts and results of flow conversations.
	Q string
	// Time filters on the last activity: Since inclusive, Until exclusive.
	Time TimeRange
	// After continues behind a row of the previous page: rows older than (AfterActivity, AfterID) in the list order.
	AfterActivity time.Time
	AfterID       string
	Limit         int
}

// ConversationPage is one page of conversations, newest activity first. More says another page follows; continue with
// the last row's UpdatedAt and ID.
type ConversationPage struct {
	Items []ConversationRow
	More  bool
}

const conversationCols = "source, id, kind, title, status, runtime, model, flow_id, flow_run_id, node_id, node_run_id, agent_run_id, assist_session_id, " +
	"forked_from_agent_run_id, fork_context, started_at, activity, ended_at"

func scanConversationRow(r rowScanner) (ConversationRow, error) {
	var (
		c                 ConversationRow
		started, activity string
		ended             sql.NullString
	)
	if err := r.Scan(&c.Source, &c.ID, &c.Kind, &c.Title, &c.Status, &c.Runtime, &c.Model, &c.FlowID, &c.FlowRunID, &c.NodeID, &c.NodeRunID,
		&c.AgentRunID, &c.AssistSessionID, &c.ForkedFromAgentRunID, &c.ForkContext, &started, &activity, &ended); err != nil {
		return ConversationRow{}, scanErr(err)
	}
	var err error
	if c.StartedAt, err = parseTS(started); err != nil {
		return ConversationRow{}, err
	}
	if c.UpdatedAt, err = parseTS(activity); err != nil {
		return ConversationRow{}, err
	}
	if c.EndedAt, err = parseTSPtr(ended); err != nil {
		return ConversationRow{}, err
	}
	return c, nil
}

// likePattern turns q into a LIKE pattern that matches it anywhere (case-insensitive for ASCII) with escape '\'.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// chatBranch selects the assist sessions. A fork chat takes the flow, node and run ids of the run it continues.
func chatBranch(f ConversationFilter) (string, []any) {
	q := "SELECT 'chat' AS source, s.id AS id, s.target_kind AS kind, s.title AS title, s.status AS status, s.runtime AS runtime, s.model AS model, " +
		"CASE WHEN fr.flow_id IS NOT NULL THEN fr.flow_id WHEN s.target_kind = 'flow' THEN s.target_id ELSE '' END AS flow_id, " +
		"COALESCE(fr.id, '') AS flow_run_id, COALESCE(nr.node_id, '') AS node_id, COALESCE(nr.id, '') AS node_run_id, '' AS agent_run_id, s.id AS assist_session_id, " +
		"s.forked_from_agent_run_id AS forked_from_agent_run_id, s.fork_context AS fork_context, s.created_at AS started_at, s.updated_at AS activity, s.closed_at AS ended_at " +
		"FROM assist_sessions s " +
		"LEFT JOIN agent_runs ar ON s.forked_from_agent_run_id <> '' AND ar.id = s.forked_from_agent_run_id " +
		"LEFT JOIN node_runs nr ON nr.id = ar.node_run_id " +
		"LEFT JOIN flow_runs fr ON fr.id = nr.flow_run_id"
	var w where
	if f.Q != "" {
		p := likePattern(f.Q)
		w.add(`(s.title LIKE ? ESCAPE '\' OR s.id LIKE ? ESCAPE '\' OR s.target_id LIKE ? ESCAPE '\' OR s.forked_from_agent_run_id LIKE ? ESCAPE '\' `+
			`OR fr.flow_id LIKE ? ESCAPE '\' OR fr.id LIKE ? ESCAPE '\' OR nr.node_id LIKE ? ESCAPE '\' `+
			`OR EXISTS (SELECT 1 FROM assist_messages m WHERE m.session_id = s.id AND m.text LIKE ? ESCAPE '\'))`,
			p, p, p, p, p, p, p, p)
	}
	return q + w.clause(), w.args
}

// flowBranch selects the head agent run of every flow node conversation.
func flowBranch(f ConversationFilter) (string, []any) {
	q := "SELECT 'flow' AS source, a.id AS id, 'agent' AS kind, '' AS title, CASE a.status WHEN 'queued' THEN 'running' ELSE a.status END AS status, " +
		"a.runtime AS runtime, a.model AS model, fr.flow_id AS flow_id, fr.id AS flow_run_id, nr.node_id AS node_id, nr.id AS node_run_id, a.id AS agent_run_id, " +
		"'' AS assist_session_id, '' AS forked_from_agent_run_id, '' AS fork_context, COALESCE(a.started_at, a.created_at) AS started_at, " +
		"COALESCE(a.ended_at, a.started_at, a.created_at) AS activity, a.ended_at AS ended_at " +
		"FROM agent_runs a JOIN node_runs nr ON nr.id = a.node_run_id JOIN flow_runs fr ON fr.id = nr.flow_run_id"
	var w where
	w.add("NOT EXISTS (SELECT 1 FROM agent_runs x WHERE x.prev_agent_run_id = a.id)")
	if f.Q != "" {
		p := likePattern(f.Q)
		w.add(`(fr.flow_id LIKE ? ESCAPE '\' OR nr.node_id LIKE ? ESCAPE '\' OR a.id LIKE ? ESCAPE '\' OR nr.id LIKE ? ESCAPE '\' OR fr.id LIKE ? ESCAPE '\' `+
			`OR a.prompt_text LIKE ? ESCAPE '\' OR a.result_text LIKE ? ESCAPE '\' `+
			`OR EXISTS (SELECT 1 FROM agent_runs p WHERE p.id = a.prev_agent_run_id AND (p.prompt_text LIKE ? ESCAPE '\' OR p.result_text LIKE ? ESCAPE '\')))`,
			p, p, p, p, p, p, p, p, p)
	}
	return q + w.clause(), w.args
}

// ListConversations lists the conversations ordered by last activity, newest first, then by id descending. Page with
// AfterActivity and AfterID: the order is total, so rows that share a timestamp are neither skipped nor repeated.
func (s *Store) ListConversations(ctx context.Context, f ConversationFilter) (ConversationPage, error) {
	var (
		branches []string
		args     []any
	)
	if f.Source != ConversationFlow {
		q, a := chatBranch(f)
		branches, args = append(branches, q), append(args, a...)
	}
	if f.Source != ConversationChat && (f.Kind == "" || f.Kind == ConversationKindAgent) && len(f.ForkedFrom) == 0 {
		q, a := flowBranch(f)
		branches, args = append(branches, q), append(args, a...)
	}
	if len(branches) == 0 {
		return ConversationPage{Items: []ConversationRow{}}, nil
	}
	var w where
	w.eq("id", f.ID)
	w.eq("kind", f.Kind)
	w.eq("flow_id", f.FlowID)
	w.eq("node_id", f.NodeID)
	w.eq("flow_run_id", f.FlowRunID)
	w.eq("runtime", f.Runtime)
	w.eq("status", f.Status)
	w.in("forked_from_agent_run_id", f.ForkedFrom)
	w.timeRange("activity", f.Time)
	if !f.AfterActivity.IsZero() {
		w.add("(activity < ? OR (activity = ? AND id < ?))", ts(f.AfterActivity), ts(f.AfterActivity), f.AfterID)
	}
	limit := limitOf(ListOpts{Limit: f.Limit})
	query := sqlJoin("SELECT ", conversationCols, " FROM (", strings.Join(branches, " UNION ALL "), ")", w.clause(), " ORDER BY activity DESC, id DESC LIMIT ?")
	args = append(append(args, w.args...), limit+1)
	rows, err := s.rdb.QueryContext(ctx, query, args...)
	if err != nil {
		return ConversationPage{}, fmt.Errorf("store: list conversations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := ConversationPage{Items: []ConversationRow{}}
	for rows.Next() {
		c, err := scanConversationRow(rows)
		if err != nil {
			return ConversationPage{}, err
		}
		page.Items = append(page.Items, c)
	}
	if err := rows.Err(); err != nil {
		return ConversationPage{}, fmt.Errorf("store: list conversations: %w", err)
	}
	if len(page.Items) > limit {
		page.Items, page.More = page.Items[:limit], true
	}
	return page, nil
}

// maxChain bounds the walk along prev_agent_run_id links (a node has a propose and an execute phase).
const maxChain = 16

// AgentRunChain returns the agent run ids of the phases that form one conversation with run id, oldest first (a
// propose-phase run, then the execute-phase run that continues it). The last id is the head. ErrNotFound for an
// unknown run.
func (s *Store) AgentRunChain(ctx context.Context, id string) ([]string, error) {
	// Forward to the head first.
	head := id
	for range maxChain {
		var next string
		err := s.rdb.QueryRowContext(ctx, "SELECT id FROM agent_runs WHERE prev_agent_run_id = ? LIMIT 1", head).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("store: agent run chain: %w", err)
		}
		head = next
	}
	chain := []string{head}
	cur := head
	for range maxChain {
		var prev string
		err := s.rdb.QueryRowContext(ctx, "SELECT COALESCE(prev_agent_run_id, '') FROM agent_runs WHERE id = ?", cur).Scan(&prev)
		if errors.Is(err, sql.ErrNoRows) {
			if cur == head {
				return nil, ErrNotFound
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("store: agent run chain: %w", err)
		}
		if prev == "" {
			break
		}
		chain = append([]string{prev}, chain...)
		cur = prev
	}
	return chain, nil
}

// ForkCounts returns, for each of the given agent run chains' ids, how many assist sessions were forked from any run of
// the chain: the result maps the HEAD id (last element of each chain) to the count. chains[i] lists one conversation's
// run ids.
func (s *Store) ForkCounts(ctx context.Context, chains [][]string) (map[string]int, error) {
	out := map[string]int{}
	var all []string
	owner := map[string]string{}
	for _, c := range chains {
		for _, id := range c {
			all = append(all, id)
			owner[id] = c[len(c)-1]
		}
	}
	if len(all) == 0 {
		return out, nil
	}
	query := sqlJoin("SELECT forked_from_agent_run_id, COUNT(*) FROM assist_sessions WHERE forked_from_agent_run_id IN (", placeholders(len(all)), ") GROUP BY forked_from_agent_run_id")
	rows, err := s.rdb.QueryContext(ctx, query, anySlice(all)...)
	if err != nil {
		return nil, fmt.Errorf("store: fork counts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("store: fork counts: %w", err)
		}
		out[owner[id]] += n
	}
	return out, rows.Err()
}

// LastChatText returns the text of the newest user or assistant message of a session ("" when there is none).
func (s *Store) LastChatText(ctx context.Context, sessionID string) (string, error) {
	var text string
	err := s.rdb.QueryRowContext(ctx, `SELECT text FROM assist_messages WHERE session_id = ? AND role IN ('user','assistant') AND kind = '' AND text IS NOT NULL AND text <> '' ORDER BY id DESC LIMIT 1`, sessionID).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: last chat text: %w", err)
	}
	return text, nil
}
