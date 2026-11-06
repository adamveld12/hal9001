package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hal9001/internal/spec"
)

func writeLocalCfg(t *testing.T, home, yaml string) {
	t.Helper()
	if _, err := spec.WriteFile(home, spec.KindSandbox, "local", []byte(yaml), ""); err != nil {
		p := spec.KindSandbox.Path(home, "local")
		if werr := os.WriteFile(p, []byte(yaml), 0o600); werr != nil {
			t.Fatal(werr)
		}
	}
}

func TestManagerOpenReloadAndSweep(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "root")
	m := NewManager(ManagerOptions{Home: home, RetentionDays: func() int { return 1 }})
	defer m.Close()

	if _, err := m.Open(ctx, "local", RunSpec{RunID: "r1"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Open on a disabled backend = %v", err)
	}
	writeLocalCfg(t, home, "enabled: true\nroot: "+root+"\n")
	s, err := m.Open(ctx, "local", RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(root, "runs", "r1")); s.Workdir() != want {
		t.Fatalf("workdir = %s, want per_run dir %s", s.Workdir(), want)
	}
	_ = s.Close(ctx, false) // failed run: kept

	m.mu.Lock()
	first := m.backends["local"].backend
	m.mu.Unlock()
	if b, _, _ := m.get("local"); b != first {
		t.Fatal("backend was rebuilt without a change")
	}

	// An env change is picked up (the backend is rebuilt and sees the new variable).
	if _, err := spec.WriteFile(home, spec.KindSandboxEnv, "local", []byte("# hal:plain GREETING\nGREETING=hello\n"), ""); err != nil {
		t.Fatal(err)
	}
	m.Reload()
	s2, err := m.Open(ctx, "local", RunSpec{RunID: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ := s2.Exec(ctx, "echo $GREETING", nil, 0); res.Stdout != "hello\n" {
		t.Fatalf("env not applied after reload: %+v", res)
	}
	_ = s2.Close(ctx, true)

	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(filepath.Join(root, "runs", "r1"), old, old)
	m.Sweep(ctx)
	if _, err := os.Stat(filepath.Join(root, "runs", "r1")); !os.IsNotExist(err) {
		t.Fatal("expired failed workspace was not swept")
	}

	writeLocalCfg(t, home, "enabled: false\n")
	m.Reload()
	if _, err := m.Open(ctx, "local", RunSpec{RunID: "r3"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Open after disabling = %v", err)
	}
}
