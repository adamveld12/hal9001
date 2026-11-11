package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Log entry kinds and levels (RunLog consumers store them as is).
const (
	KindLifecycle = "lifecycle" // provision, clone, ready, teardown
	KindExec      = "exec"      // a command
	KindFS        = "fs"        // a file operation
	KindDenied    = "denied"    // a call that was not run
	KindBackend   = "backend"   // messages from the backend itself (image pull, pod events)

	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Session statuses reported through RunLog.Update.
const (
	StatusProvisioning = "provisioning"
	StatusReady        = "ready"
	StatusFailed       = "failed" // provisioning failed
	StatusClosed       = "closed"
)

// Limits of one log entry.
const (
	// LogOutputLimit caps the stdout and the stderr of one entry: the first half and the last half are kept.
	LogOutputLimit = 64 << 10
	logTitleLimit  = 160
)

// LogEntry is one line of a sandbox run log.
type LogEntry struct {
	Kind  string
	Level string // default info
	// Title is a one-line summary.
	Title string
	// Tool is the hal tool the entry belongs to (shell, fs_write, a custom tool id).
	Tool string
	// Approval says how the call was allowed or why it was not run.
	Approval string
	// Command is the command line, or the pattern of a search.
	Command string
	Cwd     string
	Path    string
	// ExitCode is set for a command that ran to the end.
	ExitCode *int
	// Duration of the operation; zero means unknown.
	Duration time.Duration
	// Bytes is the size of the file read or written, or the number of entries or hits listed.
	Bytes     *int64
	Stdout    string
	Stderr    string
	TimedOut  bool
	Truncated bool
	Error     string
	// Fields are small key/value details (image, repo, ...).
	Fields map[string]string
}

// SessionInfo describes a sandbox. Backends fill what they know.
type SessionInfo struct {
	// ID is the backend's id of the sandbox (the Depot sandbox id); Name is the container, pod or host name.
	ID, Name  string
	Image     string
	Repo, Ref string
	Lifecycle string
	Workdir   string
	EnvNames  []string
}

// SessionUpdate changes the header of a run's sandbox log. Empty fields are left alone.
type SessionUpdate struct {
	Status string
	Info   SessionInfo
	Error  string
}

// RunLog receives the log of one sandbox session (RunSpec.Log). Implementations are safe for concurrent use and
// must not block for long; they own redaction beyond what this package already does and any size limits.
type RunLog interface {
	Add(LogEntry)
	Update(SessionUpdate)
}

// Describer is implemented by sessions that know their sandbox's identity.
type Describer interface {
	Describe() SessionInfo
}

// CallInfo annotates the sandbox operations a tool call makes. Put it on the context passed to Session methods.
type CallInfo struct {
	// Tool names the tool (shell, fs_write, a custom tool id).
	Tool string
	// Approval says how the call was allowed.
	Approval string
	// Command replaces the command line shown in the log: a tool script wrapped in exports of secret values must
	// not be logged as sent.
	Command string
	// Hidden keeps the operation out of the log (hal's own helper files).
	Hidden bool
}

type callKey struct{}

// WithCall returns ctx carrying ci.
func WithCall(ctx context.Context, ci CallInfo) context.Context {
	return context.WithValue(ctx, callKey{}, ci)
}

// CallFrom returns the CallInfo on ctx (zero when there is none).
func CallFrom(ctx context.Context) CallInfo {
	ci, _ := ctx.Value(callKey{}).(CallInfo)
	return ci
}

func logAdd(l RunLog, e LogEntry) {
	if l != nil {
		l.Add(e)
	}
}

func logStep(l RunLog, title string, fields map[string]string) {
	logAdd(l, LogEntry{Kind: KindLifecycle, Title: title, Fields: fields})
}

func logBackend(l RunLog, title string, fields map[string]string) {
	logAdd(l, LogEntry{Kind: KindBackend, Title: title, Fields: fields})
}

// CapText keeps at most limit bytes of s: the first half and the last half, with a marker for what was left out. The
// second result reports whether anything was cut.
func CapText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	half := limit / 2
	// Cutting may split a UTF-8 sequence; drop the pieces.
	head, tail := strings.ToValidUTF8(s[:half], ""), strings.ToValidUTF8(s[len(s)-half:], "")
	return fmt.Sprintf("%s\n[… %d bytes omitted …]\n%s", head, len(s)-len(head)-len(tail), tail), true
}

