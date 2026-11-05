package spec

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func startWatch(t *testing.T, home string) <-chan Reload {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch, err := Watch(ctx, home, WithDebounce(40*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

func next(t *testing.T, ch <-chan Reload) Reload {
	t.Helper()
	select {
	case r, ok := <-ch:
		if !ok {
			t.Fatal("watch channel closed")
		}
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no reload notification within 5s")
	}
	return Reload{}
}

func expectQuiet(t *testing.T, ch <-chan Reload) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("unexpected reload: %v", r.Paths)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWatchFiresOnEdit(t *testing.T) {
	home := t.TempDir()
	if _, err := Init(home, nil); err != nil {
		t.Fatal(err)
	}
	put(t, home, "flows/a.yaml", "id: a\n")
	time.Sleep(100 * time.Millisecond) // let the writes above settle before watching
	ch := startWatch(t, home)

	// In-place edit.
	put(t, home, "flows/a.yaml", "id: a\nenabled: true\n")
	if got := next(t, ch).Paths; !slices.Equal(got, []string{"flows/a.yaml"}) {
		t.Errorf("edit: paths = %v", got)
	}

	// Atomic replace, as the API and many editors save.
	if _, err := WriteFlow(home, "a", []byte("id: a\nenabled: false\n"), Hash([]byte("id: a\nenabled: true\n"))); err != nil {
		t.Fatal(err)
	}
	if got := next(t, ch).Paths; !slices.Equal(got, []string{"flows/a.yaml"}) {
		t.Errorf("atomic write: paths = %v", got)
	}

	// New file, in another watched directory, and config.yaml.
	put(t, home, "prompts/p.md", "---\nname: p\n---\nb\n")
	if got := next(t, ch).Paths; !slices.Equal(got, []string{"prompts/p.md"}) {
		t.Errorf("new prompt: paths = %v", got)
	}
	put(t, home, "config.yaml", "max_agent_runs: 3\n")
	if got := next(t, ch).Paths; !slices.Equal(got, []string{"config.yaml"}) {
		t.Errorf("config: paths = %v", got)
	}

	// Deletion is a change.
	if err := os.Remove(filepath.Join(home, "flows", "a.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := next(t, ch).Paths; !slices.Equal(got, []string{"flows/a.yaml"}) {
		t.Errorf("delete: paths = %v", got)
	}
}

func TestWatchDebouncesBurstsIntoOneReload(t *testing.T) {
	home := t.TempDir()
	if _, err := Init(home, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	ch := startWatch(t, home)

	for i := range 5 {
		put(t, home, "flows/a.yaml", "id: a\n# "+string(rune('a'+i))+"\n")
		put(t, home, "sensors/s.yaml", "id: s\n# "+string(rune('a'+i))+"\n")
	}
	got := next(t, ch).Paths
	if !slices.Equal(got, []string{"flows/a.yaml", "sensors/s.yaml"}) {
		t.Errorf("burst paths = %v, want both files once, sorted", got)
	}
	expectQuiet(t, ch)
}

func TestWatchIgnoresTempAndUnrelatedFiles(t *testing.T) {
	home := t.TempDir()
	if _, err := Init(home, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	ch := startWatch(t, home)

	put(t, home, "flows/.a.yaml.tmp-123", "x")
	put(t, home, "flows/a.yaml~", "x")
	put(t, home, "flows/notes.txt", "x")
	put(t, home, "data/state/x/scratch", "x")
	put(t, home, "data/hal.db", "x")
	put(t, home, "README.md", "x")
	expectQuiet(t, ch)
}

func TestWatchPicksUpDirectoriesCreatedLater(t *testing.T) {
	home := t.TempDir() // no sensors/ yet
	ch := startWatch(t, home)

	put(t, home, "sensors/s.yaml", "id: s\n")
	got := next(t, ch).Paths
	if !slices.Contains(got, "sensors/s.yaml") {
		t.Fatalf("file in a new directory missed: %v", got)
	}

	put(t, home, "sensors/t.yaml", "id: t\n")
	if got := next(t, ch).Paths; !slices.Equal(got, []string{"sensors/t.yaml"}) {
		t.Errorf("later file in the new directory: %v", got)
	}
}

func TestWatchStopsWithContext(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Watch(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("unexpected reload")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after cancel")
	}
}

func TestWatchMissingHome(t *testing.T) {
	if _, err := Watch(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("watching a missing home must fail")
	}
}

func TestClassifyPath(t *testing.T) {
	for rel, want := range map[string]struct {
		k  Kind
		id string
		ok bool
	}{
		"config.yaml":       {KindConfig, "", true},
		"env":               {KindEnv, "", true},
		"flows/a.yaml":      {KindFlow, "a", true},
		"sensors/b-c.yaml":  {KindSensor, "b-c", true},
		"prompts/p.md":      {KindPrompt, "p", true},
		"flows/.a.yaml.tmp": {"", "", false},
		"flows/a.txt":       {"", "", false},
		"flows/sub/a.yaml":  {"", "", false},
		"data/hal.db":       {"", "", false},
		"prompts/p.yaml":    {"", "", false},

		"env.d/sensors/linear.env":      {KindSensorEnv, "linear", true},
		"env.d/sensors/.linear.env.tmp": {"", "", false},
		"env.d/sensors/linear.txt":      {"", "", false},
		"env.d/sensors/sub/x.env":       {"", "", false},
		"env.d/other/x.env":             {"", "", false},
	} {
		k, id, ok := ClassifyPath(rel)
		if k != want.k || id != want.id || ok != want.ok {
			t.Errorf("ClassifyPath(%q) = %q %q %v", rel, k, id, ok)
		}
	}
}
