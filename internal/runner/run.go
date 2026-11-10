// Package runner executes sensor and command-node processes (REQUIREMENTS §4, §5):
// interpreter selection, process-group lifecycle, environment construction,
// secret redaction, output size caps, and parsing of stdout into node decisions.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Lang is the execution language of a node or sensor.
type Lang string

const (
	LangBash   Lang = "bash"
	LangPython Lang = "python"
	LangNode   Lang = "node"
	LangJQ     Lang = "jq"
	LangExec   Lang = "exec"
)

const (
	// DefaultGracePeriod is how long a process group gets between SIGTERM and SIGKILL.
	DefaultGracePeriod = 10 * time.Second
	// DefaultMaxOutput bounds each of stdout and stderr, in bytes.
	DefaultMaxOutput int64 = 16 << 20
	// drainTimeout bounds how long we wait for output pipes to reach EOF after the
	// process group has been killed (a process that called setsid can keep a pipe open).
	drainTimeout = 2 * time.Second
)

// Spec describes one process execution.
type Spec struct {
	Lang    Lang
	Script  string   // inline script for bash|python|node|jq
	Command []string // argv for exec
	// Cwd is the working directory. When empty, HAL_STATE_DIR from Env is used if set,
	// otherwise the daemon's working directory.
	Cwd string
	// Env is merged onto BaseEnv (Env wins).
	Env map[string]string
	// BaseEnv is the environment Env is merged onto. nil means DefaultBaseEnv(""); an
	// empty non-nil map means "start from nothing".
	BaseEnv map[string]string
	Stdin   []byte
	// Timeout kills the process group when exceeded. Zero means no timeout.
	Timeout time.Duration
	// GracePeriod is the SIGTERM→SIGKILL delay. Zero means DefaultGracePeriod.
	GracePeriod time.Duration
	// MaxOutput caps each of stdout and stderr, in bytes. Zero means DefaultMaxOutput.
	MaxOutput int64
	// TempDir holds the temp script file. Empty means os.TempDir().
	TempDir string
}

// Result is the outcome of one execution. A non-zero exit is not a Go error.
type Result struct {
	Stdout, Stderr []byte
	// StdoutTruncated/StderrTruncated report that output beyond MaxOutput was discarded;
	// StdoutBytes/StderrBytes are the total bytes the process wrote.
	StdoutTruncated, StderrTruncated bool
	StdoutBytes, StderrBytes         int64
	// ExitCode is the process exit status, or -1 when it died from a signal.
	ExitCode int
	// Signal names the terminating signal ("terminated", "killed", ...), if any.
	Signal   string
	Duration time.Duration
	// TimedOut is set when Spec.Timeout expired. Killed is set whenever we terminated
	// the process group (timeout or context cancellation), so Killed && !TimedOut means
	// the caller's context was cancelled.
	TimedOut bool
	Killed   bool
	// Argv is the command actually run (inline scripts show their temp file path).
	Argv []string
}

// Run executes spec and waits for it to finish, be killed, or ctx to end. The error is
// non-nil only when the process could not be run (invalid spec, missing interpreter,
// bad cwd); exit status, timeouts and kills are reported in Result.
func Run(ctx context.Context, spec Spec) (Result, error) {
	var res Result
	if err := ctx.Err(); err != nil {
		return res, err
	}

	env := mergeEnv(spec)
	argv, scriptDir, err := buildArgv(spec)
	if err != nil {
		return res, err
	}
	if scriptDir != "" {
		defer func() { _ = os.RemoveAll(scriptDir) }()
	}
	res.Argv = argv

	path, err := LookPath(argv[0], env["PATH"])
	if err != nil {
		return res, err
	}

	cwd := spec.Cwd
	if cwd == "" {
		cwd = env["HAL_STATE_DIR"]
	}

	cmd := &exec.Cmd{
		Path:        path,
		Args:        argv,
		Dir:         cwd,
		Env:         envSlice(env),
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}

	maxOut := spec.MaxOutput
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}
	stdout, stderr := &cappedBuffer{max: maxOut}, &cappedBuffer{max: maxOut}

	outR, outW, err := os.Pipe()
	if err != nil {
		return res, fmt.Errorf("runner: stdout pipe: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return res, fmt.Errorf("runner: stderr pipe: %w", err)
	}
	var inR, inW *os.File
	if len(spec.Stdin) > 0 {
		if inR, inW, err = os.Pipe(); err != nil {
			closeAll(outR, outW, errR, errW)
			return res, fmt.Errorf("runner: stdin pipe: %w", err)
		}
		cmd.Stdin = inR
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	start := time.Now()
	if err := cmd.Start(); err != nil {
		closeAll(outR, outW, errR, errW, inR, inW)
		return res, fmt.Errorf("runner: start %s: %w", argv[0], err)
	}
	// Parent keeps only the read ends (and the stdin write end).
	closeAll(outW, errW, inR)
	pgid := cmd.Process.Pid

	var readers sync.WaitGroup
	copyAsync := func(dst io.Writer, src *os.File) {
		readers.Add(1)
		go func() {
			defer readers.Done()
			_, _ = io.Copy(dst, src)
		}()
	}
	copyAsync(stdout, outR)
	copyAsync(stderr, errR)
	if inW != nil {
		go func() {
			_, _ = inW.Write(spec.Stdin) // EPIPE when the child exits early is expected
			_ = inW.Close()
		}()
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	grace := spec.GracePeriod
	if grace <= 0 {
		grace = DefaultGracePeriod
	}
	var timeout <-chan time.Time
	if spec.Timeout > 0 {
		t := time.NewTimer(spec.Timeout)
		defer t.Stop()
		timeout = t.C
	}

	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		res.Killed = true
		waitErr = terminate(pgid, done, grace)
	case <-timeout:
		res.Killed, res.TimedOut = true, true
		waitErr = terminate(pgid, done, grace)
	}

	// The leader is gone. Kill anything it left in the group so no children leak and
	// the output pipes can reach EOF.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	if inW != nil {
		_ = inW.Close()
	}
	drained := make(chan struct{})
	go func() { readers.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainTimeout):
		closeAll(outR, errR)
		<-drained
	}
	closeAll(outR, errR)
	res.Duration = time.Since(start)

	res.Stdout, res.StdoutTruncated, res.StdoutBytes = stdout.snapshot()
	res.Stderr, res.StderrTruncated, res.StderrBytes = stderr.snapshot()

	if ps := cmd.ProcessState; ps != nil {
		res.ExitCode = ps.ExitCode()
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signal = strings.ToLower(ws.Signal().String())
		}
	} else if waitErr != nil {
		return res, fmt.Errorf("runner: wait %s: %w", argv[0], waitErr)
	}
	return res, nil
}

