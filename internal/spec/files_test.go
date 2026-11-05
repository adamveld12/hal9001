package spec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHash(t *testing.T) {
	// sha256("abc"), lowercase hex, no prefix.
	if got := Hash([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Hash = %s", got)
	}
}

func TestWriteFlowConflictDetection(t *testing.T) {
	home := t.TempDir()
	v1 := []byte("id: a\n")

	h1, err := WriteFlow(home, "a", v1, "")
	if err != nil {
		t.Fatal(err)
	}
	if h1 != Hash(v1) {
		t.Errorf("returned hash %s, want hash of content", h1)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "flows", "a.yaml")); string(b) != "id: a\n" {
		t.Errorf("content = %q", b)
	}

	// Create-only must not overwrite.
	if _, err := WriteFlow(home, "a", []byte("id: other\n"), ""); !errors.Is(err, ErrConflict) {
		t.Errorf("create over existing: %v, want ErrConflict", err)
	}

	// Matching hash updates.
	v2 := []byte("id: a\nenabled: true\n")
	h2, err := WriteFlow(home, "a", v2, h1)
	if err != nil {
		t.Fatal(err)
	}

	// Someone edits the file on disk; the UI still holds h2.
	edited := []byte("id: a\nenabled: false # edited by hand\n")
	if err := os.WriteFile(filepath.Join(home, "flows", "a.yaml"), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = WriteFlow(home, "a", []byte("id: a\nui: save\n"), h2)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale hash: %v, want ErrConflict", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "flows", "a.yaml")); string(b) != string(edited) {
		t.Errorf("conflicting write changed the file: %q", b)
	}

	// Writing against the new hash works.
	if _, err := WriteFlow(home, "a", []byte("id: a\nmerged: true\n"), Hash(edited)); err != nil {
		t.Errorf("write with current hash: %v", err)
	}
}

func TestWriteConflictWhenFileWasDeleted(t *testing.T) {
	home := t.TempDir()
	h, _ := WriteSensor(home, "s", []byte("id: s\n"), "")
	if err := os.Remove(filepath.Join(home, "sensors", "s.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteSensor(home, "s", []byte("id: s\n"), h); !errors.Is(err, ErrConflict) {
		t.Errorf("write after delete: %v, want ErrConflict", err)
	}
}

func TestWriteLeavesNoTempFilesAndKeepsMode(t *testing.T) {
	home := t.TempDir()
	if _, err := WriteEnv(home, []byte("TOKEN=x\n"), ""); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(home, "env"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("env mode: %v %v", st, err)
	}
	if err := os.Chmod(filepath.Join(home, "env"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteEnv(home, []byte("TOKEN=y\n"), Hash([]byte("TOKEN=x\n"))); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(home, "env")); st.Mode().Perm() != 0o640 {
		t.Errorf("existing mode not preserved: %v", st.Mode())
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestWriteThroughSymlink(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "flows"), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(repo, "a.yaml")
	if err := os.WriteFile(target, []byte("id: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "flows", "a.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFlow(home, "a", []byte("id: a\nenabled: true\n"), Hash([]byte("id: a\n"))); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink was replaced by a regular file: %v %v", st, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "id: a\nenabled: true\n" {
		t.Errorf("target content = %q", b)
	}
}

func TestWriteRejectsUnsafeIDs(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"", "../x", "a/b", ".hidden", "a b", "a.yaml"} {
		if _, err := WriteFlow(home, id, []byte("x"), ""); !errors.Is(err, ErrInvalidID) {
			t.Errorf("id %q: %v, want ErrInvalidID", id, err)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("rejected write created files: %v", entries)
	}
}

func TestDeleteFile(t *testing.T) {
	home := t.TempDir()
	h, _ := WritePrompt(home, "p", []byte("x"), "")
	if err := DeleteFile(home, KindPrompt, "p", "stale"); !errors.Is(err, ErrConflict) {
		t.Errorf("stale delete: %v", err)
	}
	if err := DeleteFile(home, KindPrompt, "p", ""); !errors.Is(err, ErrConflict) {
		t.Errorf("unconditional delete must be refused: %v", err)
	}
	if err := DeleteFile(home, KindPrompt, "p", h); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadFile(home, KindPrompt, "p"); !errors.Is(err, ErrNotFound) {
		t.Errorf("read after delete: %v", err)
	}
}

func TestReadFileReturnsHash(t *testing.T) {
	home := t.TempDir()
	h, _ := WriteConfig(home, []byte("max_agent_runs: 3\n"), "")
	b, got, err := ReadFile(home, KindConfig, "")
	if err != nil || got != h || string(b) != "max_agent_runs: 3\n" {
		t.Errorf("ReadFile = %q %s %v", b, got, err)
	}
}
