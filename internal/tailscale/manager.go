package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/spec"
)

// StateFile is where hal9001 records what it added to config.yaml, under <home>.
const StateFile = "data/tailscale.json"

const defaultCacheTTL = 10 * time.Second

// startupRetry are the waits between attempts to re-apply the mapping at start when tailscaled is not reachable yet
// (it may still be starting at login).
var startupRetry = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}

// Options configures New.
type Options struct {
	// Home is the hal9001 home directory (config.yaml lives there).
	Home string
	// Port is the daemon's loopback port, the target of the mapping.
	Port int
	// CLI finds and runs the tailscale executable.
	CLI CLI
	Log *slog.Logger
	// CacheTTL is how long a detection is reused; default 10s.
	CacheTTL time.Duration
	// Now is the clock; default time.Now.
	Now func() time.Time
	// StartupRetry overrides the waits between start-up attempts (tests).
	StartupRetry []time.Duration
}

// Manager detects Tailscale and shares the daemon on the tailnet.
type Manager struct {
	opt Options
	log *slog.Logger

	opMu sync.Mutex // serializes Enable, Disable and Reconcile

	mu       sync.Mutex // guards the fields below
	snap     *snapshot
	snapAt   time.Time
	reapply  string // why the last start-up re-apply failed; "" when it did not
	versions map[string]string
}

// New returns a Manager.
func New(opt Options) *Manager {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.CacheTTL <= 0 {
		opt.CacheTTL = defaultCacheTTL
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.StartupRetry == nil {
		opt.StartupRetry = startupRetry
	}
	return &Manager{opt: opt, log: opt.Log, versions: map[string]string{}}
}

// Client is the CLI the manager uses, for the auth module.
func (m *Manager) Client() Client { return m.opt.CLI }

// snapshot is one reading of the CLI.
type snapshot struct {
	at       time.Time
	cliFound bool
	cliPath  string
	version  string
	st       Status
	stErr    error
	serve    ServeConfig
	serveErr error
}

// detect reads the CLI, or returns the cached reading when it is fresh and force is false.
func (m *Manager) detect(ctx context.Context, force bool) *snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !force && m.snap != nil && m.opt.Now().Sub(m.snapAt) < m.opt.CacheTTL {
		return m.snap
	}
	s := &snapshot{at: m.opt.Now().UTC()}
	cli := m.opt.CLI
	path, err := cli.Locate()
	if err != nil {
		m.snap, m.snapAt = s, m.opt.Now()
		return s
	}
	s.cliFound, s.cliPath = true, path
	s.version = m.versions[path]
	if s.version == "" {
		if v, err := cli.Version(ctx); err == nil {
			s.version = v
			m.versions[path] = v
		}
	}
	s.st, s.stErr = cli.Status(ctx)
	if s.stErr == nil {
		s.serve, s.serveErr = cli.ServeStatus(ctx)
	}
	m.snap, m.snapAt = s, m.opt.Now()
	return s
}

// Invalidate forgets the cached reading.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.snap = nil
	m.mu.Unlock()
}

// state is what hal9001 remembers between runs: the allowed_hosts entries it added, which are the only ones it
// removes again.
type state struct {
	AddedHosts []string `json:"added_hosts"`
}

func (m *Manager) statePath() string { return filepath.Join(m.opt.Home, filepath.FromSlash(StateFile)) }

func (m *Manager) readState() state {
	var st state
	b, err := os.ReadFile(m.statePath())
	if err != nil {
		return st
	}
	if err := json.Unmarshal(b, &st); err != nil {
		m.log.Warn("tailscale: ignoring unreadable state file", "path", m.statePath(), "err", err)
		return state{}
	}
	return st
}

func (m *Manager) writeState(st state) error {
	if len(st.AddedHosts) == 0 {
		if err := os.Remove(m.statePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath()), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.statePath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath())
}

// config reads the current config.yaml (defaults when it is missing or unreadable).
func (m *Manager) config() spec.Config {
	raw, _, err := spec.ReadFile(m.opt.Home, spec.KindConfig, "")
	if err != nil {
		raw = nil
	}
	cfg, _ := spec.ParseConfig(raw)
	if cfg == nil {
		cfg = &spec.Config{}
		cfg.ApplyDefaults()
	}
	return *cfg
}

// editConfig applies edit to config.yaml with the optimistic-write check, retrying when another writer got in
// between. Nothing is written when edit reports no change.
func (m *Manager) editConfig(edit func(root *yaml.Node) (bool, error)) error {
	var err error
	for range 3 {
		raw, hash, rerr := spec.ReadFile(m.opt.Home, spec.KindConfig, "")
		expected := hash
		if errors.Is(rerr, spec.ErrNotFound) {
			raw, expected = nil, ""
		} else if rerr != nil {
			return rerr
		}
		out, changed, eerr := spec.EditConfigYAML(raw, edit)
		if eerr != nil || !changed {
			return eerr
		}
		if _, err = spec.WriteFile(m.opt.Home, spec.KindConfig, "", out, expected); err == nil {
			return nil
		} else if !errors.Is(err, spec.ErrConflict) {
			return err
		}
	}
	return err
}

