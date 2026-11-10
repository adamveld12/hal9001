package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"hal9001/internal/spec"
)

// localBackend runs commands on this machine in a directory tree.
type localBackend struct {
	root      string
	lifecycle string
	timeout   time.Duration
	env       map[string]string
}

func (b *localBackend) Prepare(ctx context.Context, rs RunSpec) (Session, error) {
	if !spec.ValidID(rs.RunID) {
		return nil, fmt.Errorf("invalid run id %q", rs.RunID)
	}
	lifecycle := orDefault(rs.Lifecycle, b.lifecycle)
	dir := b.root
	if lifecycle == spec.LifecyclePerRun {
		dir = filepath.Join(b.root, "runs", rs.RunID)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	phys, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(phys)
	if err != nil {
		return nil, err
	}
	s := &localSession{
		dir: phys, root: root, perRun: lifecycle == spec.LifecyclePerRun, timeout: b.timeout,
		env: mergeEnv(b.env, rs.Env), info: SessionInfo{Name: phys, Workdir: phys},
	}
	logStep(rs.Log, "Created workdir "+phys, nil)
	if err := seed(ctx, s, rs); err != nil {
		_ = s.Close(ctx, false)
		return nil, err
	}
	return s, nil
}

// Sweep removes run directories left by failed runs.
func (b *localBackend) Sweep(_ context.Context, maxAge time.Duration) error {
	entries, err := os.ReadDir(filepath.Join(b.root, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			_ = os.RemoveAll(filepath.Join(b.root, "runs", e.Name()))
		}
	}
	return nil
}

type localSession struct {
	dir     string
	root    *os.Root
	perRun  bool
	timeout time.Duration
	env     map[string]string
	info    SessionInfo
}

func (s *localSession) Workdir() string { return s.dir }

func (s *localSession) Describe() SessionInfo { return s.info }

func (s *localSession) Exec(ctx context.Context, cmd string, stdin []byte, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = s.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd) //nolint:gosec // running the agent's command is the purpose of a sandbox
	c.Dir = s.dir
	c.Env = append(os.Environ(), envList(s.env)...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = 2 * time.Second
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errb limitBuffer
	out.limit, errb.limit = MaxOutput, MaxOutput
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	res := ExecResult{Stdout: out.String(), Stderr: errb.String(), Truncated: out.wasTruncated() || errb.wasTruncated()}
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut, res.ExitCode = true, -1
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
	case err != nil && !errors.Is(err, exec.ErrWaitDelay):
		return res, err
	}
	return res, nil
}

func (s *localSession) rel(p string) (string, error) { return relPath(s.dir, p) }

func mapRootErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "escapes from parent") {
		return ErrEscape
	}
	return err
}

func (s *localSession) ReadFile(_ context.Context, p string) ([]byte, error) {
	r, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(r)
	if err != nil {
		return nil, mapRootErr(err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", p)
	}
	if st.Size() > MaxFileSize {
		return nil, ErrTooLarge
	}
	return io.ReadAll(io.LimitReader(f, MaxFileSize+1))
}

func (s *localSession) WriteFile(_ context.Context, p string, data []byte) error {
	if len(data) > MaxFileSize {
		return ErrTooLarge
	}
	r, err := s.rel(p)
	if err != nil {
		return err
	}
	if r == "." {
		return fmt.Errorf("%s is a directory", p)
	}
	if dir := path.Dir(r); dir != "." {
		if err := s.root.MkdirAll(dir, 0o750); err != nil {
			return mapRootErr(err)
		}
	}
	return mapRootErr(s.root.WriteFile(r, data, 0o640))
}

func (s *localSession) List(_ context.Context, p string) ([]Entry, error) {
	r, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	ents, err := fs.ReadDir(s.root.FS(), r)
	if err != nil {
		return nil, mapRootErr(err)
	}
	var out []Entry
	for _, e := range ents {
		if len(out) == MaxListed {
			break
		}
		en := Entry{Name: e.Name(), IsDir: e.IsDir()}
		if info, err := e.Info(); err == nil && !e.IsDir() {
			en.Size = info.Size()
		}
		out = append(out, en)
	}
	return out, nil
}

func (s *localSession) Search(ctx context.Context, p, pattern string) ([]Match, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}
	r, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	fsys := s.root.FS()
	var out []Match
	errDone := errors.New("done")
	walkErr := fs.WalkDir(fsys, r, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			if fp == r {
				return err
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := fs.ReadFile(fsys, fp)
		if err != nil || len(b) > MaxFileSize || bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			if re.MatchString(line) {
				out = append(out, Match{Path: fp, Line: i + 1, Text: strings.TrimRight(line, "\r")})
				if len(out) >= MaxMatches {
					return errDone
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errDone) {
		return out, mapRootErr(walkErr)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *localSession) Close(_ context.Context, success bool) error {
	err := s.root.Close()
	if s.perRun && success {
		if rerr := os.RemoveAll(s.dir); rerr != nil && err == nil {
			err = rerr
		}
	}
	return err
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
