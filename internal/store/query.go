package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"hal9001/internal/store/blob"
)

// where accumulates AND-ed SQL conditions and their arguments.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) eq(col, val string) {
	if val != "" {
		w.add(col+" = ?", val)
	}
}

func (w *where) in(col string, vals []string) {
	if len(vals) == 0 {
		return
	}
	w.add(col+" IN ("+placeholders(len(vals))+")", anySlice(vals)...)
}

func (w *where) timeRange(col string, r TimeRange) {
	if !r.Since.IsZero() {
		w.add(col+" >= ?", ts(r.Since))
	}
	if !r.Until.IsZero() {
		w.add(col+" < ?", ts(r.Until))
	}
}

func (w *where) boolPtr(col string, p *bool) {
	if p != nil {
		w.add(col+" = ?", b2i(*p))
	}
}

func (w *where) clause() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func anySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

// listPage runs base (a SELECT ... FROM ...) with w and keyset pagination on
// idCol. It fetches one extra row to learn whether another page exists.
func listPage[T any](ctx context.Context, q dbq, base string, w *where, o ListOpts, idCol string,
	scan func(rowScanner) (T, error), idOf func(T) string) (Page[T], error) {
	limit := limitOf(o)
	if o.Cursor != "" {
		if o.Oldest {
			w.add(idCol+" > ?", o.Cursor)
		} else {
			w.add(idCol+" < ?", o.Cursor)
		}
	}
	order := " DESC"
	if o.Oldest {
		order = " ASC"
	}
	query := sqlJoin(base, w.clause(), " ORDER BY ", idCol, order, " LIMIT ?")
	args := append(append([]any{}, w.args...), limit+1)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return Page[T]{}, fmt.Errorf("store: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := Page[T]{Items: []T{}}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return Page[T]{}, err
		}
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return Page[T]{}, fmt.Errorf("store: list: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = idOf(page.Items[limit-1])
	}
	return page, nil
}

// queryAll runs query and scans every row.
func queryAll[T any](ctx context.Context, q dbq, scan func(rowScanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanString(r rowScanner) (string, error) {
	var s string
	err := r.Scan(&s)
	return s, err
}

// sqlJoin concatenates SQL fragments assembled from constants and column lists.
func sqlJoin(parts ...string) string { return strings.Join(parts, "") }

// jsonText encodes v for a TEXT JSON column; nil slices become "[]".
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: encode json: %w", err)
	}
	return string(b), nil
}

func stringsText(v []string) string {
	if v == nil {
		return "[]"
	}
	b, _ := json.Marshal(v) // []string cannot fail
	return string(b)
}

func parseStrings(s string) ([]string, error) {
	out := []string{}
	if s == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("store: decode json list: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// rawText validates and normalises an optional JSON column. Empty input is
// stored as the JSON null.
func rawText(m json.RawMessage) (string, error) {
	if len(m) == 0 {
		return "null", nil
	}
	if !json.Valid(m) {
		return "", fmt.Errorf("%w: invalid JSON", ErrInvalid)
	}
	return string(m), nil
}

func parseRaw(s string) json.RawMessage {
	if s == "" || s == "null" {
		return nil
	}
	return json.RawMessage(s)
}

// contentCols lists the three columns of a content triple. In slim mode the
// inline text is not read from disk.
func contentCols(name string, slim bool) string {
	text := name + "_text"
	if slim {
		text = "''"
	}
	return text + ", " + name + "_ref, " + name + "_size"
}

func scanDests(c *Content) []any { return []any{&c.Text, &c.Ref, &c.Size} }

// prepContent decides how c is persisted. load fetches the currently stored
// content (nil on create) and is used when c is unhydrated inline content that
// must be preserved.
func (t *txn) prepContent(c Content, load func() (Content, error)) (Content, error) {
	switch {
	case c.Text != "":
		if len(c.Text) > t.s.spill {
			ref, err := t.s.blobs.Put([]byte(c.Text))
			if err != nil {
				return Content{}, err
			}
			return Content{Ref: ref, Size: int64(len(c.Text))}, nil
		}
		return Content{Text: c.Text, Size: int64(len(c.Text))}, nil
	case c.Ref != "":
		if c.Size <= 0 {
			return Content{}, fmt.Errorf("%w: content ref %s has no size", ErrInvalid, c.Ref)
		}
		return Content{Ref: c.Ref, Size: c.Size}, nil
	case c.Size > 0:
		if load == nil {
			return Content{}, fmt.Errorf("%w: content has size %d but no text or ref", ErrInvalid, c.Size)
		}
		return load()
	}
	return Content{}, nil
}

// loadContent returns the stored content triple of one column.
func (t *txn) loadContent(ctx context.Context, table, name, idCol, id string) func() (Content, error) {
	return func() (Content, error) {
		var c Content
		// table, name and idCol are compile-time constants at every call site.
		q := "SELECT " + contentCols(name, false) + " FROM " + table + " WHERE " + idCol + " = ?"
		if err := t.QueryRowContext(ctx, q, id).Scan(scanDests(&c)...); err != nil {
			return Content{}, fmt.Errorf("store: load %s.%s: %w", table, name, err)
		}
		return c, nil
	}
}

// ReadContent returns the full bytes of c: its inline text, or the blob it points to.
func (s *Store) ReadContent(c Content) ([]byte, error) {
	if c.Ref == "" {
		return []byte(c.Text), nil
	}
	b, err := s.blobs.Get(c.Ref)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, fmt.Errorf("%w: blob %s", ErrNotFound, c.Ref)
	}
	return b, err
}

// PutBlob stores data in the blob store regardless of size and returns a
// Content referencing it, for payloads the caller streams (e.g. raw JSON-RPC).
func (s *Store) PutBlob(data []byte) (Content, error) {
	ref, err := s.blobs.Put(data)
	if err != nil {
		return Content{}, err
	}
	return Content{Ref: ref, Size: int64(len(data))}, nil
}

func scanErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("store: scan: %w", err)
}
