package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktrees manages the per-run git worktrees of `cwd: {mode: worktree}` (§7.5).
type Worktrees struct {
	// Dir is data/worktrees; one subdirectory per run key.
	Dir string
	// Git is the git binary (default "git").
	Git string
}

func (w *Worktrees) git() string {
	if w.Git != "" {
		return w.Git
	}
	return "git"
}

// Path is where the worktree for key lives.
func (w *Worktrees) Path(key string) (string, error) {
	if key == "" || key != filepath.Base(key) || key == "." || key == ".." {
		return "", fmt.Errorf("invalid worktree key %q", key)
	}
	return filepath.Join(w.Dir, key), nil
}

func (w *Worktrees) run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, w.git(), args...) //nolint:gosec // fixed git subcommands, repo paths come from node config
	cmd.Dir = dir
	cmd.Env = repoScopedEnv(os.Environ())
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// ExpandHome resolves a leading ~ in a configured path.
func ExpandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// Create fetches the repo's remotes and adds a detached worktree at ref under
// Dir/key, returning its path. A worktree already at that path (a resumed
// run) is reused. A repo without remotes has nothing to fetch.
func (w *Worktrees) Create(ctx context.Context, key, repo, ref string) (string, error) {
	path, err := w.Path(key)
	if err != nil {
		return "", err
	}
	if repo, err = ExpandHome(repo); err != nil {
		return "", err
	}
	if ref == "" {
		return "", errors.New("worktree ref is empty")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	remotes, err := w.run(ctx, repo, "remote")
	if err != nil {
		return "", err
	}
	if remotes != "" {
		if _, err := w.run(ctx, repo, "fetch", "--quiet"); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(w.Dir, 0o750); err != nil {
		return "", err
	}
	if _, err := w.run(ctx, repo, "worktree", "add", "--detach", "--quiet", path, ref); err != nil {
		return "", err
	}
	return path, nil
}

// Remove deletes the worktree for key and unregisters it from its repo.
func (w *Worktrees) Remove(ctx context.Context, key string) error {
	path, err := w.Path(key)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	common, err := w.run(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if _, err := w.run(ctx, filepath.Dir(common), "worktree", "remove", "--force", path); err != nil {
		return err
	}
	return nil
}

// Release applies the cleanup rule when a run ends: the worktree is removed
// when the run succeeded and kept otherwise (failed runs stay for debugging,
// runs awaiting approval need it for phase 2). Retention pruning removes the
// rest.
func (w *Worktrees) Release(ctx context.Context, key string, st Status) error {
	if st != StatusSucceeded {
		return nil
	}
	return w.Remove(ctx, key)
}

// Prune removes worktrees untouched for longer than maxAge, except keys for
// which keep returns true (pending approvals, starred inbox items). It
// returns the removed keys.
func (w *Worktrees) Prune(ctx context.Context, maxAge time.Duration, now time.Time, keep func(key string) bool) ([]string, error) {
	entries, err := os.ReadDir(w.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if now.Sub(info.ModTime()) < maxAge || (keep != nil && keep(e.Name())) {
			continue
		}
		if err := w.Remove(ctx, e.Name()); err != nil {
			// Not a registered worktree any more (repo deleted): drop the dir.
			if rerr := os.RemoveAll(filepath.Join(w.Dir, e.Name())); rerr != nil {
				errs = append(errs, errors.Join(err, rerr))
				continue
			}
		}
		removed = append(removed, e.Name())
	}
	return removed, errors.Join(errs...)
}

// CwdSpec is the resolved `cwd` of an agent node (templates already rendered).
type CwdSpec struct {
	// Mode is "dir", "worktree", or "" for the default directory.
	Mode string
	Path string // dir mode
	Repo string // worktree mode
	Ref  string // worktree mode
}

// ResolveCwd returns the directory an agent run starts in. key identifies the
// run (the worktree directory name); defaultDir is data/state/<flow>/<node>/ and
// is created if missing.
func (w *Worktrees) ResolveCwd(ctx context.Context, spec CwdSpec, key, defaultDir string) (string, error) {
	switch spec.Mode {
	case "dir":
		p, err := ExpandHome(spec.Path)
		if err != nil {
			return "", err
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return "", fmt.Errorf("cwd %q is not an existing directory", p)
		}
		return p, nil
	case "worktree":
		return w.Create(ctx, key, spec.Repo, spec.Ref)
	case "":
		if err := os.MkdirAll(defaultDir, 0o750); err != nil {
			return "", err
		}
		return defaultDir, nil
	}
	return "", fmt.Errorf("unknown cwd mode %q", spec.Mode)
}

// repoScopedEnv drops the variables that make git operate on a repository other
// than the one in the working directory (they are set, for example, when the
// daemon is started from inside a git hook).
func repoScopedEnv(env []string) []string {
	drop := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_COMMON_DIR": true,
		"GIT_PREFIX": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_NAMESPACE": true, "GIT_CEILING_DIRECTORIES": true,
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	return out
}
