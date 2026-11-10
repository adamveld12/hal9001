package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"
)

// RunSpec describes the workspace a run needs.
type RunSpec struct {
	// RunID names the per-run workdir. Letters, digits, - and _ only.
	RunID string
	// Lifecycle is per_run or persistent (spec.LifecyclePerRun / LifecyclePersistent).
	Lifecycle string
	// Image selects the image for docker and kubernetes; empty means the first configured image.
	Image string
	// Repo and Ref seed the workdir with a git clone; both optional.
	Repo, Ref string
	// GitToken authenticates the clone (https remotes) and is not left in the environment or git config.
	GitToken string
	// Input is written to input.json in the workdir; nil writes nothing.
	Input []byte
	// Env is added to the backend env for this run. Its values are redacted from output.
	Env map[string]string
	// Log, when set, receives the provisioning steps and backend messages of Prepare (Manager.Open also wraps the
	// returned session so its operations are logged). Nil logs nothing.
	Log RunLog
}

// ExecResult is the outcome of one command. Output is truncated at MaxOutput bytes per stream.
type ExecResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

// Entry is one directory entry returned by Session.List.
type Entry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// Match is one Search hit.
type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Session is one run's workspace. Paths are relative to Workdir (an absolute path inside the workdir is
// accepted too); any path that resolves outside it, by ".." or by a symlink, fails with ErrEscape.
// Sessions are safe for concurrent use.
type Session interface {
	// Exec runs cmd with `sh -c` in the workdir with the run's env. A non-zero exit is not an error; a
	// timeout sets TimedOut. A zero timeout means the backend default.
	Exec(ctx context.Context, cmd string, stdin []byte, timeout time.Duration) (ExecResult, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// WriteFile creates parent directories and replaces the file.
	WriteFile(ctx context.Context, path string, data []byte) error
	// List returns the entries of a directory ("" or "." is the workdir), sorted by name.
	List(ctx context.Context, path string) ([]Entry, error)
	// Search greps files under path for a regular expression (RE2 on local, extended regex elsewhere).
	// At most MaxMatches results are returned.
	Search(ctx context.Context, path, pattern string) ([]Match, error)
	// Workdir is the absolute workdir path inside the sandbox.
	Workdir() string
	// Close ends the run. success=false keeps per_run state for debugging until Sweep removes it.
	Close(ctx context.Context, success bool) error
}

// Backend creates sessions.
type Backend interface {
	Prepare(ctx context.Context, spec RunSpec) (Session, error)
}

// Sweeper is implemented by backends whose failed per_run state outlives the run.
type Sweeper interface {
	// Sweep removes failed per_run state older than maxAge.
	Sweep(ctx context.Context, maxAge time.Duration) error
}

// Limits.
const (
	MaxOutput      = 1 << 20  // bytes kept per output stream of Exec
	MaxFileSize    = 10 << 20 // largest file ReadFile returns or WriteFile accepts
	MaxMatches     = 200
	MaxListed      = 1000
	DefaultTimeout = 10 * time.Minute
)

// Errors.
var (
	// ErrEscape means a path resolves outside the workdir.
	ErrEscape = errors.New("path escapes the sandbox workdir")
	// ErrNotFound is returned for a missing file or directory.
	ErrNotFound = fs.ErrNotExist
	// ErrTooLarge means a file exceeds MaxFileSize.
	ErrTooLarge = fmt.Errorf("file is larger than %d bytes", MaxFileSize)
)

// HostKeyError is returned by the ssh backend when no host key is pinned. Fingerprint is what the server
// presented; pin it as host_key after the user confirms it.
type HostKeyError struct {
	Fingerprint string
	Pinned      string
}

func (e *HostKeyError) Error() string {
	if e.Pinned == "" {
		return "ssh host key is not pinned; server fingerprint is " + e.Fingerprint
	}
	return fmt.Sprintf("ssh host key mismatch: pinned %s, server presented %s", e.Pinned, e.Fingerprint)
}

// minSecretLen is the shortest value that is redacted; shorter values would mangle ordinary output.
const minSecretLen = 4

// Redacted replaces secret values in output.
const Redacted = "«redacted»"

// redactor replaces secret values in text.
type redactor struct{ r *strings.Replacer }

func newRedactor(secrets []string) redactor {
	var pairs []string
	seen := map[string]bool{}
	for _, s := range secrets {
		if len(s) < minSecretLen || seen[s] {
			continue
		}
		seen[s] = true
		pairs = append(pairs, s, Redacted)
	}
	if len(pairs) == 0 {
		return redactor{}
	}
	return redactor{strings.NewReplacer(pairs...)}
}

func (r redactor) str(s string) string {
	if r.r == nil {
		return s
	}
	return r.r.Replace(s)
}

func (r redactor) bytes(b []byte) []byte {
	if r.r == nil {
		return b
	}
	return []byte(r.r.Replace(string(b)))
}

// redactBackend wraps b so that everything its sessions return has secrets removed.
type redactBackend struct {
	Backend
	secrets []string
}

func (b redactBackend) Prepare(ctx context.Context, spec RunSpec) (Session, error) {
	secrets := b.secrets
	if len(spec.Env) > 0 || spec.GitToken != "" {
		secrets = append([]string{spec.GitToken}, secrets...)
		for _, v := range spec.Env {
			secrets = append(secrets, v)
		}
	}
	red := newRedactor(secrets)
	spec.Log = withRedaction(spec.Log, secrets)
	sess, err := b.Backend.Prepare(ctx, spec)
	if err != nil {
		return nil, redactError{err, red}
	}
	return redactSession{sess, red}, nil
}

func (b redactBackend) Sweep(ctx context.Context, maxAge time.Duration) error {
	if s, ok := b.Backend.(Sweeper); ok {
		return s.Sweep(ctx, maxAge)
	}
	return nil
}

type redactError struct {
	err error
	red redactor
}

func (e redactError) Error() string { return e.red.str(e.err.Error()) }
func (e redactError) Unwrap() error { return e.err }

type redactSession struct {
	Session
	red redactor
}

func (s redactSession) Describe() SessionInfo {
	if d, ok := s.Session.(Describer); ok {
		return d.Describe()
	}
	return SessionInfo{Workdir: s.Workdir()}
}

func (s redactSession) Exec(ctx context.Context, cmd string, stdin []byte, timeout time.Duration) (ExecResult, error) {
	res, err := s.Session.Exec(ctx, cmd, stdin, timeout)
	res.Stdout, res.Stderr = s.red.str(res.Stdout), s.red.str(res.Stderr)
	return res, s.err(err)
}

func (s redactSession) ReadFile(ctx context.Context, path string) ([]byte, error) {
	b, err := s.Session.ReadFile(ctx, path)
	return s.red.bytes(b), s.err(err)
}

func (s redactSession) WriteFile(ctx context.Context, path string, data []byte) error {
	return s.err(s.Session.WriteFile(ctx, path, data))
}

func (s redactSession) List(ctx context.Context, path string) ([]Entry, error) {
	e, err := s.Session.List(ctx, path)
	return e, s.err(err)
}

func (s redactSession) Search(ctx context.Context, path, pattern string) ([]Match, error) {
	m, err := s.Session.Search(ctx, path, pattern)
	for i := range m {
		m[i].Text = s.red.str(m[i].Text)
	}
	return m, s.err(err)
}

func (s redactSession) Close(ctx context.Context, success bool) error {
	return s.err(s.Session.Close(ctx, success))
}

func (s redactSession) err(err error) error {
	if err == nil {
		return nil
	}
	return redactError{err, s.red}
}
