package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"hal9001/internal/spec"
)

// ErrDisabled is returned by Manager.Open for a backend that is not enabled.
var ErrDisabled = errors.New("sandbox backend is not enabled")

// SweepInterval is how often a Manager removes expired failed per_run workspaces.
const SweepInterval = time.Hour

// ManagerOptions configures NewManager.
type ManagerOptions struct {
	// Home is the hal9001 home dir holding sandboxes/ and env.d/sandboxes/.
	Home string
	// RetentionDays returns how long failed per_run workspaces are kept (config.yaml retention_days).
	// Nil means 30.
	RetentionDays func() int
	Log           *slog.Logger
}

// Manager keeps one Backend per sandbox backend, built from the files under Home. Reload drops the
// backends whose config or env changed; they are rebuilt on the next Open. Safe for concurrent use.
type Manager struct {
	opts ManagerOptions

	mu       sync.Mutex
	backends map[string]*managed

	stop chan struct{}
	done chan struct{}
}

type managed struct {
	backend Backend
	cfg     spec.Sandbox
	print   string // fingerprint of the files the backend was built from
	// envNames are the variables commands see; secrets are the values to keep out of the run log.
	envNames []string
	secrets  []string
}

// NewManager returns a Manager. Nothing is built until Open.
func NewManager(opts ManagerOptions) *Manager {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Manager{opts: opts, backends: map[string]*managed{}}
}

// load reads and validates a backend's files.
func (m *Manager) load(backend string) (spec.Sandbox, Env, string, error) {
	raw, _, err := spec.ReadFile(m.opts.Home, spec.KindSandbox, backend)
	if err != nil && !errors.Is(err, spec.ErrNotFound) {
		return spec.Sandbox{}, Env{}, "", err
	}
	cfg, ds := spec.CheckSandboxIn(m.opts.Home, backend, raw)
	if cfg == nil || spec.HasErrors(ds) {
		msg := "invalid config"
		if len(ds) > 0 {
			msg = ds[0].Message
		}
		return spec.Sandbox{}, Env{}, "", fmt.Errorf("sandbox %s: %s", backend, msg)
	}
	env, err := LoadEnv(m.opts.Home, backend)
	if err != nil {
		return spec.Sandbox{}, Env{}, "", err
	}
	keys := make([]string, 0, len(env.Vars))
	for k := range env.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := string(raw) + "\x00"
	for _, k := range keys {
		h += k + "=" + env.Vars[k] + "\x00"
	}
	return *cfg, env, spec.Hash([]byte(h)), nil
}

// Config returns the stored config of a backend (the defaults when it has no file).
func (m *Manager) Config(backend string) (spec.Sandbox, error) {
	cfg, _, _, err := m.load(backend)
	return cfg, err
}

// Open starts a session on backend for rs. It fails with ErrDisabled for a backend that is not enabled.
// rs.Lifecycle defaults to the backend's configured lifecycle. With rs.Log set, the provisioning, every operation of
// the session and its teardown are recorded there.
func (m *Manager) Open(ctx context.Context, backend string, rs RunSpec) (Session, error) {
	start := time.Now()
	log := rs.Log
	mg, err := m.get(backend)
	if mg != nil {
		log = withRedaction(log, mg.secrets)
		rs.Log = log
	}
	if rs.Lifecycle == "" && mg != nil {
		rs.Lifecycle = mg.cfg.Lifecycle
	}
	if log != nil {
		fields := map[string]string{"backend": backend, "lifecycle": rs.Lifecycle}
		if rs.Image != "" {
			fields["image"] = rs.Image
		}
		if rs.Repo != "" {
			fields["repo"] = CleanRepo(rs.Repo)
		}
		if rs.Ref != "" {
			fields["ref"] = rs.Ref
		}
		var names []string
		if mg != nil {
			names = append(append([]string(nil), mg.envNames...), mapKeys(rs.Env)...)
			sort.Strings(names)
			names = slices.Compact(names)
		}
		log.Update(SessionUpdate{Status: StatusProvisioning, Info: SessionInfo{
			Image: rs.Image, Repo: CleanRepo(rs.Repo), Ref: rs.Ref, Lifecycle: rs.Lifecycle, EnvNames: names,
		}})
		logStep(log, "Provisioning "+backend+" sandbox", fields)
		if mg != nil {
			env := LogEntry{Kind: KindLifecycle, Title: "Environment variables visible to commands (names only)", Fields: map[string]string{"count": fmt.Sprint(len(names))}}
			if len(names) > 0 {
				env.Fields["names"] = strings.Join(names, ", ")
			}
			log.Add(env)
		}
	}
	fail := func(err error) (Session, error) {
		if log != nil {
			msg := err.Error()
			log.Add(LogEntry{Kind: KindLifecycle, Level: LevelError, Title: "Provisioning failed: " + oneLine(msg), Error: msg, Duration: time.Since(start)})
			log.Update(SessionUpdate{Status: StatusFailed, Error: msg})
		}
		return nil, err
	}
	if err != nil {
		return fail(err)
	}
	sess, err := mg.backend.Prepare(ctx, rs)
	if err != nil {
		return fail(err)
	}
	if log == nil {
		return sess, nil
	}
	info := SessionInfo{Workdir: sess.Workdir()}
	if d, ok := sess.(Describer); ok {
		info = d.Describe()
	}
	info.Lifecycle = rs.Lifecycle
	took := time.Since(start)
	fields := map[string]string{"workdir": info.Workdir, "duration_ms": fmt.Sprint(ms(took))}
	if info.Name != "" {
		fields["sandbox"] = info.Name
	}
	if info.ID != "" {
		fields["sandbox_id"] = info.ID
	}
	log.Add(LogEntry{Kind: KindLifecycle, Title: "Sandbox ready in " + took.Round(time.Millisecond).String(), Duration: took, Fields: fields})
	log.Update(SessionUpdate{Status: StatusReady, Info: info})
	return &loggedSession{Session: sess, log: log, backend: backend, lifecycle: rs.Lifecycle}, nil
}

