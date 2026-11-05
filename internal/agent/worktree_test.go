package agent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hal9001/internal/agent"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(cleanGitEnv(os.Environ()),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a repo with one commit on main.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestWorktreeCreateIsDetachedAtRef(t *testing.T) {
	repo := newRepo(t)
	head := git(t, repo, "rev-parse", "HEAD")
	wt := &agent.Worktrees{Dir: filepath.Join(t.TempDir(), "wt")}

	path, err := wt.Create(t.Context(), "run-a", repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, path, "rev-parse", "HEAD"); got != head {
		t.Errorf("worktree at %s, want %s", got, head)
	}
	if branch := git(t, path, "branch", "--show-current"); branch != "" {
		t.Errorf("worktree must be detached, on branch %q", branch)
	}
	// A resumed run reuses its worktree.
	if again, err := wt.Create(t.Context(), "run-a", repo, "main"); err != nil || again != path {
		t.Errorf("second Create = %q, %v", again, err)
	}
}

func TestWorktreeFetchesRemoteBeforeAdding(t *testing.T) {
	origin := newRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "clone", "-q", origin, clone)

	// origin moves on after the clone.
	if err := os.WriteFile(filepath.Join(origin, "new.txt"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", ".")
	git(t, origin, "commit", "-q", "-m", "second")
	want := git(t, origin, "rev-parse", "HEAD")

	wt := &agent.Worktrees{Dir: filepath.Join(t.TempDir(), "wt")}
	path, err := wt.Create(t.Context(), "run-b", clone, "origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, path, "rev-parse", "HEAD"); got != want {
		t.Errorf("worktree at %s, want fetched %s", got, want)
	}
}

func TestWorktreeRejectsBadKeyAndBadRef(t *testing.T) {
	repo := newRepo(t)
	wt := &agent.Worktrees{Dir: filepath.Join(t.TempDir(), "wt")}
	for _, key := range []string{"", "..", "a/b", "../escape"} {
		if _, err := wt.Create(t.Context(), key, repo, "main"); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}
	if _, err := wt.Create(t.Context(), "run-c", repo, "no-such-ref"); err == nil {
		t.Error("unknown ref accepted")
	}
	if _, err := os.Stat(filepath.Join(wt.Dir, "run-c")); err == nil {
		t.Error("failed Create left a directory behind")
	}
}

func TestWorktreePruneKeepsProtectedAndFresh(t *testing.T) {
	repo := newRepo(t)
	wt := &agent.Worktrees{Dir: filepath.Join(t.TempDir(), "wt")}
	for _, k := range []string{"old", "old-pending", "fresh"} {
		if _, err := wt.Create(t.Context(), k, repo, "main"); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-48 * time.Hour)
	for _, k := range []string{"old", "old-pending"} {
		if err := os.Chtimes(filepath.Join(wt.Dir, k), past, past); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := wt.Prune(t.Context(), 24*time.Hour, time.Now(), func(k string) bool { return k == "old-pending" })
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "old" {
		t.Errorf("removed = %v, want [old]", removed)
	}
	for k, want := range map[string]bool{"old": false, "old-pending": true, "fresh": true} {
		_, err := os.Stat(filepath.Join(wt.Dir, k))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", k, err == nil, want)
		}
	}
}

func TestResolveCwdModes(t *testing.T) {
	wt := &agent.Worktrees{Dir: filepath.Join(t.TempDir(), "wt")}
	def := filepath.Join(t.TempDir(), "state", "flow", "node")
	got, err := wt.ResolveCwd(t.Context(), agent.CwdSpec{}, "k", def)
	if err != nil || got != def {
		t.Fatalf("default cwd = %q, %v", got, err)
	}
	if st, err := os.Stat(def); err != nil || !st.IsDir() {
		t.Error("default cwd was not created")
	}
	dir := t.TempDir()
	if got, err := wt.ResolveCwd(t.Context(), agent.CwdSpec{Mode: "dir", Path: dir}, "k", def); err != nil || got != dir {
		t.Errorf("dir cwd = %q, %v", got, err)
	}
	if _, err := wt.ResolveCwd(t.Context(), agent.CwdSpec{Mode: "dir", Path: filepath.Join(dir, "missing")}, "k", def); err == nil {
		t.Error("missing dir accepted")
	}
	if _, err := wt.ResolveCwd(t.Context(), agent.CwdSpec{Mode: "bogus"}, "k", def); err == nil {
		t.Error("unknown mode accepted")
	}
}

// cleanGitEnv removes repo-selecting variables inherited when the tests run
// from a git hook, which would otherwise point every git call at the outer repo.
func cleanGitEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}
