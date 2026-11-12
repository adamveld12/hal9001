package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"hal9001/internal/outschema"
)

func nonNilSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

const inboxCols = `id, source_type, flow_run_id, node_run_id, agent_run_id, title, priority, summary_md, suggested_reply, links,
	parse_error, is_read, starred, archived, dry_run, created_at, client, thread_key, questions, actions, conversation_id`

func scanInbox(r rowScanner) (InboxItem, error) {
	var (
		it                                 InboxItem
		src, prio, links, created          string
		questions, actions                 string
		parseErr, read, starred, arch, dry int
	)
	if err := r.Scan(&it.ID, &src, &it.FlowRunID, &it.NodeRunID, &it.AgentRunID, &it.Title, &prio, &it.SummaryMD, &it.SuggestedReply, &links,
		&parseErr, &read, &starred, &arch, &dry, &created, &it.Client, &it.ThreadKey, &questions, &actions, &it.ConversationID); err != nil {
		return InboxItem{}, scanErr(err)
	}
	it.SourceType, it.Priority = SourceType(src), Priority(prio)
	it.ParseError, it.Read, it.Starred, it.Archived, it.DryRun = parseErr != 0, read != 0, starred != 0, arch != 0, dry != 0
	it.Links = []InboxLink{}
	if err := json.Unmarshal([]byte(links), &it.Links); err != nil {
		return InboxItem{}, fmt.Errorf("store: decode inbox links: %w", err)
	}
	if it.Links == nil {
		it.Links = []InboxLink{}
	}
	if err := json.Unmarshal([]byte(questions), &it.Questions); err != nil {
		return InboxItem{}, fmt.Errorf("store: decode inbox questions: %w", err)
	}
	if err := json.Unmarshal([]byte(actions), &it.Actions); err != nil {
		return InboxItem{}, fmt.Errorf("store: decode inbox actions: %w", err)
	}
	var err error
	it.CreatedAt, err = parseTS(created)
	return it, err
}