// Policy returns the approval policy of backend.
func (m *Manager) Policy(backend string) (*Policy, error) {
	cfg, err := m.Config(backend)
	if err != nil {
		return nil, err
	}
	return NewPolicy(cfg)
}

// get returns the built backend of a backend name. On ErrDisabled and other errors the result is nil, except that
// an enabled backend whose construction failed has no entry either.
func (m *Manager) get(backend string) (*managed, error) {
	if !spec.ValidSandboxBackend(backend) {
		return nil, fmt.Errorf("unknown sandbox backend %q", backend)
	}
	cfg, env, print, err := m.load(backend)
	if err != nil {
		return nil, err
	}
	names := envNames(env.commandVars(backend))
	if !cfg.Enabled {
		return &managed{cfg: cfg, envNames: names, secrets: env.Secrets}, fmt.Errorf("%w: %s", ErrDisabled, backend)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.backends[backend]; cur != nil {
		if cur.print == print {
			return cur, nil
		}
		closeBackend(cur.backend)
		delete(m.backends, backend)
	}
	b, err := New(backend, cfg, env, m.opts.Home)
	if err != nil {
		return nil, err
	}
	cur := &managed{backend: b, cfg: cfg, print: print, envNames: names, secrets: env.Secrets}
	m.backends[backend] = cur
	return cur, nil
}

func closeBackend(b Backend) {
	if c, ok := b.(io.Closer); ok {
		_ = c.Close()
	}
}

// Reload drops every backend whose files changed or that was disabled, closing its connections. Sessions
// already open keep working until they end.
func (m *Manager) Reload() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cur := range m.backends {
		cfg, _, print, err := m.load(name)
		if err == nil && cfg.Enabled && print == cur.print {
			continue
		}
		closeBackend(cur.backend)
		delete(m.backends, name)
	}
}

// Sweep removes failed per_run workspaces older than the retention period on every enabled backend.
func (m *Manager) Sweep(ctx context.Context) {
	days := 30
	if m.opts.RetentionDays != nil {
		days = m.opts.RetentionDays()
	}
	maxAge := time.Duration(days) * 24 * time.Hour
	for _, name := range spec.SandboxBackends {
		cfg, _, _, err := m.load(name)
		if err != nil || !cfg.Enabled || cfg.Lifecycle != spec.LifecyclePerRun {
			continue
		}
		mg, err := m.get(name)
		if err != nil {
			continue
		}
		if s, ok := mg.backend.(Sweeper); ok {
			if err := s.Sweep(ctx, maxAge); err != nil {
				m.opts.Log.Warn("sandbox sweep failed", "backend", name, "err", err)
			}
		}
	}
}

// Start runs Sweep now and every SweepInterval until Close or ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.stop, m.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(m.done)
		t := time.NewTicker(SweepInterval)
		defer t.Stop()
		for {
			m.Sweep(ctx)
			select {
			case <-ctx.Done():
				return
			case <-m.stop:
				return
			case <-t.C:
			}
		}
	}()
}

// Close stops the sweeper and closes every backend.
func (m *Manager) Close() {
	if m.stop != nil {
		close(m.stop)
		<-m.done
		m.stop = nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cur := range m.backends {
		closeBackend(cur.backend)
		delete(m.backends, name)
	}
}