// Status builds the report. force checks the CLI again instead of using the cached reading.
func (m *Manager) Status(ctx context.Context, force bool) Report {
	return m.report(m.detect(ctx, force), m.config(), m.readState())
}

func (m *Manager) report(s *snapshot, cfg spec.Config, st state) Report {
	port := cfg.Tailscale.HTTPSPort
	r := Report{
		CLI:           CLIInfo{Found: s.cliFound, Path: ptr(s.cliPath), Version: ptr(s.version)},
		Serve:         ServeInfo{Enabled: cfg.Tailscale.Serve, HTTPSPort: port, Target: ServeTarget(m.opt.Port)},
		AllowedLogins: slices.Clone(cfg.AllowedLogins),
		Warnings:      []Warning{},
		CheckedAt:     s.at,
	}
	if r.AllowedLogins == nil {
		r.AllowedLogins = []string{}
	}
	switch {
	case !s.cliFound:
		r.Error = ptr("tailscale CLI not found")
	case s.stErr != nil:
		r.Error = ptr(s.stErr.Error())
	}
	r.Warnings = append(r.Warnings, s.readiness()...)

	if s.cliFound && s.stErr == nil {
		stt := s.st
		r.BackendState = ptr(stt.BackendState)
		r.LoggedIn = stt.BackendState == "Running"
		r.DNSName, r.Tailnet, r.OwnerLogin = ptr(stt.Hostname()), ptr(stt.TailnetName()), ptr(stt.OwnerLogin())
		r.HTTPSEnabled, r.MagicDNSEnabled = stt.HTTPSEnabled(), stt.MagicDNSEnabled()
		if len(r.AllowedLogins) == 0 && stt.OwnerLogin() != "" {
			r.AllowedLogins = []string{stt.OwnerLogin()}
		}
		if s.serveErr != nil {
			r.Error = ptr("serve status: " + s.serveErr.Error())
		} else {
			r.Serve.Funnel = s.serve.Funnel()
			mp := s.serve.MappingFor(port, m.opt.Port)
			r.Serve.Active, r.Serve.PointsAtHal, r.Serve.Conflict = mp.Active, mp.PointsAtHal, ptr(mp.Other)
		}
		if name := stt.Hostname(); name != "" {
			entry := HostEntry(name, port)
			r.URL = ptr(URLFor(name, port))
			r.AllowedHost = AllowedHostInfo{Entry: &entry, Present: hasHost(cfg.AllowedHosts, entry)}
			r.AllowedHost.AddedByHal = r.AllowedHost.Present && hasHost(st.AddedHosts, entry)
		}
	}
	r.Warnings = append(r.Warnings, m.serveWarnings(r, s, cfg, st)...)
	return r
}

// serveWarnings are the findings about the mapping and allowed_hosts of an otherwise working Tailscale.
func (m *Manager) serveWarnings(r Report, s *snapshot, cfg spec.Config, st state) []Warning {
	var ws []Warning
	if r.Serve.Funnel {
		ws = append(ws, warn(WarnFunnelEnabled, "Something on this Mac is exposed to the public internet through `tailscale funnel`. hal9001 never uses funnel; check what is exposed.", "tailscale funnel status", ""))
	}
	if s.cliFound && s.stErr == nil && s.serveErr == nil && cfg.Tailscale.Serve {
		switch {
		case r.Serve.Conflict != nil:
			ws = append(ws, warn(WarnPortConflict, fmt.Sprintf("Sharing is on, but HTTPS port %d serves %s instead of hal9001. hal9001 does not replace it by itself.", cfg.Tailscale.HTTPSPort, *r.Serve.Conflict), "", ""))
		case !r.Serve.Active && r.LoggedIn:
			ws = append(ws, warn(WarnServeMissing, "Sharing is on, but Tailscale has no mapping for hal9001. Turn sharing on again, or restart hal9001.", serveCommand(cfg.Tailscale.HTTPSPort, m.opt.Port), ""))
		}
	}
	if r.AllowedHost.Entry != nil && !r.AllowedHost.Present && (cfg.Tailscale.Serve || r.Serve.PointsAtHal) {
		ws = append(ws, warn(WarnHostMissing, fmt.Sprintf("%s is not in allowed_hosts in config.yaml, so requests over the tailnet are refused. Turn sharing on again to add it.", *r.AllowedHost.Entry), "", ""))
	}
	if r.DNSName != nil {
		var stale []string
		for _, h := range st.AddedHosts {
			if hasHost(cfg.AllowedHosts, h) && !strings.EqualFold(hostName(h), *r.DNSName) {
				stale = append(stale, h)
			}
		}
		if len(stale) > 0 {
			ws = append(ws, warn(WarnHostRenamed, fmt.Sprintf("This Mac's tailnet name is now %s. The old name (%s) is still in allowed_hosts; remove it from config.yaml if you no longer use it.", *r.DNSName, strings.Join(stale, ", ")), "", ""))
		}
	}
	m.mu.Lock()
	reapply := m.reapply
	m.mu.Unlock()
	if reapply != "" {
		ws = append(ws, warn(WarnReapplyFailed, "hal9001 could not restore the tailnet mapping at start: "+reapply, "", ""))
	}
	return ws
}

