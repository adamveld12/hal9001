package sandbox

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/spec"
)

// editSession opens a session of a backend that runs EditFile its own way: the local one (a confined root and a rename)
// or the ssh one (shell scripts, here over a test server that runs them on this machine).
func editSession(t *testing.T, backend string) Session {
	t.Helper()
	var b Backend
	switch backend {
	case "local":
		b, _ = newLocal(t, spec.LifecyclePerRun, Env{})
	case "ssh":
		srv := startSSHServer(t)
		b, _ = newSSH(t, srv, srv.fingerprint, Env{Vars: map[string]string{"SSH_PASSWORD": "pw"}})
	default:
		t.Fatalf("unknown backend %q", backend)
	}
	s, err := b.Prepare(context.Background(), RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var editBackends = []string{"local", "ssh"}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// EditFile creates a file (and its directories) or replaces it from the stored content, atomically, with no temporary
// file left behind; the file keeps its mode. When the function declines, fails or returns too much, the file is as it was.
func TestSandboxHashlineEditFileReplacesAFileAndKeepsItsMode(t *testing.T) {
	ctx := context.Background()
	for _, backend := range editBackends {
		t.Run(backend, func(t *testing.T) {
			s := editSession(t, backend)
			p := filepath.Join(s.Workdir(), "a", "b", "f.txt")

			err := s.EditFile(ctx, "a/b/f.txt", func(old RawFile) ([]byte, error) {
				if old.Exists || old.Data != nil || old.Redact == nil {
					t.Errorf("a missing file was handed over as %+v", old)
				}
				return []byte("first\n"), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(p); err != nil || string(got) != "first\n" {
				t.Fatalf("new file = %q, %v", got, err)
			}
			if got := modeOf(t, p); got != 0o640 {
				t.Errorf("a new file has mode %v; want 0640", got)
			}

			if err := os.Chmod(p, 0o600); err != nil {
				t.Fatal(err)
			}
			err = s.EditFile(ctx, "a/b/f.txt", func(old RawFile) ([]byte, error) {
				if !old.Exists || string(old.Data) != "first\n" {
					t.Errorf("the stored file was handed over as %+v", old)
				}
				return append(bytes.Clone(old.Data), "second\n"...), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(p); string(got) != "first\nsecond\n" {
				t.Errorf("edited file = %q", got)
			}
			if got := modeOf(t, p); got != 0o600 {
				t.Errorf("the edited file has mode %v; want 0600 kept", got)
			}

			refused := errors.New("refused")
			for name, tc := range map[string]struct {
				edit EditFunc
				want error
			}{
				"error":   {func(RawFile) ([]byte, error) { return []byte("lost"), refused }, refused},
				"no-op":   {func(RawFile) ([]byte, error) { return []byte("lost"), ErrNoChange }, nil},
				"too-big": {func(RawFile) ([]byte, error) { return make([]byte, MaxFileSize+1), nil }, ErrTooLarge},
			} {
				err := s.EditFile(ctx, "a/b/f.txt", tc.edit)
				if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
					t.Errorf("%s: error = %v, want %v", name, err, tc.want)
				}
				if got, _ := os.ReadFile(p); string(got) != "first\nsecond\n" {
					t.Errorf("%s: file = %q, want it as it was", name, got)
				}
			}
			ents, err := os.ReadDir(filepath.Dir(p))
			if err != nil || len(ents) != 1 || ents[0].Name() != "f.txt" {
				t.Errorf("directory holds %v, %v; want only f.txt (no temporary file)", ents, err)
			}
		})
	}
}

// A path that leaves the workdir, a symlink to somewhere else and a symlink inside the workdir are refused before the
// function sees anything, and nothing is written through them.
func TestSandboxHashlineEditFileRefusesEscapesAndSymlinks(t *testing.T) {
	ctx := context.Background()
	for _, backend := range editBackends {
		t.Run(backend, func(t *testing.T) {
			s := editSession(t, backend)
			outside := t.TempDir()
			target := filepath.Join(outside, "target.txt")
			for p, content := range map[string]string{target: "outside", filepath.Join(s.Workdir(), "real.txt"): "inside"} {
				if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for link, to := range map[string]string{"dirlink": outside, "filelink": target, "inlink": "real.txt"} {
				if err := os.Symlink(to, filepath.Join(s.Workdir(), link)); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range []string{"../x", "a/../../x", "/etc/passwd", "dirlink/new.txt", "dirlink/sub/new.txt", "filelink", "inlink"} {
				called := false
				err := s.EditFile(ctx, p, func(RawFile) ([]byte, error) {
					called = true
					return []byte("x"), nil
				})
				if !errors.Is(err, ErrEscape) {
					t.Errorf("EditFile(%q) = %v, want ErrEscape", p, err)
				}
				if called {
					t.Errorf("EditFile(%q) handed the function a file it must refuse", p)
				}
			}
			for _, gone := range []string{"new.txt", "sub"} {
				if _, err := os.Stat(filepath.Join(outside, gone)); err == nil {
					t.Errorf("%s was created outside the workdir", gone)
				}
			}
			if got, _ := os.ReadFile(target); string(got) != "outside" {
				t.Errorf("the file behind a symlink was changed: %q", got)
			}
			if got, _ := os.ReadFile(filepath.Join(s.Workdir(), "real.txt")); string(got) != "inside" {
				t.Errorf("the file behind a symlink inside the workdir was changed: %q", got)
			}
			if err := s.EditFile(ctx, ".", func(RawFile) ([]byte, error) { return []byte("x"), nil }); err == nil {
				t.Error("the workdir itself was edited as a file")
			}
		})
	}
}

// The calls of hal on one file do not interleave: a read-modify-write of each of them sees the result of the one before.
func TestSandboxHashlineEditFileSerializesCallsOnOnePath(t *testing.T) {
	ctx := context.Background()
	for _, backend := range editBackends {
		t.Run(backend, func(t *testing.T) {
			s := editSession(t, backend)
			if err := s.WriteFile(ctx, "n.txt", []byte("0\n")); err != nil {
				t.Fatal(err)
			}
			const workers = 6
			var wg sync.WaitGroup
			for range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					err := s.EditFile(ctx, "n.txt", func(old RawFile) ([]byte, error) {
						n, err := strconv.Atoi(strings.TrimSpace(string(old.Data)))
						if err != nil {
							return nil, err
						}
						return []byte(strconv.Itoa(n+1) + "\n"), nil
					})
					if err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if got, err := s.ReadFile(ctx, "n.txt"); err != nil || string(got) != strconv.Itoa(workers)+"\n" {
				t.Errorf("counter = %q, %v; want %d", got, err, workers)
			}
		})
	}
}

// On a backend that runs scripts, a file that another process changes between the read and the replacement is read again
// and the function is called with the new content; the other process's change is not overwritten.
func TestSandboxHashlineEditFileReadsAgainWhenTheFileChangedUnderIt(t *testing.T) {
	ctx := context.Background()
	s := editSession(t, "ssh")
	if err := s.WriteFile(ctx, "f.txt", []byte("v1\n")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.Workdir(), "f.txt")
	var seen []string
	err := s.EditFile(ctx, "f.txt", func(old RawFile) ([]byte, error) {
		seen = append(seen, string(old.Data))
		if len(seen) == 1 {
			if err := os.WriteFile(p, []byte("v2 by another process\n"), 0o600); err != nil {
				t.Error(err)
			}
		}
		return append(bytes.Clone(old.Data), "mine\n"...), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "v1\n" || seen[1] != "v2 by another process\n" {
		t.Errorf("the function saw %q, want the first read and then the changed file", seen)
	}
	if got, _ := os.ReadFile(p); string(got) != "v2 by another process\nmine\n" {
		t.Errorf("file = %q, want the other process's change kept", got)
	}

	// A function that refuses the new content (its request was made against the old one) leaves the other change alone.
	stale := errors.New("stale")
	err = s.EditFile(ctx, "f.txt", func(RawFile) ([]byte, error) {
		if len(seen) == 2 {
			seen = append(seen, "")
			if err := os.WriteFile(p, []byte("v3 by another process\n"), 0o600); err != nil {
				t.Error(err)
			}
			return []byte("based on v2\n"), nil
		}
		return nil, stale
	})
	if !errors.Is(err, stale) {
		t.Errorf("error = %v, want the refusal of the second call", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v3 by another process\n" {
		t.Errorf("file = %q, want the last change of the other process", got)
	}
}

// The agent-facing read and the errors hide secret values, but what an edit is handed and writes is the stored bytes: the
// secrets in the file survive an edit of other lines. RawFile.Redact keeps the line structure, also for a secret that
// spans lines.
func TestSandboxHashlineEditNeverRedactsWhatItWrites(t *testing.T) {
	ctx := context.Background()
	const secret = "s3cr3t-value"
	const multi = "-----BEGIN KEY-----\nabcd1234efgh\n-----END KEY-----"
	b, _ := newLocal(t, spec.LifecyclePerRun, Env{Vars: map[string]string{"API_KEY": secret}, Secrets: []string{secret, multi}})
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	content := "key = " + secret + "\n" + multi + "\nplain = 1\n"
	if err := s.WriteFile(ctx, "f.txt", []byte(content)); err != nil {
		t.Fatal(err)
	}

	if got, err := s.ReadFile(ctx, "f.txt"); err != nil || strings.Contains(string(got), secret) || strings.Contains(string(got), "abcd1234efgh") {
		t.Errorf("ReadFile shows a secret: %q, %v", got, err)
	}
	raw, err := s.ReadFileRaw(ctx, "f.txt")
	if err != nil || string(raw.Data) != content || !raw.Exists {
		t.Fatalf("ReadFileRaw = %q, %v; want the stored bytes", raw.Data, err)
	}
	hidden := raw.Redact(content)
	if strings.Contains(hidden, secret) || strings.Contains(hidden, "abcd1234efgh") || !strings.Contains(hidden, Redacted) {
		t.Errorf("Redact left a secret: %q", hidden)
	}
	if strings.Count(hidden, "\n") != strings.Count(content, "\n") {
		t.Errorf("Redact moved the lines: %d line breaks became %d (%q)", strings.Count(content, "\n"), strings.Count(hidden, "\n"), hidden)
	}

	err = s.EditFile(ctx, "f.txt", func(old RawFile) ([]byte, error) {
		if string(old.Data) != content {
			t.Errorf("the edit was handed %q, want the stored bytes", old.Data)
		}
		return []byte(strings.Replace(string(old.Data), "plain = 1", "plain = 2", 1)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(s.Workdir(), "f.txt")); string(got) != strings.Replace(content, "plain = 1", "plain = 2", 1) {
		t.Errorf("file after the edit = %q, want the secrets kept", got)
	}

	err = s.EditFile(ctx, "f.txt", func(RawFile) ([]byte, error) { return nil, errors.New("bad line: " + secret) })
	if err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), Redacted) {
		t.Errorf("error = %v, want the secret value redacted", err)
	}
}

// An edit is one log entry with the size of the new file and what the call said it did; a refused edit is a warning with
// the (redacted) message; a hidden preview is not logged; no content reaches the log.
func TestSandboxHashlineEditIsLoggedWithoutContent(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	writeLocalCfg(t, home, "enabled: true\nroot: "+filepath.Join(t.TempDir(), "root")+"\nlifecycle: per_run\n")
	if _, err := spec.WriteFile(home, spec.KindSandboxEnv, "local", []byte("API_TOKEN=s3cr3t-value\n"), ""); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerOptions{Home: home})
	defer m.Close()
	lg := &collectLog{}
	s, err := m.Open(ctx, "local", RunSpec{RunID: "r1", Log: lg})
	if err != nil {
		t.Fatal(err)
	}

	tctx := WithCall(ctx, CallInfo{Tool: "fs_write", Approval: "auto: test", Detail: "2 edits"})
	const written = "hello s3cr3t-value\n"
	if err := s.EditFile(tctx, "a.txt", func(RawFile) ([]byte, error) { return []byte(written), nil }); err != nil {
		t.Fatal(err)
	}
	e := lg.find(KindFS, "write a.txt")
	if e == nil || e.Tool != "fs_write" || e.Approval != "auto: test" || e.Path != "a.txt" || e.Level != "" || e.Bytes == nil || *e.Bytes != int64(len(written)) ||
		!strings.Contains(e.Title, "2 edits") || !strings.Contains(e.Title, strconv.Itoa(len(written))+" bytes") {
		t.Fatalf("edit entry = %+v", e)
	}

	err = s.EditFile(tctx, "a.txt", func(RawFile) ([]byte, error) { return nil, errors.New("line 1 holds s3cr3t-value") })
	if err == nil {
		t.Fatal("a refused edit succeeded")
	}
	refused := lg.find(KindFS, "not written")
	if refused == nil || refused.Level != LevelWarn || refused.Bytes != nil || !strings.Contains(refused.Error, Redacted) {
		t.Fatalf("refused edit entry = %+v", refused)
	}

	before := len(lg.entries)
	if err := s.EditFile(WithCall(ctx, CallInfo{Hidden: true}), "a.txt", func(RawFile) ([]byte, error) { return nil, ErrNoChange }); err != nil {
		t.Fatal(err)
	}
	if len(lg.entries) != before {
		t.Errorf("a hidden preview was logged: %+v", lg.entries[before:])
	}
	if err := s.EditFile(tctx, "a.txt", func(RawFile) ([]byte, error) { return nil, ErrNoChange }); err != nil {
		t.Fatal(err)
	}
	if lg.find(KindFS, "no changes") == nil {
		t.Errorf("an edit that changes nothing is not logged: %s", lg.dump())
	}
	if strings.Contains(lg.dump(), "s3cr3t-value") {
		t.Errorf("a secret value reached the log: %s", lg.dump())
	}
}

// The backend that has no stdin and no binary-safe output (Depot) edits through base64: a file with a NUL byte and
// long lines is replaced intact.
func TestSandboxHashlineEditFileOverDepot(t *testing.T) {
	ctx := context.Background()
	fake := &fakeDepot{home: t.TempDir()}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	b := depotBackendFor(t, srv, spec.LifecyclePerRun, "tok-depot")
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close(ctx, true) }()

	payload := "a\x00b\nlong " + strings.Repeat("x", 500) + "\n"
	if err := s.EditFile(ctx, "d/e.bin", func(old RawFile) ([]byte, error) {
		if old.Exists {
			t.Errorf("a missing file was handed over as %+v", old)
		}
		return []byte(payload), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EditFile(ctx, "d/e.bin", func(old RawFile) ([]byte, error) {
		if string(old.Data) != payload {
			t.Errorf("the stored file was handed over as %q", old.Data)
		}
		return append(bytes.Clone(old.Data), "tail\n"...), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(ctx, "d/e.bin"); err != nil || string(got) != payload+"tail\n" {
		t.Errorf("file = %q, %v", got, err)
	}
	if err := s.EditFile(ctx, "../outside", func(RawFile) ([]byte, error) { return []byte("x"), nil }); !errors.Is(err, ErrEscape) {
		t.Errorf("escape = %v", err)
	}
}
