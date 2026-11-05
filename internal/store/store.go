package store

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"hal9001/internal/store/blob"
)

// SpillThreshold is the largest payload (bytes) kept inline in a row; anything
// larger goes to the blob store (REQUIREMENTS §9).
const SpillThreshold = 64 << 10

const (
	defaultLimit = 50
	maxLimit     = 500
	readConns    = 8
)

// Store is the SQLite-backed trace store. It is safe for concurrent use.
//
// Writes go through a single connection (serialised in-process, BEGIN
// IMMEDIATE), so concurrent goroutines never hit write-lock contention or
// lost updates; reads use a separate WAL reader pool and never block on
// writers.
type Store struct {
	wdb    *sql.DB
	rdb    *sql.DB
	blobs  *blob.Store
	path   string
	now    func() time.Time
	spill  int
	notify atomic.Pointer[func(Change)]

	idMu    sync.Mutex
	entropy *ulid.MonotonicEntropy
}

type config struct {
	blobDir  string
	now      func() time.Time
	notifier func(Change)
}

// Option configures Open.
type Option func(*config)

// WithBlobDir sets the blob directory (default: "blobs" next to the database file).
func WithBlobDir(dir string) Option { return func(c *config) { c.blobDir = dir } }

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(c *config) { c.now = now } }

// WithNotifier installs the change callback; see Store.SetNotifier.
func WithNotifier(fn func(Change)) Option { return func(c *config) { c.notifier = fn } }

// Open opens (creating if needed) the database at path, applies pending
// migrations, and opens the blob store. path must be a file path; in-memory
// databases are not supported because reads and writes use separate connections.
func Open(path string, opts ...Option) (*Store, error) {
	cfg := config{now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	if cfg.blobDir == "" {
		cfg.blobDir = filepath.Join(filepath.Dir(abs), "blobs")
	}
	blobs, err := blob.Open(cfg.blobDir)
	if err != nil {
		return nil, err
	}
	s := &Store{
		blobs:   blobs,
		path:    abs,
		now:     cfg.now,
		spill:   SpillThreshold,
		entropy: ulid.Monotonic(crand.Reader, 0),
	}
	if cfg.notifier != nil {
		s.notify.Store(&cfg.notifier)
	}

	s.wdb, err = sql.Open("sqlite", dsn(abs, true))
	if err != nil {
		_ = blobs.Close()
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	s.wdb.SetMaxOpenConns(1)
	s.wdb.SetMaxIdleConns(1)
	s.wdb.SetConnMaxLifetime(0)

	if err := s.migrate(context.Background()); err != nil {
		_ = s.wdb.Close()
		_ = blobs.Close()
		return nil, err
	}

	s.rdb, err = sql.Open("sqlite", dsn(abs, false))
	if err != nil {
		_ = s.wdb.Close()
		_ = blobs.Close()
		return nil, fmt.Errorf("store: open reader: %w", err)
	}
	s.rdb.SetMaxOpenConns(readConns)
	s.rdb.SetMaxIdleConns(readConns)
	s.rdb.SetConnMaxLifetime(0)
	return s, nil
}

func dsn(abs string, writer bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if writer {
		q.Add("_txlock", "immediate")
	} else {
		q.Add("_pragma", "query_only(1)")
	}
	u := url.URL{Scheme: "file", Path: abs}
	return u.String() + "?" + q.Encode()
}

// Close closes the database and blob store.
func (s *Store) Close() error {
	return errors.Join(s.rdb.Close(), s.wdb.Close(), s.blobs.Close())
}

// Blobs returns the underlying blob store.
func (s *Store) Blobs() *blob.Store { return s.blobs }

// Path returns the absolute database path.
func (s *Store) Path() string { return s.path }

// Now returns the store clock's current time.
func (s *Store) Now() time.Time { return s.now().UTC() }

// SetNotifier installs (or with nil, removes) the change callback. The store
// calls it after each successful commit, once per changed row, in commit
// order, on the committing goroutine, with no store locks held. It must not
// block for long; hand off to a channel to feed SSE.
func (s *Store) SetNotifier(fn func(Change)) {
	if fn == nil {
		s.notify.Store(nil)
		return
	}
	s.notify.Store(&fn)
}

// NewID returns a new ULID string stamped with the store clock. IDs from one
// Store are strictly increasing.
func (s *Store) NewID() string {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	id, err := ulid.New(ulid.Timestamp(s.now()), s.entropy)
	if err != nil {
		// Monotonic entropy overflow within one millisecond: wait for the next one.
		time.Sleep(time.Millisecond)
		id = ulid.MustNew(ulid.Timestamp(time.Now()), s.entropy)
	}
	return id.String()
}

// dbq is satisfied by *sql.DB and *sql.Tx.
type dbq interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// txn is a write transaction that collects change notifications.
type txn struct {
	*sql.Tx
	s       *Store
	changes []Change
}

func (t *txn) note(resource, id, op string) {
	t.changes = append(t.changes, Change{Resource: resource, ID: id, Op: op})
}

// write runs fn in a serialised write transaction. Notifications fire only if
// the transaction commits. fn must not call other Store write methods.
func (s *Store) write(ctx context.Context, fn func(t *txn) error) error {
	tx, err := s.wdb.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	t := &txn{Tx: tx, s: s}
	if err := fn(t); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	if p := s.notify.Load(); p != nil {
		for _, c := range t.changes {
			(*p)(c)
		}
	}
	return nil
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// Time encoding: fixed-width UTC text so string order is time order.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func tsOrNil(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return ts(*t)
}

func parseTS(s string) (time.Time, error) {
	t, err := time.Parse(tsLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: bad timestamp %q: %w", s, err)
	}
	return t, nil
}

func parseTSPtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTS(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func limitOf(o ListOpts) int {
	switch {
	case o.Limit <= 0:
		return defaultLimit
	case o.Limit > maxLimit:
		return maxLimit
	}
	return o.Limit
}