// terminate sends SIGTERM to the process group, escalating to SIGKILL after grace.
func terminate(pgid int, done <-chan error, grace time.Duration) error {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return <-done
	}
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// buildArgv returns the argv for spec. For inline scripts it also writes the temp
// script and returns its directory for the caller to remove.
func buildArgv(spec Spec) (argv []string, scriptDir string, err error) {
	var interp []string
	var ext string
	switch spec.Lang {
	case LangExec:
		if len(spec.Command) == 0 || spec.Command[0] == "" {
			return nil, "", errors.New("runner: lang exec requires a non-empty command")
		}
		return append([]string(nil), spec.Command...), "", nil
	case LangBash:
		interp, ext = []string{"bash"}, ".sh"
	case LangPython:
		interp, ext = []string{"python3"}, ".py"
	case LangNode:
		interp, ext = []string{"node"}, ".js"
	case LangJQ:
		interp, ext = []string{"jq", "-c", "-f"}, ".jq"
	default:
		return nil, "", fmt.Errorf("runner: unknown lang %q", spec.Lang)
	}
	if strings.TrimSpace(spec.Script) == "" {
		return nil, "", fmt.Errorf("runner: lang %s requires a script", spec.Lang)
	}
	dir, err := os.MkdirTemp(spec.TempDir, "hal-run-*")
	if err != nil {
		return nil, "", fmt.Errorf("runner: temp script dir: %w", err)
	}
	file := filepath.Join(dir, "script"+ext)
	if err := os.WriteFile(file, []byte(spec.Script), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("runner: write script: %w", err)
	}
	return append(interp, file), dir, nil
}

func mergeEnv(spec Spec) map[string]string {
	base := spec.BaseEnv
	if base == nil {
		base = DefaultBaseEnv("")
	}
	env := make(map[string]string, len(base)+len(spec.Env))
	for k, v := range base {
		env[k] = v
	}
	for k, v := range spec.Env {
		env[k] = v
	}
	return env
}

func envSlice(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// LookPath resolves name against pathEnv (the child's PATH, not the daemon's:
// exec.LookPath would ignore cmd.Env), so a PATH captured from the login shell
// applies to command lookup too. A name containing "/" is returned unchanged.
func LookPath(name, pathEnv string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil // relative paths resolve against cmd.Dir
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("runner: %q not found in PATH %q: %w", name, pathEnv, exec.ErrNotFound)
}

// cappedBuffer keeps the first max bytes written and counts the rest, so a runaway
// process can neither OOM the daemon nor block on a full pipe.
type cappedBuffer struct {
	mu        sync.Mutex
	max       int64
	buf       bytes.Buffer
	total     int64
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	room := c.max - int64(c.buf.Len())
	switch {
	case room >= int64(len(p)):
		c.buf.Write(p)
	case room > 0:
		c.buf.Write(p[:room])
		c.truncated = true
	default:
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) snapshot() (data []byte, truncated bool, total int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.buf.Bytes()), c.truncated, c.total
}
