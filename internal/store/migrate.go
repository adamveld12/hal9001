package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations returns the embedded migrations ordered by version. Files are
// named NNNN_description.sql; versions must be unique.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		numPart, _, _ := strings.Cut(name, "_")
		v, err := strconv.Atoi(numPart)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("store: migration %q: name must start with a positive version number", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("store: migrations %q and %q share version %d", prev, name, v)
		}
		seen[v] = name
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate applies every migration newer than the recorded schema version, each
// in its own transaction together with its schema_migrations row. Running it
// again is a no-op. A database newer than this binary is refused.
func (s *Store) migrate(ctx context.Context) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	// Two processes opening a brand-new database at once can hit SQLITE_BUSY
	// while the first switches it to WAL, which busy_timeout does not cover.
	for attempt := 0; ; attempt++ {
		err = s.migrateOnce(ctx, migs)
		if err == nil || attempt >= 20 || !strings.Contains(err.Error(), "SQLITE_BUSY") {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Store) migrateOnce(ctx context.Context, migs []migration) error {
	// One immediate transaction covers the whole run, so concurrent openers
	// queue on the write lock (busy_timeout) and then find nothing to do.
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migrations: %w", err)
	}
	if err := applyMigrations(ctx, tx, migs, ts(s.now())); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migrations: %w", err)
	}
	return nil
}

func applyMigrations(ctx context.Context, tx *sql.Tx, migs []migration, now string) error {
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if len(migs) > 0 && current > migs[len(migs)-1].version {
		return fmt.Errorf("store: database schema version %d is newer than this binary supports (%d)", current, migs[len(migs)-1].version)
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("store: apply migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`, m.version, m.name, now); err != nil {
			return fmt.Errorf("store: record migration %s: %w", m.name, err)
		}
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.rdb.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}