func hostName(entry string) string {
	if i := strings.LastIndex(entry, ":"); i >= 0 {
		return entry[:i]
	}
	return entry
}

// NotReadyError means Tailscale cannot share anything yet; Warnings say what to fix.
type NotReadyError struct{ Warnings []Warning }

func (e *NotReadyError) Error() string {
	msgs := make([]string, len(e.Warnings))
	for i, w := range e.Warnings {
		msgs[i] = w.Message
	}
	return "tailscale is not ready: " + strings.Join(msgs, " ")
}

// ConflictError means the HTTPS port already serves something other than the daemon.
type ConflictError struct {
	HTTPSPort int
	Target    string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("HTTPS port %d on the tailnet already serves %s", e.HTTPSPort, e.Target)
}

// EnableRequest is the input of Enable.
type EnableRequest struct {
	// HTTPSPort is the tailnet HTTPS port; 0 means the configured one (default 443).
	HTTPSPort int
	// Replace overwrites what the port serves when it is not the daemon.
	Replace bool
}

// Enable shares the daemon on the tailnet and brings config.yaml in line (see the package comment). It is safe to
// call again: nothing is run or written for what is already in place.
func (m *Manager) Enable(ctx context.Context, req EnableRequest) (Report, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	cfg := m.config()
	port := req.HTTPSPort
	if port == 0 {
		port = cfg.Tailscale.HTTPSPort
	}
	if port < 1 || port > 65535 {
		return Report{}, fmt.Errorf("https_port must be between 1 and 65535, got %d", port)
	}
	snap := m.detect(ctx, true)
	if ws := snap.readiness(); len(ws) > 0 {
		return Report{}, &NotReadyError{Warnings: ws}
	}
	if snap.serveErr != nil {
		return Report{}, snap.serveErr
	}
	mp := snap.serve.MappingFor(port, m.opt.Port)
	if mp.Active && !mp.PointsAtHal && !req.Replace {
		return Report{}, &ConflictError{HTTPSPort: port, Target: mp.Other}
	}
	// Moving to another port: take down the mapping hal9001 put on the old one.
	if old := cfg.Tailscale.HTTPSPort; cfg.Tailscale.Serve && old != port {
		if om := snap.serve.MappingFor(old, m.opt.Port); om.PointsAtHal {
			if err := m.opt.CLI.ServeOff(ctx, old); err != nil {
				return Report{}, err
			}
			m.log.Info("tailscale: removed the mapping on the old HTTPS port", "https_port", old)
		}
	}
	if !mp.PointsAtHal {
		if err := m.opt.CLI.Serve(ctx, port, m.opt.Port); err != nil {
			return Report{}, err
		}
		m.log.Info("tailscale: sharing hal9001 on the tailnet", "https_port", port, "args", strings.Join(ServeArgs(port, m.opt.Port), " "))
	}
	if err := m.syncConfig(snap.st.Hostname(), port, cfg.Tailscale.Serve, cfg.Tailscale.HTTPSPort); err != nil {
		return Report{}, err
	}
	m.setReapply("")
	return m.Status(ctx, true), nil
}

