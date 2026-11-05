package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const inboxCols = `id, source_type, flow_run_id, node_run_id, agent_run_id, title, priority, summary_md, suggested_reply, links,
	parse_error, is_read, starred, archived, dry_run, created_at`

func scanInbox(r rowScanner) (InboxItem, error) {
	var (
		it                                 InboxItem
		src, prio, links, created          string
		parseErr, read, starred, arch, dry int
	)
	if err := r.Scan(&it.ID, &src, &it.FlowRunID, &it.NodeRunID, &it.AgentRunID, &it.Title, &prio, &it.SummaryMD, &it.SuggestedReply, &links,
		&parseErr, &read, &starred, &arch, &dry, &created); err != nil {
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
	var err error
	it.CreatedAt, err = parseTS(created)
	return it, err
}

// CreateInboxItem inserts it, assigning ID, Priority (normal) and CreatedAt.
// SourceType must name the id that is set (AgentRunID or NodeRunID). FlowRunID
// is filled in from the node/agent run when empty, so a starred item always
// protects its whole trace from pruning.
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
	return s.write(ctx, func(t *txn) error {
		if err := t.resolveInboxLinks(ctx, it); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `INSERT INTO inbox_items (`+inboxCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			it.ID, it.SourceType, it.FlowRunID, it.NodeRunID, it.AgentRunID, it.Title, it.Priority, it.SummaryMD, it.SuggestedReply, links,
			b2i(it.ParseError), b2i(it.Read), b2i(it.Starred), b2i(it.Archived), b2i(it.DryRun), ts(it.CreatedAt))
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
	Search     string // case-insensitive substring of title or summary
	Time       TimeRange
	ListOpts
}

// ListInbox lists items, newest first. Ordering by priority is left to the
// client so cursors stay stable ULIDs.
func (s *Store) ListInbox(ctx context.Context, f InboxFilter) (Page[InboxItem], error) {
	var w where
	w.boolPtr("is_read", f.Read)
	w.boolPtr("starred", f.Starred)
	w.boolPtr("archived", f.Archived)
	w.boolPtr("dry_run", f.DryRun)
	w.in("priority", strs(f.Priorities))
	w.eq("flow_run_id", f.FlowRunID)
	w.timeRange("created_at", f.Time)
	if f.Search != "" {
		w.add("(instr(lower(title), lower(?)) > 0 OR instr(lower(summary_md), lower(?)) > 0)", f.Search, f.Search)
	}
	return listPage(ctx, s.rdb, "SELECT "+inboxCols+" FROM inbox_items", &w, f.ListOpts, "id",
		scanInbox, func(i InboxItem) string { return i.ID })
}