// oneLine shortens s to its first line, at most logTitleLimit bytes.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	line, rest, more := strings.Cut(s, "\n")
	cut := len(line) > logTitleLimit
	if cut {
		line = strings.ToValidUTF8(line[:logTitleLimit], "")
	}
	if cut || more && strings.TrimSpace(rest) != "" {
		line += " …"
	}
	return line
}

// CleanRepo removes credentials from a clone URL.
func CleanRepo(repo string) string {
	if u, err := url.Parse(repo); err == nil && u.User != nil && u.Host != "" {
		u.User = nil
		return u.String()
	}
	return repo
}

// redactLog removes secret values from everything written to l.
type redactLog struct {
	l   RunLog
	red redactor
}

func (r redactLog) Add(e LogEntry) {
	e.Title, e.Approval, e.Command, e.Cwd, e.Path = r.red.str(e.Title), r.red.str(e.Approval), r.red.str(e.Command), r.red.str(e.Cwd), r.red.str(e.Path)
	e.Stdout, e.Stderr, e.Error = r.red.str(e.Stdout), r.red.str(e.Stderr), r.red.str(e.Error)
	if len(e.Fields) > 0 {
		f := make(map[string]string, len(e.Fields))
		for k, v := range e.Fields {
			f[k] = r.red.str(v)
		}
		e.Fields = f
	}
	r.l.Add(e)
}

func (r redactLog) Update(u SessionUpdate) {
	u.Error = r.red.str(u.Error)
	u.Info.Workdir, u.Info.Repo, u.Info.Name, u.Info.ID = r.red.str(u.Info.Workdir), r.red.str(u.Info.Repo), r.red.str(u.Info.Name), r.red.str(u.Info.ID)
	r.l.Update(u)
}

// withRedaction wraps l (which may be nil) so secrets are removed from what it receives.
func withRedaction(l RunLog, secrets []string) RunLog {
	if l == nil {
		return nil
	}
	return redactLog{l, newRedactor(secrets)}
}

// loggedSession records every operation of the session it wraps in the run log. log may be nil.
type loggedSession struct {
	Session
	log       RunLog
	backend   string
	lifecycle string
	secrets   []string    // values redacted from what a log adopted later receives
	meta      SessionInfo // what the session was opened with
}

// Adopt returns a session for the sandbox of sess that logs to log from now on, and the log sess used before (nil
// when it had none). sess must come from Manager.Open and must not be used afterwards: closing the returned session
// tears the sandbox down and records that on log. log gets the same secret redaction the first log had. Adopt does not
// write to either log: the caller records the hand-over.
func Adopt(sess Session, log RunLog) (Session, RunLog, bool) {
	ls, ok := sess.(*loggedSession)
	if !ok {
		return nil, nil, false
	}
	return &loggedSession{Session: ls.Session, log: withRedaction(log, ls.secrets), backend: ls.backend, lifecycle: ls.lifecycle, secrets: ls.secrets, meta: ls.meta}, ls.log, true
}

func ms(d time.Duration) int64 { return d.Milliseconds() }

// Describe is the backend's description of the sandbox plus what the session was opened with (image, repo, ref,
// lifecycle, environment variable names).
func (s *loggedSession) Describe() SessionInfo {
	info := SessionInfo{Workdir: s.Workdir()}
	if d, ok := s.Session.(Describer); ok {
		info = d.Describe()
	}
	fill := func(dst *string, v string) {
		if *dst == "" {
			*dst = v
		}
	}
	fill(&info.Image, s.meta.Image)
	fill(&info.Repo, s.meta.Repo)
	fill(&info.Ref, s.meta.Ref)
	fill(&info.Lifecycle, s.meta.Lifecycle)
	if len(info.EnvNames) == 0 {
		info.EnvNames = s.meta.EnvNames
	}
	return info
}

