package engine_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"hal9001/internal/core"
	"hal9001/internal/store"
)

// An already-seen item whose event was pruned is stored again as an event of
// the manual run and replayed; the dedup state does not change.
func TestRerunSeenStoresAPrunedEventAgain(t *testing.T) {
	h := newHarness(t)
	items := filepath.Join(t.TempDir(), "items.jsonl")
	if err := os.WriteFile(items, []byte(`{"id":"a"}`+"\n"+`{"id":"b"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.sensor("src", "id: src\nlang: bash\nscript: cat "+items+"\nkey: \"{{.id}}\"\nfirst_run: fire\ntimeout: 10s\n")
	h.start()

	// No flow yet: the poll stores events and runs nothing.
	first, err := h.eng.PollSensor(t.Context(), "src", core.PollOptions{})
	if err != nil || first.NewEvents != 2 || len(first.FlowRunIDs) != 0 {
		t.Fatalf("first poll: %+v %v", first, err)
	}
	if _, err := h.st.Prune(t.Context(), time.Now().AddDate(0, 0, 30), 7, 3650); err != nil {
		t.Fatal(err)
	}
	if n := len(h.events("src")); n != 0 {
		t.Fatalf("events after pruning: %d, want 0", n)
	}

	h.flow("f", flowYAML("f", "src"))
	h.reload()
	res, err := h.eng.PollSensor(t.Context(), "src", core.PollOptions{RerunSeen: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.NewEvents != 0 || res.RerunEvents != 2 || len(res.FlowRunIDs) != 2 {
		t.Fatalf("rerun poll: %+v", res)
	}
	evs := h.events("src")
	if len(evs) != 2 {
		t.Fatalf("events after rerun: %d, want the 2 items stored again", len(evs))
	}
	for _, ev := range evs {
		if ev.SensorRunID != res.Run.ID {
			t.Errorf("event %s belongs to run %q, want the manual run %s", ev.DedupKey, ev.SensorRunID, res.Run.ID)
		}
	}
	for _, id := range res.FlowRunIDs {
		if fr := h.waitRun(id, store.FlowRunSucceeded); fr.Kind != store.KindReplay {
			t.Errorf("run %s kind %s, want replay", id, fr.Kind)
		}
	}
	again, err := h.eng.PollSensor(t.Context(), "src", core.PollOptions{})
	if err != nil || again.NewEvents != 0 || again.Run.NewCount != 0 {
		t.Fatalf("dedup state changed by the rerun: %+v %v", again, err)
	}
}