// syncConfig sets tailscale.serve and tailscale.https_port and makes sure allowed_hosts holds the entry for name on
// port, recording an entry it added. oldPort is the port of the previous mapping (when serve was on), whose entry is
// removed again if hal9001 added it.
func (m *Manager) syncConfig(name string, port int, wasServing bool, oldPort int) error {
	entry := HostEntry(name, port)
	st := m.readState()
	var added bool
	var removed []string
	err := m.editConfig(func(root *yaml.Node) (bool, error) {
		added, removed = false, nil
		c1, err := spec.SetTailscaleServe(root, true, port)
		if err != nil {
			return false, err
		}
		c2, err := spec.AddAllowedHost(root, entry)
		if err != nil {
			return false, err
		}
		added = c2
		changed := c1 || c2
		if wasServing && oldPort != port {
			if old := HostEntry(name, oldPort); hasHost(st.AddedHosts, old) && spec.RemoveAllowedHost(root, old) {
				removed = append(removed, old)
				changed = true
			}
		}
		return changed, nil
	})
	if err != nil {
		return fmt.Errorf("update config.yaml: %w", err)
	}
	next := state{AddedHosts: slices.DeleteFunc(slices.Clone(st.AddedHosts), func(h string) bool { return slices.Contains(removed, h) })}
	if added && !hasHost(next.AddedHosts, entry) {
		next.AddedHosts = append(next.AddedHosts, entry)
	}
	if !slices.Equal(next.AddedHosts, st.AddedHosts) {
		if err := m.writeState(next); err != nil {
			m.log.Warn("tailscale: could not record the allowed_hosts entry it added", "err", err)
		}
	}
	return nil
}

// Disable stops sharing: it removes the mapping when it points at the daemon, sets tailscale.serve to false and
// removes the allowed_hosts entries hal9001 added. A mapping to anything else and entries you wrote stay.
func (m *Manager) Disable(ctx context.Context) (Report, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	cfg := m.config()
	port := cfg.Tailscale.HTTPSPort
	snap := m.detect(ctx, true)
	switch {
	case !snap.cliFound: // nothing to take down
	case snap.stErr != nil:
		return Report{}, snap.stErr
	case snap.serveErr != nil:
		return Report{}, snap.serveErr
	default:
		if snap.serve.MappingFor(port, m.opt.Port).PointsAtHal {
			if err := m.opt.CLI.ServeOff(ctx, port); err != nil {
				return Report{}, err
			}
			m.log.Info("tailscale: stopped sharing hal9001 on the tailnet", "https_port", port)
		}
	}
	st := m.readState()
	err := m.editConfig(func(root *yaml.Node) (bool, error) {
		changed, err := spec.SetTailscaleServe(root, false, port)
		for _, h := range st.AddedHosts {
			if spec.RemoveAllowedHost(root, h) {
				changed = true
			}
		}
		return changed, err
	})
	if err != nil {
		return Report{}, fmt.Errorf("update config.yaml: %w", err)
	}
	if err := m.writeState(state{}); err != nil {
		m.log.Warn("tailscale: could not clear the state file", "err", err)
	}
	m.setReapply("")
	return m.Status(ctx, true), nil
}

func (m *Manager) setReapply(msg string) {
	m.mu.Lock()
	m.reapply = msg
	m.mu.Unlock()
}

// Reconcile is run at start: when tailscale.serve is on it re-applies the mapping if Tailscale lost it and keeps
// allowed_hosts in line with this Mac's current name (a new name is added; the old one stays and shows as a
// warning). A failure is logged and kept as the reapply_failed warning. It retries while tailscaled is not
// reachable yet and returns when done or ctx ends.
func (m *Manager) Reconcile(ctx context.Context) {
	for attempt := 0; ; attempt++ {
		err := m.reconcileOnce(ctx)
		if err == nil {
			return
		}
		retry := errors.Is(err, ErrUnavailable) && !errors.Is(err, exec.ErrNotFound) && attempt < len(m.opt.StartupRetry)
		if !retry {
			m.log.Warn("tailscale: could not re-apply sharing at start", "err", err)
			m.setReapply(err.Error())
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.opt.StartupRetry[attempt]):
		}
	}
}

func (m *Manager) reconcileOnce(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	cfg := m.config()
	if !cfg.Tailscale.Serve {
		m.setReapply("")
		return nil
	}
	port := cfg.Tailscale.HTTPSPort
	snap := m.detect(ctx, true)
	switch {
	case !snap.cliFound:
		return fmt.Errorf("%w: tailscale CLI not found: %w", ErrUnavailable, exec.ErrNotFound)
	case snap.stErr != nil:
		return snap.stErr
	}
	if ws := snap.readiness(); len(ws) > 0 {
		return &NotReadyError{Warnings: ws}
	}
	if snap.serveErr != nil {
		return snap.serveErr
	}
	mp := snap.serve.MappingFor(port, m.opt.Port)
	if mp.Active && !mp.PointsAtHal {
		return &ConflictError{HTTPSPort: port, Target: mp.Other}
	}
	if !mp.PointsAtHal {
		if err := m.opt.CLI.Serve(ctx, port, m.opt.Port); err != nil {
			return err
		}
		m.log.Info("tailscale: re-applied sharing at start", "https_port", port)
	}
	if err := m.syncConfig(snap.st.Hostname(), port, true, port); err != nil {
		return err
	}
	m.Invalidate()
	m.setReapply("")
	return nil
}