// CreateInboxItem inserts it, assigning ID, Priority (normal) and CreatedAt.
// SourceType must name the id that is set (AgentRunID or NodeRunID), or be SourceMCP, which has no run ids and
// names the external client in Client. FlowRunID is filled in from the node/agent run when empty, so a starred item
// always protects its whole trace from pruning. Questions and Actions are only for a note an agent wrote (AgentRunID
// set) and must pass outschema.Check; otherwise ErrInvalid.
func (s *Store) CreateInboxItem(ctx context.Context, it *InboxItem) error {
	if it.Title == "" {
		return fmt.Errorf("%w: inbox title required", ErrInvalid)
	}
	if it.Priority == "" {
		it.Priority = PriorityNormal
	}
	if !it.Priority.valid() {
		return fmt.Errorf("%w: inbox priority %q", ErrInvalid, it.Priority)
	}
	switch {
	case it.SourceType == SourceAgentRun && it.AgentRunID != "":
	case it.SourceType == SourceNodeRun && it.NodeRunID != "":
	case it.SourceType == SourceMCP && it.AgentRunID == "" && it.NodeRunID == "" && it.FlowRunID == "":
	default:
		return fmt.Errorf("%w: inbox source_type %q does not match the source id provided", ErrInvalid, it.SourceType)
	}
	if it.ID == "" {
		it.ID = s.NewID()
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = s.Now()
	}
	links, err := jsonText(nonNilLinks(it.Links))
	if err != nil {
		return err
	}
	if len(it.Questions) > 0 || len(it.Actions) > 0 {
		if it.AgentRunID == "" {
			return fmt.Errorf("%w: only a note an agent wrote can have questions or actions", ErrInvalid)
		}
		if err := outschema.Check(it.Questions, it.Actions); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
	}
	questions, err := jsonText(nonNilSlice(it.Questions))
	if err != nil {
		return err
	}
	actions, err := jsonText(nonNilSlice(it.Actions))
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		if err := t.resolveInboxLinks(ctx, it); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO inbox_items (`+inboxCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			it.ID, it.SourceType, it.FlowRunID, it.NodeRunID, it.AgentRunID, it.Title, it.Priority, it.SummaryMD, it.SuggestedReply, links,
			b2i(it.ParseError), b2i(it.Read), b2i(it.Starred), b2i(it.Archived), b2i(it.DryRun), ts(it.CreatedAt), it.Client, it.ThreadKey,
			questions, actions, it.ConversationID)
		if err != nil {
			return fmt.Errorf("store: insert inbox_item: %w", err)
		}
		t.note(ResInboxItem, it.ID, OpCreate)
		return nil
	})
}

func nonNilLinks(l []InboxLink) []InboxLink {
	if l == nil {
		return []InboxLink{}
	}
	return l
}

// resolveInboxLinks fills in whichever of node_run_id / flow_run_id is derivable.
func (t *txn) resolveInboxLinks(ctx context.Context, it *InboxItem) error {
	if it.NodeRunID == "" && it.AgentRunID != "" {
		err := t.QueryRowContext(ctx, `SELECT node_run_id FROM agent_runs WHERE id = ?`, it.AgentRunID).Scan(&it.NodeRunID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: resolve inbox node run: %w", err)
		}
	}
	if it.FlowRunID == "" && it.NodeRunID != "" {
		err := t.QueryRowContext(ctx, `SELECT flow_run_id FROM node_runs WHERE id = ?`, it.NodeRunID).Scan(&it.FlowRunID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: resolve inbox flow run: %w", err)
		}
	}
	return nil
}

// GetInboxItem returns one inbox item.
func (s *Store) GetInboxItem(ctx context.Context, id string) (*InboxItem, error) {
	it, err := scanInbox(s.rdb.QueryRowContext(ctx, "SELECT "+inboxCols+" FROM inbox_items WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// UpdateInboxItem writes title, priority, summary, reply, links and the
// read/starred/archived flags. Prefer SetInboxFlags for flag-only changes: it
// cannot overwrite concurrent edits to other fields.
func (s *Store) UpdateInboxItem(ctx context.Context, it *InboxItem) error {
	if it.Title == "" || !it.Priority.valid() {
		return fmt.Errorf("%w: inbox title %q / priority %q", ErrInvalid, it.Title, it.Priority)
	}
	links, err := jsonText(nonNilLinks(it.Links))
	if err != nil {
		return err
	}
	return s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE inbox_items SET title=?, priority=?, summary_md=?, suggested_reply=?, links=?, parse_error=?, is_read=?, starred=?, archived=? WHERE id=?`,
			it.Title, it.Priority, it.SummaryMD, it.SuggestedReply, links, b2i(it.ParseError), b2i(it.Read), b2i(it.Starred), b2i(it.Archived), it.ID)
		if err != nil {
			return fmt.Errorf("store: update inbox_item: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		t.note(ResInboxItem, it.ID, OpUpdate)
		return nil
	})
}

// InboxFlags is a partial update of an item's flags; nil fields are unchanged.
type InboxFlags struct {
	Read     *bool
	Starred  *bool
	Archived *bool
}

