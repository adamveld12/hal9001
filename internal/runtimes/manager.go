package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hal9001/internal/runner"
	"hal9001/internal/spec"
)

// Options configures New.
type Options struct {
	// Data is <home>/data; managed adapters live under Data/adapters.
	Data string
	// Path is the PATH hal9001 starts agents with (the login-shell PATH under launchd, else the process PATH); empty
	// means this process's. Directories where user tools normally live are added to it for the lookups.
	Path string
	// ExactPath uses Path as it is, without adding the directories where user tools normally live.
	ExactPath bool
	// UserHome is the user's home directory, for those directories; empty means os.UserHomeDir.
	UserHome string
	// EnvFile returns the values of hal9001's env file; they are part of the environment detection commands run in.
	EnvFile func() map[string]string
	Log     *slog.Logger
	// CacheTTL is how long a Detection is reused; default 5 minutes. A detect, an install and a configuration change
	// always recompute it.
	CacheTTL time.Duration
}

// Manager detects, resolves and installs runtimes. It is safe for concurrent use.
type Manager struct {
	opt  Options
	path string
	log  *slog.Logger

	toolsMu sync.Mutex
	tools   map[string]*toolEntry

	detMu sync.Mutex
	det   map[string]*detEntry

	mu         sync.Mutex
	installing map[string]bool   // adapter bin -> running
	lastErr    map[string]string // adapter bin -> why the last install failed
	subs       map[int]func(Event)
	nextSub    int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type detEntry struct {
	mu  sync.Mutex
	fp  string
	res Detection
	at  time.Time
}

// New returns a Manager.
func New(opt Options) *Manager {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.CacheTTL <= 0 {
		opt.CacheTTL = 5 * time.Minute
	}
	if opt.UserHome == "" {
		opt.UserHome, _ = os.UserHomeDir()
	}
	base := opt.Path
	if base == "" {
		base = os.Getenv("PATH")
	}
	path := runner.EffectivePATH(base, opt.UserHome)
	if opt.ExactPath {
		path = base
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		opt: opt, path: path, log: opt.Log,
		tools: map[string]*toolEntry{}, det: map[string]*detEntry{},
		installing: map[string]bool{}, lastErr: map[string]string{}, subs: map[int]func(Event){},
		ctx: ctx, cancel: cancel,
	}
}

// Close stops running installs and waits for them.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

// Refresh forgets every cached lookup and detection; the next call looks again.
func (m *Manager) Refresh() {
	m.toolsMu.Lock()
	m.tools = map[string]*toolEntry{}
	m.toolsMu.Unlock()
	m.detMu.Lock()
	m.det = map[string]*detEntry{}
	m.detMu.Unlock()
}

// Resolution is how a runtime without a usable user command starts.
type Resolution struct {
	// Command is the argv to start (with {{data}} replaced); nil when Err is set.
	Command []string
	// Source is SourceCustom, SourceManaged or SourceBuiltin.
	Source string
	// AdapterVersion is the installed adapter's version (managed) or the CLI's (builtin).
	AdapterVersion string
	// Err says why the runtime cannot start and how to fix it.
	Err error
}

// Adapter sources.
const (
	SourceBuiltin = "builtin"
	SourceManaged = "managed"
	SourceCustom  = "custom"
	SourceNone    = "none"
)

// plan is what resolving a runtime found, before any login check.
type plan struct {
	kind     string
	info     kindInfo
	known    bool
	source   string
	command  []string
	cli      tool
	node     tool
	adapter  adapterState
	warnings []Warning
}

// adapterState is the state of the adapter a runtime runs (managed install, the omp CLI, or a custom command).
type adapterState struct {
	installed bool
	version   string
	path      string
}

func (m *Manager) sub(s string) string {
	return strings.ReplaceAll(s, spec.DataPlaceholder, m.opt.Data)
}

// plan resolves rt. name is only used in messages.
func (m *Manager) plan(ctx context.Context, name string, rt spec.Runtime) plan {
	p := plan{kind: kindOf(rt)}
	p.info, p.known = kinds[p.kind]
	if p.known {
		p.cli = m.lookup(ctx, p.info.cli)
	}
	switch {
	case len(rt.Command) > 0:
		p.source = SourceCustom
		for _, a := range rt.Command {
			p.command = append(p.command, m.sub(a))
		}
		if path, ok := m.findCommand(p.command[0]); ok {
			p.adapter = adapterState{installed: true, path: path}
		} else {
			p.warnings = append(p.warnings, Warning{
				Code:    CodeAdapterMissing,
				Message: fmt.Sprintf("The command %q of runtime %q was not found. Fix `command` in config.yaml.", p.command[0], name),
			})
			p.command = nil
		}
	case p.known && p.info.adapter != nil:
		p.source = SourceNone
		p.node = m.lookup(ctx, "node")
		m.nodeWarnings(&p)
		ver, script, ok := m.readAdapter(p.info.adapter)
		if ok {
			p.adapter = adapterState{installed: true, version: ver, path: script}
		} else {
			p.warnings = append(p.warnings, Warning{
				Code: CodeAdapterMissing,
				Message: fmt.Sprintf("The ACP adapter %s %s is not installed. Install it with the Install adapter button in Settings > Runtimes or `hal9001 runtimes install %s`.",
					p.info.adapter.bin, p.info.adapter.pin, name),
				Command: new("hal9001 runtimes install " + name),
			})
		}
		if ok && p.node.found {
			p.source = SourceManaged
			p.command = []string{p.node.path, script}
		}
		if msg := m.installError(p.info.adapter); msg != "" {
			p.warnings = append(p.warnings, Warning{
				Code: CodeAdapterInstallFailed, Message: "Installing the adapter failed: " + msg,
				Command: new("hal9001 runtimes install " + name),
			})
		}
	case p.known:
		p.source = SourceBuiltin
		if p.cli.found {
			p.command = []string{p.cli.path, "acp"}
			p.adapter = adapterState{installed: true, version: p.cli.version, path: p.cli.path}
		}
	}
	if p.known && !p.cli.found {
		p.warnings = append(p.warnings, Warning{
			Code:    CodeCLIMissing,
			Message: fmt.Sprintf("The %s CLI (%s) was not found on PATH. Install it, then sign in with `%s`.", p.info.title, p.info.cli, p.info.loginCommand),
			URL:     new(p.info.installURL),
		})
	}
	return p
}

func (m *Manager) nodeWarnings(p *plan) {
	switch {
	case !p.node.found:
		p.warnings = append(p.warnings, Warning{
			Code:    CodeNodeMissing,
			Message: fmt.Sprintf("Node.js %d or newer was not found on PATH; the %s ACP adapter needs it.", MinNodeMajor, p.info.title),
			URL:     new("https://nodejs.org/en/download"),
		})
	case nodeMajor(p.node.version) < MinNodeMajor:
		p.warnings = append(p.warnings, Warning{
			Code:    CodeNodeOld,
			Message: fmt.Sprintf("Node.js %s was found, but the %s ACP adapter needs %d or newer.", p.node.version, p.info.title, MinNodeMajor),
			URL:     new("https://nodejs.org/en/download"),
		})
	}
}

// findCommand resolves argv0 of a user command the way the agent process will find it.
func (m *Manager) findCommand(argv0 string) (string, bool) {
	if strings.Contains(argv0, "/") {
		st, err := os.Stat(argv0)
		return argv0, err == nil && !st.IsDir()
	}
	c := candidates(argv0, m.path)
	if len(c) == 0 {
		return "", false
	}
	return c[0], true
}

// Resolve returns how the runtime starts. A runtime with a command of its own is returned as written. Otherwise the
// installed adapter or the CLI's own ACP mode is used; if neither is usable Err names the problem and the fix.
// Resolve runs no login check; Detect does.
func (m *Manager) Resolve(ctx context.Context, name string, rt spec.Runtime) Resolution {
	p := m.plan(ctx, name, rt)
	res := Resolution{Source: p.source, AdapterVersion: p.adapter.version}
	if len(p.command) > 0 {
		res.Command = p.command
		return res
	}
	res.Err = unavailableError(name, p.warnings)
	return res
}

func unavailableError(name string, ws []Warning) error {
	if len(ws) == 0 {
		return fmt.Errorf("runtime %q is not available", name)
	}
	return fmt.Errorf("runtime %q is not available: %s", name, ws[0].Message)
}

// readAdapter reports the version and entry script of the installed adapter.
func (m *Manager) readAdapter(ai *adapterInfo) (version, script string, ok bool) {
	pkgDir := filepath.Join(m.opt.Data, "adapters", ai.bin, "node_modules", filepath.FromSlash(ai.pkg))
	b, err := os.ReadFile(filepath.Join(pkgDir, "package.json")) //nolint:gosec // path under hal9001's data directory
	if err != nil {
		return "", "", false
	}
	var pkg struct {
		Version string          `json:"version"`
		Bin     json.RawMessage `json:"bin"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return "", "", false
	}
	var rel string
	if json.Unmarshal(pkg.Bin, &rel) != nil {
		var bins map[string]string
		if json.Unmarshal(pkg.Bin, &bins) != nil {
			return "", "", false
		}
		rel = bins[ai.bin]
	}
	if rel == "" {
		return "", "", false
	}
	script = filepath.Join(pkgDir, filepath.FromSlash(rel))
	if st, err := os.Stat(script); err != nil || st.IsDir() {
		return "", "", false
	}
	return pkg.Version, script, true
}

func (m *Manager) installError(ai *adapterInfo) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[ai.bin]
}

func (m *Manager) isInstalling(ai *adapterInfo) bool {
	if ai == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installing[ai.bin]
}

// Detect checks one runtime: its CLI, login, adapter, Node.js and the command it would start. A result is reused
// until CacheTTL passes, the runtime's configuration changes, or Refresh is called; force skips the reuse (it does
// not forget the lookups of node and the CLIs: Refresh does).
func (m *Manager) Detect(ctx context.Context, name string, rt spec.Runtime, force bool) Detection {
	fpb, _ := json.Marshal([]any{name, rt.Kind, rt.Command})
	fp := string(fpb)
	m.detMu.Lock()
	e := m.det[name]
	if e == nil {
		e = &detEntry{}
		m.det[name] = e
	}
	m.detMu.Unlock()

	e.mu.Lock()
	if force || e.at.IsZero() || e.fp != fp || time.Since(e.at) >= m.opt.CacheTTL {
		e.res, e.fp, e.at = m.detect(ctx, name, rt), fp, time.Now()
	}
	res := e.res
	e.mu.Unlock()
	res.Adapter.Installing = m.isInstalling(kinds[kindOf(rt)].adapter)
	return res
}

func (m *Manager) detect(ctx context.Context, name string, rt spec.Runtime) Detection {
	p := m.plan(ctx, name, rt)
	d := Detection{
		CLI:     CLI{Name: p.info.cli},
		Auth:    Auth{Status: AuthUnknown},
		Adapter: Adapter{Source: p.source, Installed: p.adapter.installed, Installing: m.isInstalling(p.info.adapter)},
		Node:    Node{Found: p.node.found},
		Command: p.command, Warnings: p.warnings, CheckedAt: time.Now().UTC(),
	}
	if !p.known {
		// An agent hal9001 has no catalog entry for: all it can tell is whether the command resolves.
		if len(rt.Command) > 0 {
			d.CLI.Name = filepath.Base(rt.Command[0])
			d.CLI.Found = p.adapter.installed
			d.CLI.Path = nonEmpty(p.adapter.path)
		}
		d.Status = StatusReady
		if len(d.Command) == 0 {
			d.Status = StatusUnavailable
		}
		return finish(d)
	}
	d.CLI.Found = p.cli.found
	d.CLI.Path, d.CLI.Version = nonEmpty(p.cli.path), nonEmpty(p.cli.version)
	d.Auth.Command = new(p.info.loginCommand)
	d.Node.Version = nonEmpty(p.node.version)
	d.Node.OK = p.node.found && nodeMajor(p.node.version) >= MinNodeMajor
	d.Adapter.Version, d.Adapter.Path = nonEmpty(p.adapter.version), nonEmpty(p.adapter.path)
	if p.info.adapter != nil {
		d.Adapter.PinnedVersion = new(p.info.adapter.pin)
		d.Adapter.Installable = len(rt.Command) == 0
	}
	if p.cli.found {
		d.Auth.Status = m.checkAuth(ctx, p.info, p.cli.path)
		d.Warnings = append(d.Warnings, authWarnings(p.info, d.Auth.Status)...)
	}
	d.Status = StatusReady
	switch {
	case len(d.Command) == 0:
		d.Status = StatusUnavailable
	case len(d.Warnings) > 0 || d.Auth.Status != AuthOK:
		d.Status = StatusWarning
	}
	return finish(d)
}

func finish(d Detection) Detection {
	if d.Warnings == nil {
		d.Warnings = []Warning{}
	}
	return d
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Errors of Install and Uninstall.
var (
	// ErrNotInstallable: the runtime has no adapter hal9001 can install (omp, an agent with a user command).
	ErrNotInstallable = errors.New("runtime has no adapter hal9001 can install")
	// ErrBusy: an install of the adapter is running.
	ErrBusy = errors.New("an adapter install is already running")
)

// PrerequisiteError says an install cannot start because Node.js or npm is missing or too old.
type PrerequisiteError struct {
	Code    string
	Message string
}

func (e *PrerequisiteError) Error() string { return e.Message }