func (s *loggedSession) Exec(ctx context.Context, cmd string, stdin []byte, timeout time.Duration) (ExecResult, error) {
	ci := CallFrom(ctx)
	start := time.Now()
	res, err := s.Session.Exec(ctx, cmd, stdin, timeout)
	if ci.Hidden {
		return res, err
	}
	shown := cmd
	if ci.Command != "" {
		shown = ci.Command
	}
	e := LogEntry{
		Kind: KindExec, Tool: ci.Tool, Approval: ci.Approval, Command: shown, Cwd: s.Workdir(), Duration: time.Since(start),
		TimedOut: res.TimedOut, Truncated: res.Truncated,
	}
	var cutOut, cutErr bool
	e.Stdout, cutOut = CapText(res.Stdout, LogOutputLimit)
	e.Stderr, cutErr = CapText(res.Stderr, LogOutputLimit)
	e.Truncated = e.Truncated || cutOut || cutErr
	title := "$ " + oneLine(shown)
	switch {
	case err != nil:
		e.Level, e.Error = LevelError, err.Error()
		title += " (failed to run)"
	case res.TimedOut:
		e.Level = LevelError
		title += " (timed out)"
	case res.ExitCode != 0:
		e.Level = LevelWarn
		code := res.ExitCode
		e.ExitCode = &code
		title += fmt.Sprintf(" (exit %d)", code)
	default:
		code := res.ExitCode
		e.ExitCode = &code
	}
	e.Title = title
	logAdd(s.log, e)
	return res, err
}

func (s *loggedSession) fsEntry(ctx context.Context, tool, title, path string, start time.Time, n int64, err error) {
	ci := CallFrom(ctx)
	if ci.Hidden {
		return
	}
	if ci.Tool != "" {
		tool = ci.Tool
	}
	e := LogEntry{Kind: KindFS, Tool: tool, Approval: ci.Approval, Title: title, Path: path, Duration: time.Since(start)}
	if err != nil {
		e.Level, e.Error = LevelWarn, err.Error()
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrEscape) && !errors.Is(err, ErrTooLarge) {
			e.Level = LevelError
		}
	} else {
		e.Bytes = &n
	}
	logAdd(s.log, e)
}

func (s *loggedSession) ReadFile(ctx context.Context, path string) ([]byte, error) {
	start := time.Now()
	b, err := s.Session.ReadFile(ctx, path)
	s.fsEntry(ctx, "fs_read", "read "+path, path, start, int64(len(b)), err)
	return b, err
}

func (s *loggedSession) WriteFile(ctx context.Context, path string, data []byte) error {
	start := time.Now()
	err := s.Session.WriteFile(ctx, path, data)
	s.fsEntry(ctx, "fs_write", fmt.Sprintf("write %s (%d bytes)", path, len(data)), path, start, int64(len(data)), err)
	return err
}

func (s *loggedSession) List(ctx context.Context, path string) ([]Entry, error) {
	start := time.Now()
	ents, err := s.Session.List(ctx, path)
	if path == "" {
		path = "."
	}
	s.fsEntry(ctx, "fs_list", "list "+path, path, start, int64(len(ents)), err)
	return ents, err
}

func (s *loggedSession) Search(ctx context.Context, path, pattern string) ([]Match, error) {
	start := time.Now()
	hits, err := s.Session.Search(ctx, path, pattern)
	if path == "" {
		path = "."
	}
	s.fsEntry(ctx, "fs_search", fmt.Sprintf("search %s in %s", oneLine(pattern), path), path, start, int64(len(hits)), err)
	return hits, err
}

func (s *loggedSession) Close(ctx context.Context, success bool) error {
	start := time.Now()
	err := s.Session.Close(ctx, success)
	e := LogEntry{Kind: KindLifecycle, Duration: time.Since(start), Fields: map[string]string{"backend": s.backend, "lifecycle": s.lifecycle, "run_succeeded": fmt.Sprint(success)}}
	switch {
	case !success && s.backend == "depot" && s.lifecycle == "per_run":
		e.Title = "Teardown: run failed, sandbox stopped"
	case !success:
		e.Title = "Teardown: run failed, workspace kept for debugging until the retention sweep"
	case s.lifecycle == "per_run":
		e.Title = "Teardown: run succeeded, sandbox removed"
	default:
		e.Title = "Teardown: run succeeded, workdir removed from the shared sandbox"
	}
	up := SessionUpdate{Status: StatusClosed}
	if err != nil {
		e.Level, e.Error = LevelError, err.Error()
		e.Title = "Teardown failed: " + oneLine(err.Error())
		up.Error = "teardown: " + oneLine(err.Error())
	}
	logAdd(s.log, e)
	if s.log != nil {
		s.log.Update(up)
	}
	return err
}

// envNames lists the variable names, sorted.
func envNames(vars map[string]string) []string {
	out := mapKeys(vars)
	sort.Strings(out)
	return out
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