// SetInboxFlags applies the non-nil flags and returns the updated item.
func (s *Store) SetInboxFlags(ctx context.Context, id string, f InboxFlags) (*InboxItem, error) {
	var out *InboxItem
	err := s.write(ctx, func(t *txn) error {
		res, err := t.ExecContext(ctx, `UPDATE inbox_items SET
			is_read  = COALESCE(?, is_read),
			starred  = COALESCE(?, starred),
			archived = COALESCE(?, archived)
			WHERE id = ?`, boolArg(f.Read), boolArg(f.Starred), boolArg(f.Archived), id)
		if err != nil {
			return fmt.Errorf("store: set inbox flags: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		it, err := scanInbox(t.QueryRowContext(ctx, "SELECT "+inboxCols+" FROM inbox_items WHERE id = ?", id))
		if err != nil {
			return err
		}
		out = &it
		t.note(ResInboxItem, id, OpUpdate)
		return nil
	})
	return out, err
}

func boolArg(p *bool) any {
	if p == nil {
		return nil
	}
	return b2i(*p)
}

// InboxFilter selects inbox items for ListInbox.
type InboxFilter struct {
	Read       *bool
	Starred    *bool
	Archived   *bool
	DryRun     *bool
	Priorities []Priority
	FlowRunID  string
	FlowID     string // items whose flow run belongs to this flow
	Search     string // case-insensitive substring of title or summary
	Time       TimeRange
	// ByPriority orders by priority (urgent first) and then newest first,
	// instead of newest first. Cursors stay item ids.
	ByPriority bool
	ListOpts
}

const inboxRank = `(CASE priority WHEN 'urgent' THEN 3 WHEN 'high' THEN 2 WHEN 'normal' THEN 1 ELSE 0 END)`

// ListInbox lists items, newest first, or by priority then newest first with
// ByPriority (Oldest is ignored in that mode).
func (s *Store) ListInbox(ctx context.Context, f InboxFilter) (Page[InboxItem], error) {
	w := f.whereClause()
	if !f.ByPriority {
		return listPage(ctx, s.rdb, "SELECT "+inboxCols+" FROM inbox_items", &w, f.ListOpts, "id",
			scanInbox, func(i InboxItem) string { return i.ID })
	}
	if f.Cursor != "" {
		w.add("("+inboxRank+" < (SELECT "+inboxRank+" FROM inbox_items c WHERE c.id = ?) OR ("+inboxRank+" = (SELECT "+inboxRank+" FROM inbox_items c WHERE c.id = ?) AND id < ?))",
			f.Cursor, f.Cursor, f.Cursor)
	}
	limit := limitOf(f.ListOpts)
	rows, err := s.rdb.QueryContext(ctx,
		sqlJoin("SELECT ", inboxCols, " FROM inbox_items", w.clause(), " ORDER BY ", inboxRank, " DESC, id DESC LIMIT ?"),
		append(append([]any{}, w.args...), limit+1)...)
	if err != nil {
		return Page[InboxItem]{}, fmt.Errorf("store: list inbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := Page[InboxItem]{Items: []InboxItem{}}
	for rows.Next() {
		it, err := scanInbox(rows)
		if err != nil {
			return Page[InboxItem]{}, err
		}
		page.Items = append(page.Items, it)
	}
	if err := rows.Err(); err != nil {
		return Page[InboxItem]{}, fmt.Errorf("store: list inbox: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	return page, nil
}

// Inbox bulk actions.
const (
	InboxArchive    = "archive"
	InboxUnarchive  = "unarchive"
	InboxMarkRead   = "mark_read"
	InboxMarkUnread = "mark_unread"
	InboxDelete     = "delete"
)

// MaxBulkInboxIDs bounds InboxBulk.IDs.
const MaxBulkInboxIDs = 1000

// InboxBulk is one bulk action on inbox items. Exactly one of IDs and Filter
// selects the targets (Filter ignores the paging fields). Archive and Delete
// leave starred items alone unless IncludeStarred; Delete only removes archived
// items.
type InboxBulk struct {
	Action         string
	IDs            []string
	Filter         *InboxFilter
	IncludeStarred bool
	// DryRun computes the result without changing anything.
	DryRun bool
}

// InboxBulkResult counts what an InboxBulk matched and did.
type InboxBulkResult struct {
	Matched            int      // targets selected (existing ids, or filter matches), including skipped ones
	Changed            int      // targets modified (or that a dry run would modify)
	SkippedStarred     int      // starred targets left alone by archive/delete
	SkippedNotArchived int      // delete targets that were not archived
	ChangedIDs         []string // ids of the changed targets
}

func (f *InboxFilter) whereClause() where {
	var w where
	w.boolPtr("is_read", f.Read)
	w.boolPtr("starred", f.Starred)
	w.boolPtr("archived", f.Archived)
	w.boolPtr("dry_run", f.DryRun)
	w.in("priority", strs(f.Priorities))
	w.eq("flow_run_id", f.FlowRunID)
	if f.FlowID != "" {
		w.add("flow_run_id IN (SELECT id FROM flow_runs WHERE flow_id = ?)", f.FlowID)
	}
	w.timeRange("created_at", f.Time)
	if f.Search != "" {
		w.add("(instr(lower(title), lower(?)) > 0 OR instr(lower(summary_md), lower(?)) > 0)", f.Search, f.Search)
	}
	return w
}

// BulkInbox applies b in one transaction. The filter is evaluated by the
// database inside that transaction, so the targets are exactly the rows that
// match when the change is made. One inbox_item change is recorded per changed
// item (a single wildcard change for Delete).
func (s *Store) BulkInbox(ctx context.Context, b InboxBulk) (InboxBulkResult, error) {
	switch b.Action {
	case InboxArchive, InboxUnarchive, InboxMarkRead, InboxMarkUnread, InboxDelete:
	default:
		return InboxBulkResult{}, fmt.Errorf("%w: unknown inbox action %q", ErrInvalid, b.Action)
	}
	if (b.Filter == nil) == (b.IDs == nil) {
		return InboxBulkResult{}, fmt.Errorf("%w: set exactly one of ids and filter", ErrInvalid)
	}
	if b.IDs != nil && (len(b.IDs) == 0 || len(b.IDs) > MaxBulkInboxIDs) {
		return InboxBulkResult{}, fmt.Errorf("%w: ids must hold 1 to %d items", ErrInvalid, MaxBulkInboxIDs)
	}
	res := InboxBulkResult{ChangedIDs: []string{}}
	err := s.write(ctx, func(t *txn) error {
		var w where
		if b.Filter != nil {
			w = b.Filter.whereClause()
		} else {
			w.in("id", b.IDs)
		}
		rows, err := t.QueryContext(ctx, sqlJoin("SELECT id, is_read, starred, archived FROM inbox_items", w.clause()), w.args...)
		if err != nil {
			return fmt.Errorf("store: bulk inbox select: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			var read, starred, archived bool
			if err := rows.Scan(&id, &read, &starred, &archived); err != nil {
				return fmt.Errorf("store: bulk inbox scan: %w", err)
			}
			res.Matched++
			var change bool
			switch b.Action {
			case InboxArchive:
				if starred && !b.IncludeStarred {
					res.SkippedStarred++
					continue
				}
				change = !archived
			case InboxUnarchive:
				change = archived
			case InboxMarkRead:
				change = !read
			case InboxMarkUnread:
				change = read
			case InboxDelete:
				if !archived {
					res.SkippedNotArchived++
					continue
				}
				if starred && !b.IncludeStarred {
					res.SkippedStarred++
					continue
				}
				change = true
			}
			if change {
				res.ChangedIDs = append(res.ChangedIDs, id)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("store: bulk inbox select: %w", err)
		}
		_ = rows.Close()
		res.Changed = len(res.ChangedIDs)
		if b.DryRun || res.Changed == 0 {
			return nil
		}
		const chunk = 500
		for i := 0; i < len(res.ChangedIDs); i += chunk {
			ids := res.ChangedIDs[i:min(i+chunk, len(res.ChangedIDs))]
			set := map[string]string{
				InboxArchive: "archived = 1", InboxUnarchive: "archived = 0",
				InboxMarkRead: "is_read = 1", InboxMarkUnread: "is_read = 0",
			}[b.Action]
			q := "UPDATE inbox_items SET " + set + " WHERE id IN (" + placeholders(len(ids)) + ")"
			if b.Action == InboxDelete {
				q = "DELETE FROM inbox_items WHERE id IN (" + placeholders(len(ids)) + ")"
			}
			if _, err := t.ExecContext(ctx, q, anySlice(ids)...); err != nil {
				return fmt.Errorf("store: bulk inbox %s: %w", b.Action, err)
			}
		}
		if b.Action == InboxDelete {
			t.note(ResInboxItem, "*", OpDelete)
			return nil
		}
		for _, id := range res.ChangedIDs {
			t.note(ResInboxItem, id, OpUpdate)
		}
		return nil
	})
	if err != nil {
		return InboxBulkResult{}, err
	}
	return res, nil
}
