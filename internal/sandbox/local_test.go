package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/spec"
)

func newLocal(t *testing.T, lifecycle string, env Env) (Backend, string) {
	t.Helper()
	root := t.TempDir()
	cfg := spec.DefaultSandbox(spec.SandboxLocal)
	cfg.Enabled, cfg.Root, cfg.Lifecycle = true, root, lifecycle
	b, err := New(spec.SandboxLocal, cfg, env, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b, root
}

func TestLocalExecFilesAndSeeding(t *testing.T) {
	ctx := context.Background()
	b, _ := newLocal(t, spec.LifecyclePerRun, Env{Vars: map[string]string{"GREETING": "hi"}})
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1", Input: []byte(`{"a":1}`), Env: map[string]string{"EXTRA": "x1"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Exec(ctx, `echo "$GREETING $EXTRA"; echo oops >&2; pwd; cat; exit 3`, []byte("in"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 || !strings.Contains(res.Stdout, "hi x1") || !strings.Contains(res.Stdout, s.Workdir()) || !strings.HasSuffix(res.Stdout, "in") || strings.TrimSpace(res.Stderr) != "oops" {
		t.Fatalf("bad result %+v", res)
	}
	if got, err := s.ReadFile(ctx, "input.json"); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("input.json = %q, %v", got, err)
	}
	if err := s.WriteFile(ctx, "sub/dir/a.txt", []byte("hello needle\nsecond")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadFile(ctx, filepath.Join(s.Workdir(), "sub/dir/a.txt")); string(got) != "hello needle\nsecond" {
		t.Fatalf("absolute path read = %q", got)
	}
	ents, err := s.List(ctx, "sub")
	if err != nil || len(ents) != 1 || ents[0].Name != "dir" || !ents[0].IsDir {
		t.Fatalf("list = %+v, %v", ents, err)
	}
	hits, err := s.Search(ctx, ".", `needle`)
	if err != nil || len(hits) != 1 || hits[0].Path != "sub/dir/a.txt" || hits[0].Line != 1 {
		t.Fatalf("search = %+v, %v", hits, err)
	}
	if _, err := s.ReadFile(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestLocalTimeout(t *testing.T) {
	b, _ := newLocal(t, spec.LifecyclePerRun, Env{})
	s, err := b.Prepare(context.Background(), RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := s.Exec(context.Background(), "sleep 30", nil, 200*time.Millisecond)
	if err != nil || !res.TimedOut || time.Since(start) > 5*time.Second {
		t.Fatalf("res=%+v err=%v after %s", res, err, time.Since(start))
	}
}

func TestLocalEscapesAreRejected(t *testing.T) {
	ctx := context.Background()
	b, root := newLocal(t, spec.LifecyclePerRun, Env{})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Workdir(), "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(s.Workdir(), "flink")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../x", "a/../../x", "/etc/passwd", filepath.Join(root, "runs", "other")} {
		if _, err := s.ReadFile(ctx, p); !errors.Is(err, ErrEscape) {
			t.Errorf("ReadFile(%q) error = %v, want ErrEscape", p, err)
		}
		if err := s.WriteFile(ctx, p, []byte("x")); !errors.Is(err, ErrEscape) {
			t.Errorf("WriteFile(%q) error = %v, want ErrEscape", p, err)
		}
	}
	for _, p := range []string{"link/secret.txt", "flink"} {
		if _, err := s.ReadFile(ctx, p); !errors.Is(err, ErrEscape) {
			t.Errorf("ReadFile(%q) through symlink error = %v, want ErrEscape", p, err)
		}
	}
	if err := s.WriteFile(ctx, "link/new.txt", []byte("x")); !errors.Is(err, ErrEscape) {
		t.Errorf("WriteFile through symlink error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Error("write escaped the workdir")
	}
	if _, err := s.List(ctx, "link"); !errors.Is(err, ErrEscape) {
		t.Errorf("List through symlink error = %v", err)
	}
	hits, _ := s.Search(ctx, ".", "top secret")
	if len(hits) != 0 {
		t.Errorf("search followed a symlink: %+v", hits)
	}
}

func TestLocalLifecycle(t *testing.T) {
	ctx := context.Background()
	b, root := newLocal(t, spec.LifecyclePerRun, Env{})
	ok, _ := b.Prepare(ctx, RunSpec{RunID: "good"})
	bad, _ := b.Prepare(ctx, RunSpec{RunID: "bad"})
	_ = ok.WriteFile(ctx, "f", []byte("x"))
	_ = bad.WriteFile(ctx, "f", []byte("x"))
	_ = ok.Close(ctx, true)
	_ = bad.Close(ctx, false)
	if _, err := os.Stat(filepath.Join(root, "runs", "good")); !os.IsNotExist(err) {
		t.Error("per_run workdir of a successful run was kept")
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "bad", "f")); err != nil {
		t.Error("per_run workdir of a failed run was removed")
	}
	_ = os.Chtimes(filepath.Join(root, "runs", "bad"), time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	if err := b.(Sweeper).Sweep(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "runs", "bad")); !os.IsNotExist(err) {
		t.Error("Sweep kept an old failed workdir")
	}

	p, _ := newLocal(t, spec.LifecyclePersistent, Env{})
	s, _ := p.Prepare(ctx, RunSpec{RunID: "one"})
	_ = s.WriteFile(ctx, "keep.txt", []byte("x"))
	_ = s.Close(ctx, true)
	s2, _ := p.Prepare(ctx, RunSpec{RunID: "two"})
	if got, err := s2.ReadFile(ctx, "keep.txt"); err != nil || string(got) != "x" {
		t.Errorf("persistent workdir lost its files: %q %v", got, err)
	}
}

func TestSecretsAreRedacted(t *testing.T) {
	ctx := context.Background()
	b, _ := newLocal(t, spec.LifecyclePerRun, Env{
		Vars:    map[string]string{"API_KEY": "s3cr3t-value", "NAME": "plain-value"},
		Secrets: []string{"s3cr3t-value"},
	})
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1", Env: map[string]string{"RUN_SECRET": "run-level-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := s.Exec(ctx, `echo "$API_KEY $NAME $RUN_SECRET"; echo "$API_KEY" > f.txt; echo "$API_KEY" >&2`, nil, 0)
	if strings.Contains(res.Stdout+res.Stderr, "s3cr3t-value") || strings.Contains(res.Stdout, "run-level-secret") || !strings.Contains(res.Stdout, "plain-value") {
		t.Fatalf("output not redacted: %+v", res)
	}
	got, _ := s.ReadFile(ctx, "f.txt")
	if strings.Contains(string(got), "s3cr3t-value") || !strings.Contains(string(got), Redacted) {
		t.Fatalf("file not redacted: %q", got)
	}
	hits, _ := s.Search(ctx, ".", "s3cr3t")
	if len(hits) != 1 || strings.Contains(hits[0].Text, "s3cr3t-value") {
		t.Fatalf("search not redacted: %+v", hits)
	}
}

func TestCommandsNeverSeeBackendCredentials(t *testing.T) {
	env := Env{Vars: map[string]string{"DEPOT_TOKEN": "tok-123456", "OTHER": "ok"}}
	if got := env.commandVars(spec.SandboxDepot); got["DEPOT_TOKEN"] != "" || got["OTHER"] != "ok" {
		t.Fatalf("commandVars = %v", got)
	}
}

func TestSeedingClonesRepoAtRef(t *testing.T) {
	if testing.Short() {
		t.Skip("runs git")
	}
	ctx := context.Background()
	src := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"},
	} {
		runGit(t, src, args...)
	}
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("one"), 0o600)
	runGit(t, src, "add", ".")
	runGit(t, src, "commit", "-qm", "one")
	runGit(t, src, "tag", "v1")
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("two"), 0o600)
	runGit(t, src, "commit", "-qam", "two")

	b, _ := newLocal(t, spec.LifecyclePerRun, Env{})
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1", Repo: src, Ref: "v1", Input: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadFile(ctx, "a.txt"); string(got) != "one" {
		t.Fatalf("a.txt = %q, want the content at v1", got)
	}
	if _, err := s.ReadFile(ctx, "input.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Prepare(ctx, RunSpec{RunID: "r2", Repo: filepath.Join(src, "nope")}); err == nil {
		t.Fatal("clone of a missing repo succeeded")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := execCommand("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

var execCommand = exec.Command
