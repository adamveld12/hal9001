package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeShell(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fakeshell")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return p
}

func TestLoginShellPATHIgnoresRCNoise(t *testing.T) {
	// Real login shells print banners; the marker framing must survive them. The fake
	// also checks it is invoked as `-lic <script>`.
	sh := fakeShell(t, `[ "$1" = "-lic" ] || exit 9
echo "Welcome back"
PATH=/opt/homebrew/bin:/usr/bin
eval "$2"
echo "trailing noise"`)
	got, err := LoginShellPATH(t.Context(), sh, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/opt/homebrew/bin:/usr/bin" {
		t.Errorf("PATH = %q", got)
	}
}

func TestLoginShellPATHUsesSHELLEnvByDefault(t *testing.T) {
	sh := fakeShell(t, `PATH=/from/shell; eval "$2"`)
	t.Setenv("SHELL", sh)
	got, err := LoginShellPATH(t.Context(), "", 0)
	if err != nil || got != "/from/shell" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestLoginShellPATHFailures(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := LoginShellPATH(t.Context(), fakeShell(t, "sleep 30"), 200*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("took %s", time.Since(start))
		}
	})
	t.Run("non-zero exit reports stderr", func(t *testing.T) {
		_, err := LoginShellPATH(t.Context(), fakeShell(t, "echo rc broke >&2; exit 4"), 0)
		if err == nil || !strings.Contains(err.Error(), "rc broke") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no marker", func(t *testing.T) {
		if _, err := LoginShellPATH(t.Context(), fakeShell(t, "echo hi"), 0); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("missing shell", func(t *testing.T) {
		if _, err := LoginShellPATH(t.Context(), "/no/such/shell", 0); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestLoginShellPATHWithRealShell(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real login shell")
	}
	got, err := LoginShellPATH(t.Context(), "/bin/sh", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/usr/bin") {
		t.Errorf("PATH = %q", got)
	}
}

func TestEffectivePATHAddsMissingWellKnownDirsAfterTheCapturedOnes(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o750); err != nil {
		t.Fatal(err)
	}
	// ~/.local/share/mise/shims is deliberately absent: only directories that exist are added.
	got := EffectivePATH("/custom/bin:/usr/bin:/custom/bin", home)
	parts := filepath.SplitList(got)
	if parts[0] != "/custom/bin" || parts[1] != "/usr/bin" {
		t.Errorf("captured entries must keep their order and be deduplicated: %q", got)
	}
	idx := map[string]int{}
	for i, p := range parts {
		if _, dup := idx[p]; dup {
			t.Errorf("duplicate entry %q in %q", p, got)
		}
		idx[p] = i
	}
	if i, ok := idx[localBin]; !ok || i < 2 {
		t.Errorf("%s should be appended after the captured entries: %q", localBin, got)
	}
	if _, ok := idx[filepath.Join(home, ".local", "share", "mise", "shims")]; ok {
		t.Errorf("a missing directory was added: %q", got)
	}
}

func TestLookPathSearchesTheGivenPATHNotTheProcessPATH(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "only-on-effective-path")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	got, err := LookPath("only-on-effective-path", "/nonexistent:"+dir)
	if err != nil || got != exe {
		t.Errorf("LookPath = %q, %v; want %q", got, err, exe)
	}
	if _, err := LookPath("only-on-effective-path", "/usr/bin:/bin"); err == nil {
		t.Error("resolved a command that is on neither PATH")
	}
}
