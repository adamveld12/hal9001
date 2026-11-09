package spec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameRefsRewritesOnlyTheReferences(t *testing.T) {
	src := "# c\nsubscribe: [a, \"b\", ab]  # keep\nnodes:\n  - {id: a, type: agent, prompt: a, tools: [a, {id: a, approve: never}, b]}\n  - id: a2\n    type: inbox\n    title: a\n"
	got, ok := RenameRefs([]byte(src), KindSensor, "a", "z")
	if !ok || string(got) != "# c\nsubscribe: [z, \"b\", ab]  # keep\nnodes:\n  - {id: a, type: agent, prompt: a, tools: [a, {id: a, approve: never}, b]}\n  - id: a2\n    type: inbox\n    title: a\n" {
		t.Errorf("sensor refs:\n%s", got)
	}
	got, ok = RenameRefs([]byte(src), KindTool, "a", "z")
	if !ok || string(got) != "# c\nsubscribe: [a, \"b\", ab]  # keep\nnodes:\n  - {id: a, type: agent, prompt: a, tools: [z, {id: z, approve: never}, b]}\n  - id: a2\n    type: inbox\n    title: a\n" {
		t.Errorf("tool refs:\n%s", got)
	}
	got, ok = RenameRefs([]byte(src), KindPrompt, "a", "z")
	if !ok || string(got) != "# c\nsubscribe: [a, \"b\", ab]  # keep\nnodes:\n  - {id: a, type: agent, prompt: z, tools: [a, {id: a, approve: never}, b]}\n  - id: a2\n    type: inbox\n    title: a\n" {
		t.Errorf("prompt refs:\n%s", got)
	}
	if _, ok := RenameRefs([]byte(src), KindSensor, "nope", "z"); ok {
		t.Error("reported a change without a match")
	}
}

func TestRenameFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, DirSensors), 0o750); err != nil {
		t.Fatal(err)
	}
	h, err := WriteSensor(home, "a", []byte("id: a\nlang: bash\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSensor(home, "taken", []byte("lang: bash\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RenameFile(home, KindSensor, "a", "taken", h); !errors.Is(err, ErrConflict) {
		t.Errorf("target exists: %v", err)
	}
	if _, err := RenameFile(home, KindSensor, "a", "b", "0000"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale hash: %v", err)
	}
	if _, err := RenameFile(home, KindSensor, "ghost", "b", h); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing source: %v", err)
	}
	if _, err := RenameFile(home, KindSensor, "a", "bad id", h); !errors.Is(err, ErrInvalidID) {
		t.Errorf("invalid id: %v", err)
	}
	nh, err := RenameFile(home, KindSensor, "a", "b", h)
	if err != nil {
		t.Fatal(err)
	}
	got, hash, err := ReadFile(home, KindSensor, "b")
	if err != nil || string(got) != "lang: bash\n" || hash != nh {
		t.Errorf("renamed file = %q %v", got, err)
	}
	if _, _, err := ReadFile(home, KindSensor, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old file: %v", err)
	}
}
