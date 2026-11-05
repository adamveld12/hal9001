package store

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClaimEventExactlyOneWinnerUnderRace(t *testing.T) {
	s, _ := newTestStore(t)
	const racers = 64
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, racers)
	for range racers {
		wg.Go(func() {
			<-start
			ok, err := s.ClaimEvent(ctx(), "github", "123:2026-01-01")
			if err != nil {
				errs <- err
				return
			}
			if ok {
				wins.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if wins.Load() != 1 {
		t.Fatalf("%d goroutines saw the key as new, want exactly 1", wins.Load())
	}
	// Keys are per sensor.
	if ok, err := s.ClaimEvent(ctx(), "slack", "123:2026-01-01"); err != nil || !ok {
		t.Errorf("same key on another sensor: new=%v err=%v", ok, err)
	}
}

func TestIngestEventRaceCreatesOneEvent(t *testing.T) {
	s, _ := newTestStore(t)
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			ok, err := s.IngestEvent(ctx(), &Event{SensorID: "s", DedupKey: "same", Payload: Inline(fmt.Sprintf(`{"n":%d}`, i))})
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		})
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created = %d, want 1", created.Load())
	}
	page, err := s.ListEvents(ctx(), EventFilter{SensorID: "s"})
	must(t, err)
	if len(page.Items) != 1 {
		t.Fatalf("stored events = %d, want 1", len(page.Items))
	}
}

func TestBaselineKeysSuppressLaterEvents(t *testing.T) {
	s, _ := newTestStore(t)
	// first_run: baseline marks keys seen without creating events...
	if ok, err := s.ClaimEvent(ctx(), "gh", "n1"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// ...so the same item polled later is not new,
	if ok, err := s.IngestEvent(ctx(), &Event{SensorID: "gh", DedupKey: "n1"}); err != nil || ok {
		t.Fatalf("baselined key produced an event: new=%v err=%v", ok, err)
	}
	// while a changed key (e.g. new updated_at) is.
	if ok, err := s.IngestEvent(ctx(), &Event{SensorID: "gh", DedupKey: "n1:v2"}); err != nil || !ok {
		t.Fatalf("new key rejected: new=%v err=%v", ok, err)
	}
}

func TestEventPaginationIsStableUnderInserts(t *testing.T) {
	s, _ := newTestStore(t)
	var all []string
	for i := range 25 {
		all = append(all, mkEvent(t, s, "s", fmt.Sprintf("k%02d", i), "{}").ID)
	}

	var got []string
	opts := ListOpts{Limit: 10}
	for page := 0; ; page++ {
		p, err := s.ListEvents(ctx(), EventFilter{ListOpts: opts})
		must(t, err)
		for _, e := range p.Items {
			got = append(got, e.ID)
		}
		if page == 0 {
			// New events arriving between page fetches must not shift later pages.
			mkEvent(t, s, "s", "arrived-late-1", "{}")
			mkEvent(t, s, "s", "arrived-late-2", "{}")
		}
		if p.NextCursor == "" {
			break
		}
		if len(p.Items) != 10 {
			t.Fatalf("non-final page has %d items", len(p.Items))
		}
		opts.Cursor = p.NextCursor
	}
	if len(got) != 25 {
		t.Fatalf("paged %d events, want exactly the original 25 (no dupes, no skips): %v", len(got), got)
	}
	for i, id := range got { // newest first
		if want := all[24-i]; id != want {
			t.Fatalf("item %d = %s, want %s", i, id, want)
		}
	}

	// Oldest-first walks forward from a cursor.
	p, err := s.ListEvents(ctx(), EventFilter{ListOpts: ListOpts{Oldest: true, Limit: 3, Cursor: all[1]}})
	must(t, err)
	if len(p.Items) != 3 || p.Items[0].ID != all[2] || p.NextCursor != all[4] {
		t.Errorf("oldest-first page = %v next=%s", ids(p.Items), p.NextCursor)
	}
}

func ids(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func TestListLimitIsClamped(t *testing.T) {
	s, _ := newTestStore(t)
	for i := range 3 {
		mkEvent(t, s, "s", fmt.Sprint(i), "{}")
	}
	p, err := s.ListEvents(ctx(), EventFilter{ListOpts: ListOpts{Limit: 100000}})
	must(t, err)
	if len(p.Items) != 3 || p.NextCursor != "" {
		t.Errorf("items=%d next=%q", len(p.Items), p.NextCursor)
	}
	p, err = s.ListEvents(ctx(), EventFilter{ListOpts: ListOpts{Limit: -5}})
	must(t, err)
	if len(p.Items) != 3 {
		t.Errorf("negative limit returned %d items", len(p.Items))
	}
}

func TestEventTextSearchAndFilters(t *testing.T) {
	s, clk := newTestStore(t)
	a := mkEvent(t, s, "github", "n1", `{"subject":{"title":"Fix Flaky Test"}}`)
	clk.Advance(time.Hour)
	b := mkEvent(t, s, "slack", "C1:100", `{"text":"deploy is 100% done_ok"}`)
	clk.Advance(time.Hour)
	c := mkEvent(t, s, "github", "n2", `{"subject":{"title":"Update docs"}}`)
	flow := mkFlowRun(t, s, "triage", c.ID, FlowRunQueued)
	_ = flow

	search := func(q string, f EventFilter) []string {
		t.Helper()
		f.Search = q
		p, err := s.ListEvents(ctx(), f)
		must(t, err)
		return ids(p.Items)
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("got %v want %v", got, want)
		}
	}
	eq(search("flaky", EventFilter{}), a.ID)     // case-insensitive payload match
	eq(search("C1:100", EventFilter{}), b.ID)    // dedup key match
	eq(search("100%", EventFilter{}), b.ID)      // % is literal, not a wildcard
	eq(search("done_ok", EventFilter{}), b.ID)   // _ is literal too
	eq(search("%", EventFilter{}), b.ID)         // only b contains a percent sign
	eq(search("nothing-matches", EventFilter{})) // no match
	eq(search("subject", EventFilter{SensorID: "github"}), c.ID, a.ID)
	eq(search("", EventFilter{FlowID: "triage"}), c.ID)
	eq(search("", EventFilter{Time: TimeRange{Since: clk.Now().Add(-90 * time.Minute), Until: clk.Now().Add(-30 * time.Minute)}}), b.ID)

	// Searching flow runs goes through the event payload.
	fr, err := s.ListFlowRuns(ctx(), FlowRunFilter{Search: "UPDATE DOCS"})
	must(t, err)
	if len(fr.Items) != 1 || fr.Items[0].EventID != c.ID {
		t.Errorf("flow run search = %+v", fr.Items)
	}
	fr, err = s.ListFlowRuns(ctx(), FlowRunFilter{Search: "flaky"})
	must(t, err)
	if len(fr.Items) != 0 {
		t.Errorf("flow run search matched an event with no run: %+v", fr.Items)
	}
}
