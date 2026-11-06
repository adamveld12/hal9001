package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
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
// rs.Lifecycle defaults to the backend's configured lifecycle.
func (m *Manager) Open(ctx context.Context, backend string, rs RunSpec) (Session, error) {
	b, cfg, err := m.get(backend)
	if err != nil {
		return nil, err
	}
	if rs.Lifecycle == "" {
		rs.Lifecycle = cfg.Lifecycle
	}
	return b.Prepare(ctx, rs)
}

// Policy returns the approval policy of backend.
func (m *Manager) Policy(backend string) (*Policy, error) {
	cfg, err := m.Config(backend)
	if err != nil {
		return nil, err
	}
	return NewPolicy(cfg)
}

func (m *Manager) get(backend string) (Backend, spec.Sandbox, error) {
	if !spec.ValidSandboxBackend(backend) {
		return nil, spec.Sandbox{}, fmt.Errorf("unknown sandbox backend %q", backend)
	}
	cfg, env, print, err := m.load(backend)
	if err != nil {
		return nil, cfg, err
	}
	if !cfg.Enabled {
		return nil, cfg, fmt.Errorf("%w: %s", ErrDisabled, backend)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur := m.backends[backend]; cur != nil {
		if cur.print == print {
			return cur.backend, cfg, nil
		}
		closeBackend(cur.backend)
		delete(m.backends, backend)
	}
	b, err := New(backend, cfg, env, m.opts.Home)
	if err != nil {
		return nil, cfg, err
	}
	m.backends[backend] = &managed{backend: b, cfg: cfg, print: print}
	return b, cfg, nil
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
		b, _, err := m.get(name)
		if err != nil {
			continue
		}
		if s, ok := b.(Sweeper); ok {
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
