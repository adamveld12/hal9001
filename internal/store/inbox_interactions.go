package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

const interactionCols = `id, item_id, conversation_id, kind, action_index, answers, message, status, approval_id, result, error, created_at, updated_at`

func scanInteraction(r rowScanner) (InboxInteraction, error) {
	var (
		in                InboxInteraction
		idx               sql.NullInt64
		answers, ca, upAt string
	)
	if err := r.Scan(&in.ID, &in.ItemID, &in.ConversationID, &in.Kind, &idx, &answers, &in.Message, &in.Status, &in.ApprovalID,
		&in.Result, &in.Error, &ca, &upAt); err != nil {
		return InboxInteraction{}, scanErr(err)
	}
	if idx.Valid {
		n := int(idx.Int64)
		in.ActionIndex = &n
	}
	if err := json.Unmarshal([]byte(answers), &in.Answers); err != nil {
		return InboxInteraction{}, fmt.Errorf("store: decode inbox answers: %w", err)
	}
	var err error
	if in.CreatedAt, err = parseTS(ca); err != nil {
		return InboxInteraction{}, err
	}
	in.UpdatedAt, err = parseTS(upAt)
	return in, err
}

// StartInboxInteraction records a new interaction of an inbox item in status running, assigning its ID and times, and
// links the item to ConversationID when it has no conversation yet. It is the single gate of what may happen once: it
// fails with ErrConflict when the item has another interaction that is still running or waiting, when an action was
// already used (any status), when a question was already answered, or when the item is linked to another conversation.
// ErrNotFound for an unknown item. The change is published as an update of the item.
func (s *Store) StartInboxInteraction(ctx context.Context, in *InboxInteraction) error {
	switch in.Kind {
	case InteractionAnswers:
		if len(in.Answers) == 0 {
			return fmt.Errorf("%w: answers without answers", ErrInvalid)
		}
	case InteractionAction:
		if in.ActionIndex == nil {
			return fmt.Errorf("%w: action without an index", ErrInvalid)
		}
	case InteractionReply:
	default:
		return fmt.Errorf("%w: interaction kind %q", ErrInvalid, in.Kind)
	}
	if in.ConversationID == "" || in.Message == "" {
		return fmt.Errorf("%w: interaction needs a conversation and a message", ErrInvalid)
	}
	answers, err := jsonText(nonNilSlice(in.Answers))
	if err != nil {
		return err
	}
	now := s.Now()
	if in.ID == "" {
		in.ID = s.NewID()
	}
	in.Status, in.CreatedAt, in.UpdatedAt = InteractionRunning, now, now
	return s.write(ctx, func(t *txn) error {
		var conv string
		if err := t.QueryRowContext(ctx, `SELECT conversation_id FROM inbox_items WHERE id = ?`, in.ItemID).Scan(&conv); err != nil {
			return scanErr(err)
		}
		if conv != "" && conv != in.ConversationID {
			return fmt.Errorf("%w: the note is already linked to conversation %s", ErrConflict, conv)
		}
		var active int
		if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox_interactions WHERE item_id = ? AND status IN (`+interactionActiveIn+`)`, in.ItemID).Scan(&active); err != nil {
			return fmt.Errorf("store: count active interactions: %w", err)
		}
		if active > 0 {
			return fmt.Errorf("%w: the agent is still working on the previous message", ErrConflict)
		}
		switch in.Kind {
		case InteractionAction:
			var used int
			if err := t.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox_interactions WHERE item_id = ? AND kind = 'action' AND action_index = ?`, in.ItemID, *in.ActionIndex).Scan(&used); err != nil {
				return fmt.Errorf("store: count used actions: %w", err)
			}
			if used > 0 {
				return fmt.Errorf("%w: this action was already used", ErrConflict)
			}
		case InteractionAnswers:
			prev, err := answeredIn(ctx, t, in.ItemID)
			if err != nil {
				return err
			}
			for _, a := range in.Answers {
				if prev[a.QuestionID] {
					return fmt.Errorf("%w: question %q was already answered", ErrConflict, a.QuestionID)
				}
			}
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO inbox_interactions (`+interactionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			in.ID, in.ItemID, in.ConversationID, in.Kind, nullInt(in.ActionIndex), answers, in.Message, in.Status, "", "", "", ts(now), ts(now)); err != nil {
			return fmt.Errorf("store: insert inbox_interaction: %w", err)
		}
		if conv == "" {
			if _, err := t.ExecContext(ctx, `UPDATE inbox_items SET conversation_id = ? WHERE id = ?`, in.ConversationID, in.ItemID); err != nil {
				return fmt.Errorf("store: link inbox conversation: %w", err)
			}
		}
		t.note(ResInboxItem, in.ItemID, OpUpdate)
		return nil
	})
}

// answeredIn is the set of question ids the item's answers interactions already cover.
func answeredIn(ctx context.Context, t *txn, itemID string) (map[string]bool, error) {
	rows, err := t.QueryContext(ctx, `SELECT answers FROM inbox_interactions WHERE item_id = ? AND kind = 'answers'`, itemID)
	if err != nil {
		return nil, fmt.Errorf("store: list answers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("store: scan answers: %w", err)
		}
		var as []InboxAnswer
		if err := json.Unmarshal([]byte(raw), &as); err != nil {
			return nil, fmt.Errorf("store: decode answers: %w", err)
		}
		for _, a := range as {
			out[a.QuestionID] = true
		}
	}
	return out, rows.Err()
}

// DeleteInboxInteraction removes an interaction whose message could not be sent, so the action or question stays
// available. The conversation link of the item stays.
func (s *Store) DeleteInboxInteraction(ctx context.Context, id string) error {
	return s.write(ctx, func(t *txn) error {
		var item string
		if err := t.QueryRowContext(ctx, `SELECT item_id FROM inbox_interactions WHERE id = ?`, id).Scan(&item); err != nil {
			return scanErr(err)
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM inbox_interactions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete inbox_interaction: %w", err)
		}
		t.note(ResInboxItem, item, OpUpdate)
		return nil
	})
}

// SettleInboxInteraction applies fn to the active (running or waiting) interaction of conversationID and stores the
// result, stamping UpdatedAt. It reports false, changing nothing, when the conversation has no active interaction
// (a turn the user started in the chat itself). The change is published as an update of the item.
func (s *Store) SettleInboxInteraction(ctx context.Context, conversationID string, fn func(*InboxInteraction)) (bool, error) {
	found := false
	err := s.write(ctx, func(t *txn) error {
		in, err := scanInteraction(t.QueryRowContext(ctx, `SELECT `+interactionCols+` FROM inbox_interactions
			WHERE conversation_id = ? AND status IN (`+interactionActiveIn+`) ORDER BY created_at DESC LIMIT 1`, conversationID))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		fn(&in)
		in.UpdatedAt = s.Now()
		if _, err := t.ExecContext(ctx, `UPDATE inbox_interactions SET status=?, approval_id=?, result=?, error=?, updated_at=? WHERE id=?`,
			in.Status, in.ApprovalID, in.Result, in.Error, ts(in.UpdatedAt), in.ID); err != nil {
			return fmt.Errorf("store: update inbox_interaction: %w", err)
		}
		t.note(ResInboxItem, in.ItemID, OpUpdate)
		return nil
	})
	return found, err
}

// FailActiveInboxInteractions marks every running or waiting interaction failed with reason: their turns died with the
// daemon. It returns how many it changed.
func (s *Store) FailActiveInboxInteractions(ctx context.Context, reason string) (int, error) {
	n := 0
	err := s.write(ctx, func(t *txn) error {
		items, err := activeInteractionItems(ctx, t)
		if err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `UPDATE inbox_interactions SET status = 'failed', error = ?, updated_at = ? WHERE status IN (`+interactionActiveIn+`)`, reason, ts(s.Now()))
		if err != nil {
			return fmt.Errorf("store: fail active interactions: %w", err)
		}
		c, _ := res.RowsAffected()
		n = int(c)
		for _, id := range items {
			t.note(ResInboxItem, id, OpUpdate)
		}
		return nil
	})
	return n, err
}

// ListInboxInteractions lists an item's interactions, oldest first.
func (s *Store) ListInboxInteractions(ctx context.Context, itemID string) ([]InboxInteraction, error) {
	rows, err := s.rdb.QueryContext(ctx, `SELECT `+interactionCols+` FROM inbox_interactions WHERE item_id = ? ORDER BY created_at, id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("store: list inbox interactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []InboxInteraction{}
	for rows.Next() {
		in, err := scanInteraction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// activeInteractionItems lists the items that have a running or waiting interaction.
func activeInteractionItems(ctx context.Context, t *txn) ([]string, error) {
	rows, err := t.QueryContext(ctx, `SELECT DISTINCT item_id FROM inbox_interactions WHERE status IN (`+interactionActiveIn+`)`)
	if err != nil {
		return nil, fmt.Errorf("store: list active interactions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: scan active interaction: %w", err)
		}
		items = append(items, id)
	}
	return items, rows.Err()
}
