package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hal.db")
	s, err := Open(path)
	must(t, err)
	v1, err := s.SchemaVersion(ctx())
	must(t, err)
	if v1 < 1 {
		t.Fatalf("schema version = %d after Open", v1)
	}
	ev := &Event{SensorID: "s", DedupKey: "k", Payload: Inline(`{"a":1}`)}
	if ok, err := s.IngestEvent(ctx(), ev); err != nil || !ok {
		t.Fatalf("ingest: %v %v", ok, err)
	}
	must(t, s.Close())

	// Reopening must not re-run migrations (which would fail on existing tables
	// or wipe data) and must keep the data.
	s, err = Open(path)
	must(t, err)
	defer func() { _ = s.Close() }()
	v2, err := s.SchemaVersion(ctx())
	must(t, err)
	if v2 != v1 {
		t.Errorf("version changed across reopen: %d -> %d", v1, v2)
	}
	var rows int
	must(t, s.rdb.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&rows))
	migs, err := loadMigrations()
	must(t, err)
	if rows != len(migs) {
		t.Errorf("schema_migrations rows = %d, want %d", rows, len(migs))
	}
	if _, err := s.GetEvent(ctx(), ev.ID); err != nil {
		t.Errorf("event lost across reopen: %v", err)
	}
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hal.db")
	s, err := Open(path)
	must(t, err)
	_, err = s.wdb.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, 'from_the_future', 'x')`)
	must(t, err)
	must(t, s.Close())
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Open of newer schema = %v, want a 'newer' error", err)
	}
}

func TestOpenAtPathWithSpacesAndConcurrentOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "with space", "data")
	path := filepath.Join(dir, "hal.db")
	var wg sync.WaitGroup
	stores := make([]*Store, 4)
	errs := make([]error, 4)
	for i := range stores {
		wg.Go(func() { stores[i], errs[i] = Open(path) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Open #%d: %v", i, err)
		}
		defer func() { _ = stores[i].Close() }()
	}
	if _, err := stores[0].ClaimEvent(ctx(), "s", "k"); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionsUseWALForeignKeysAndBusyTimeout(t *testing.T) {
	s, _ := newTestStore(t)
	var mode string
	must(t, s.wdb.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
	if mode != "wal" {
		t.Errorf("journal_mode = %q", mode)
	}
	var fk, busy int
	must(t, s.wdb.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	must(t, s.rdb.QueryRow(`PRAGMA foreign_keys`).Scan(&fk))
	must(t, s.wdb.QueryRow(`PRAGMA busy_timeout`).Scan(&busy))
	if fk != 1 || busy < 1000 {
		t.Errorf("foreign_keys=%d busy_timeout=%d", fk, busy)
	}
	// Foreign keys are enforced: a node run needs an existing flow run.
	err := s.CreateNodeRun(ctx(), &NodeRun{FlowRunID: "missing", NodeID: "n", NodeType: NodeCommand})
	if err == nil {
		t.Error("node run with missing flow run was accepted")
	}
	// The reader pool cannot write.
	if _, err := s.rdb.Exec(`DELETE FROM events`); err == nil {
		t.Error("reader connection accepted a write")
	}
}

func TestEnumsRejectedByGoAndByDatabase(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.CreateFlowRun(ctx(), &FlowRun{FlowID: "f", FlowHash: "h", FlowYAML: "x", Status: "weird"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid status via Go: %v, want ErrInvalid", err)
	}
	if err := s.CreateFlowRun(ctx(), &FlowRun{FlowID: "f", FlowHash: "h", FlowYAML: "x", Kind: "weird"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid kind via Go: %v, want ErrInvalid", err)
	}
	// Bypass Go validation: the CHECK constraint is the second line of defence.
	_, err := s.wdb.Exec(`INSERT INTO flow_versions (flow_id, hash, yaml, first_seen) VALUES ('f','h','y','t')`)
	must(t, err)
	_, err = s.wdb.Exec(`INSERT INTO flow_runs (id, flow_id, flow_hash, kind, status, created_at) VALUES ('x','f','h','live','bogus','t')`)
	if err == nil {
		t.Error("CHECK constraint did not reject status 'bogus'")
	}
	if err := s.CreateAgentRun(ctx(), &AgentRun{NodeRunID: "n", Runtime: "r", Mode: "sneaky", Policy: PolicyAuto, Phase: PhaseExecute}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid agent mode: %v", err)
	}
	if err := s.CreateInboxItem(ctx(), &InboxItem{Title: "t", Priority: "meh", SourceType: SourceNodeRun, NodeRunID: "n"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid priority: %v", err)
	}
}

func TestNotifierFiresAfterCommitOnly(t *testing.T) {
	var mu sync.Mutex
	var got []Change
	s, _ := newTestStore(t, WithNotifier(func(c Change) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	}))
	ev := mkEvent(t, s, "sensor", "k1", `{}`)
	fr := mkFlowRun(t, s, "flow", ev.ID, FlowRunQueued)

	// A rolled-back transaction must not notify.
	before := len(got)
	err := s.write(ctx(), func(tx *txn) error {
		tx.note(ResEvent, "ghost", OpCreate)
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	mu.Lock()
	rolledBack := len(got) != before
	mu.Unlock()
	if rolledBack {
		t.Error("notifier fired for a rolled-back transaction")
	}

	// A duplicate (non-new) event is not a change.
	before = len(got)
	if ok, err := s.IngestEvent(ctx(), &Event{SensorID: "sensor", DedupKey: "k1"}); err != nil || ok {
		t.Fatalf("duplicate ingest: %v %v", ok, err)
	}
	if len(got) != before {
		t.Error("notifier fired for a deduplicated event")
	}

	fr.Status = FlowRunRunning
	must(t, s.UpdateFlowRun(ctx(), fr))

	mu.Lock()
	defer mu.Unlock()
	want := []Change{
		{ResEvent, ev.ID, OpCreate},
		{ResFlowVersion, "flow/h1", OpCreate},
		{ResFlowRun, fr.ID, OpCreate},
		{ResFlowRun, fr.ID, OpUpdate},
	}
	if len(got) < len(want) {
		t.Fatalf("changes = %+v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("change[%d] = %+v, want %+v", i, got[i], w)
		}
	}

	// The notifier can be removed.
	s.SetNotifier(nil)
	n := len(got)
	mu.Unlock()
	mkEvent(t, s, "sensor", "k2", `{}`)
	mu.Lock()
	if len(got) != n {
		t.Error("notifier still firing after SetNotifier(nil)")
	}
}

func TestConcurrentReadersAndWritersNeverFail(t *testing.T) {
	s, _ := newTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for w := range 8 {
		wg.Go(func() {
			for i := range 15 {
				ev := &Event{SensorID: "s", DedupKey: strings.Repeat("k", w+1) + string(rune('a'+i)), Payload: Inline("{}")}
				if _, err := s.IngestEvent(ctx(), ev); err != nil {
					errs <- err
				}
			}
		})
		wg.Go(func() {
			for range 15 {
				if _, err := s.ListEvents(ctx(), EventFilter{Search: "x"}); err != nil {
					errs <- err
				}
				if _, err := s.Counts(ctx()); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent op failed: %v", err)
	}
	page, err := s.ListEvents(ctx(), EventFilter{ListOpts: ListOpts{Limit: 500}})
	must(t, err)
	if len(page.Items) != 8*15 {
		t.Errorf("events = %d, want %d", len(page.Items), 8*15)
	}
}

func TestBlobColumnListMatchesSchema(t *testing.T) {
	s, _ := newTestStore(t)
	listed := map[string]bool{}
	for _, bc := range blobColumns {
		listed[bc.table+"."+bc.col] = true
	}
	rows, err := s.rdb.Query(`SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND p.name LIKE '%\_ref' ESCAPE '\'`)
	must(t, err)
	defer func() { _ = rows.Close() }()
	found := map[string]bool{}
	for rows.Next() {
		var tbl, col string
		must(t, rows.Scan(&tbl, &col))
		found[tbl+"."+col] = true
		if !listed[tbl+"."+col] {
			t.Errorf("column %s.%s holds blob refs but is missing from blobColumns; GC would delete live blobs", tbl, col)
		}
	}
	must(t, rows.Err())
	for k := range listed {
		if !found[k] {
			t.Errorf("blobColumns lists %s which does not exist", k)
		}
	}
}
