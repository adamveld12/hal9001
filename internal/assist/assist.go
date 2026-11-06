// Package assist runs AI-assisted sensor authoring: a chat session with a coding agent (an ACP runtime from
// config.yaml) that edits a draft sensor file.
//
// The agent never writes a real sensor file. It works in a private draft directory,
// <home>/data/assist/<session>/, in a file named sensor.yaml; only the user's Save, through the normal sensor
// endpoints, persists a sensor. The agent never receives secrets: its environment holds no env-file values, and
// it runs the draft through the daemon (TestDraft), which overlays the global env file and the target sensor's
// own env values, redacts the output, and records nothing.
//
// Every turn of the conversation is an agent run (agent_runs, agent_events, permission_requests, approvals),
// linked to the session, so it shows on the Agent run page like any other. The agent process stays alive
// between turns and is suspended after an idle timeout; the next message resumes it with session/load.
package assist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

const (
	// DefaultIdleTimeout is how long an agent process may sit idle between turns before it is suspended.
	DefaultIdleTimeout = 15 * time.Minute
	// maxDraftBytes bounds a draft; sensors are small scripts.
	maxDraftBytes = 256 << 10
	// cancelWait is how long Cancel waits for the turn to wind down.
	cancelWait = 15 * time.Second
)

var ulidRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// Host is what the manager needs from the engine: runtime resolution (the same as agent nodes, isolated agent
// homes included), the env values and redactor, the approval queue, the kill switch and the engine's lifetime.
type Host interface {
	// Context is the engine's lifetime. Turns run on it, never on a request's context.
	Context() context.Context
	// Spawn runs fn on the engine's lifetime, tracked by Stop. It reports false, without running fn, once Stop has begun.
	Spawn(fn func(ctx context.Context)) bool
	// Track registers a cancelable unit so the kill switch and Stop can cancel it; the returned function unregisters it.
	Track(id string, cancel context.CancelCauseFunc) (untrack func())
	// Paused reports whether the kill switch is engaged.
	Paused() bool
	// Runtime returns a configured runtime with its placeholders resolved.
	Runtime(name string) (agent.RuntimeConfig, bool)
	// Env returns copies of the global env file values and of the sensor's own values (sensorID may be empty),
	// and a redactor that masks every secret of the workspace.
	Env(sensorID string) (global, sensor map[string]string, red *runner.Redactor)
	// BasePATH is the PATH nodes and agents start with.
	BasePATH() string
	// Recorder returns the recorder that persists the agent runs of an assist session.
	Recorder(assistSessionID, model string) agent.Recorder
	// Broker is the approval queue.
	Broker() agent.ApprovalBroker
	Clock() agent.Clock
	// TestFlow runs a validated flow draft once on one input as a dry-run test run and returns what every node did.
	TestFlow(ctx context.Context, req core.FlowTestRequest) (core.AssistFlowTestResult, error)
}

// Options configures New.
type Options struct {
	Store *store.Store
	// Home is the hal9001 home directory; drafts live under Home/data/assist.
	Home string
	Host Host
	Log  *slog.Logger
	// URL is the base URL the agent's CLI reaches this daemon at, e.g. http://127.0.0.1:9001.
	URL string
	// Executable is the hal9001 binary; its directory is put first on the agent's PATH so `hal9001 assist test`
	// works from the agent. Default: this process's executable.
	Executable string
	// IdleTimeout suspends an idle agent process (default 15m).
	IdleTimeout time.Duration
}

// Manager implements core.Assist.
type Manager struct {
	opt  Options
	st   *store.Store
	host Host
	log  *slog.Logger
	ctx  context.Context // uncancelable twin of the host's, for bookkeeping that must finish during shutdown
	home string
	dir  string // <home>/data/assist

	mu       sync.Mutex
	sessions map[string]*session
	byRun    map[string]*session // running turn's agent run id -> session
	stopped  bool
}

var _ core.Assist = (*Manager)(nil)

// New returns a manager. Call Start before serving requests.
func New(opt Options) (*Manager, error) {
	if opt.Store == nil || opt.Host == nil {
		return nil, errors.New("assist: Store and Host are required")
	}
	if opt.Home == "" {
		return nil, errors.New("assist: Home is required")
	}
	home, err := filepath.Abs(opt.Home)
	if err != nil {
		return nil, fmt.Errorf("assist: resolve home: %w", err)
	}
	opt.Home = home
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = DefaultIdleTimeout
	}
	if opt.Executable == "" {
		if opt.Executable, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("assist: locate the hal9001 binary: %w", err)
		}
	}
	return &Manager{
		opt: opt, st: opt.Store, host: opt.Host, log: opt.Log, ctx: context.WithoutCancel(opt.Host.Context()),
		home:     home,
		dir:      filepath.Join(home, "data", "assist"),
		sessions: map[string]*session{}, byRun: map[string]*session{},
	}, nil
}

// Start removes draft directories that no live session owns (left by a crash or a pruned session).
func (m *Manager) Start() error {
	entries, err := os.ReadDir(m.dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("assist: list draft directories: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || !ulidRE.MatchString(e.Name()) {
			continue
		}
		row, err := m.st.GetAssistSession(m.ctx, e.Name())
		if err == nil && row.Status != store.AssistClosed {
			continue
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := os.RemoveAll(filepath.Join(m.dir, e.Name())); err != nil {
			m.log.Warn("assist: remove orphan draft directory", "session", e.Name(), "err", err)
		}
	}
	return nil
}

// Shutdown stops every agent process. Call it after the engine has stopped (which cancels running turns).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.stopped = true
	all := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.shutdown()
		}()
	}
	wg.Wait()
}

// session returns the live state of a session, loading it from the store on first use.
func (m *Manager) session(ctx context.Context, id string) (*session, error) {
	if !ulidRE.MatchString(id) {
		return nil, fmt.Errorf("%w: assist session %q", store.ErrNotFound, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[id]; s != nil {
		return s, nil
	}
	row, err := m.st.GetAssistSession(ctx, id)
	if err != nil {
		return nil, err
	}
	s := newSession(m, *row)
	m.sessions[id] = s
	return s, nil
}

// CreateSession starts a session: its draft directory and draft, no agent process yet.
func (m *Manager) CreateSession(ctx context.Context, req core.AssistCreateRequest) (*store.AssistSession, error) {
	if req.TargetKind != store.AssistTargetSensor && req.TargetKind != store.AssistTargetFlow {
		return nil, invalid("target.kind", "target.kind must be sensor or flow")
	}
	if _, ok := m.host.Runtime(req.Runtime); !ok {
		return nil, &core.ValidationError{Diagnostics: []spec.Diagnostic{{
			Severity: spec.SeverityError, Code: spec.CodeUnknownRuntime, Path: "runtime", Message: fmt.Sprintf("runtime %q is not configured in config.yaml", req.Runtime),
		}}}
	}
	var yaml string
	switch {
	case req.YAML != nil:
		yaml = *req.YAML
	case req.TargetID != "":
		kind := spec.KindSensor
		if req.TargetKind == store.AssistTargetFlow {
			kind = spec.KindFlow
		}
		raw, _, err := spec.ReadFile(m.opt.Home, kind, req.TargetID)
		if err != nil {
			if errors.Is(err, spec.ErrNotFound) {
				return nil, fmt.Errorf("%w: %s %q", store.ErrNotFound, req.TargetKind, req.TargetID)
			}
			return nil, err
		}
		yaml = string(raw)
	case req.TargetKind == store.AssistTargetFlow:
		yaml = flowSkeleton
	default:
		yaml = skeleton
	}
	if len(yaml) > maxDraftBytes {
		return nil, invalid("yaml", "the draft is too large")
	}

	id := m.st.NewID()
	row := store.AssistSession{
		ID: id, TargetKind: req.TargetKind, TargetID: req.TargetID, Runtime: req.Runtime, Model: req.Model, Status: store.AssistIdle,
	}
	s := newSession(m, row)
	s.applyDraft(yaml, store.DraftByUser)
	if err := s.initDir(); err != nil {
		return nil, err
	}
	if err := m.st.CreateAssistSession(m.ctx, &s.row); err != nil {
		_ = os.RemoveAll(s.dir)
		return nil, err
	}
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()
	out := s.row
	return &out, nil
}

// SendMessage stores the user's message and starts one agent turn.
func (m *Manager) SendMessage(ctx context.Context, sessionID, text string, yaml *string) (*store.AssistSession, error) {
	if m.host.Paused() {
		return nil, fmt.Errorf("%w: assist turns are rejected while the kill switch is engaged", core.ErrPaused)
	}
	if yaml != nil && len(*yaml) > maxDraftBytes {
		return nil, invalid("yaml", "the draft is too large")
	}
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.send(text, yaml)
}

// UpdateDraft replaces the draft with the editor's content.
func (m *Manager) UpdateDraft(ctx context.Context, sessionID, yaml, expectedHash string) (*store.AssistSession, error) {
	if len(yaml) > maxDraftBytes {
		return nil, invalid("yaml", "the draft is too large")
	}
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.updateDraft(yaml, expectedHash)
}

// Cancel cancels the running turn.
func (m *Manager) Cancel(ctx context.Context, sessionID string) (*store.AssistSession, error) {
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.cancel(ctx)
}

// Close ends the session: it stops the agent, removes the draft directory and marks the session closed.
func (m *Manager) Close(ctx context.Context, sessionID string) error {
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := s.close(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	return nil
}

// SensorCreated makes the open sessions that were authoring a new sensor with this id sessions on it, so their
// tests and instructions use the sensor's own env keys and its saved script.
func (m *Manager) SensorCreated(ctx context.Context, sensorID string) error {
	return m.attachNew(ctx, store.AssistTargetSensor, sensorID)
}

// FlowCreated does the same for flow sessions that were authoring a new flow with this id: the saved flow's code
// becomes the base of the approval diff and the instructions say the session edits it.
func (m *Manager) FlowCreated(ctx context.Context, flowID string) error {
	return m.attachNew(ctx, store.AssistTargetFlow, flowID)
}

// ToolCreated does the same for tool sessions that were authoring a new custom tool with this id.
func (m *Manager) ToolCreated(ctx context.Context, toolID string) error {
	return m.attachNew(ctx, store.AssistTargetTool, toolID)
}

func (m *Manager) attachNew(ctx context.Context, kind, id string) error {
	page, err := m.st.ListAssistSessions(ctx, store.AssistSessionFilter{TargetKind: kind, ListOpts: store.ListOpts{Limit: 500}})
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range page.Items {
		if row.TargetID != "" || row.DraftID != id {
			continue
		}
		s, err := m.session(ctx, row.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.adopt(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ToolApprovalPending links a gated tool call to its approval and marks the session as waiting for it.
func (m *Manager) ToolApprovalPending(runID, toolCallID, approvalID string) {
	if s := m.sessionOfRun(runID); s != nil {
		s.approvalPending(runID, toolCallID, approvalID)
	}
}

// ToolApprovalResolved marks the session as running again once no approval is pending. An allowed test of a changed
// script records that script as approved.
func (m *Manager) ToolApprovalResolved(runID, approvalID string, allowed bool) {
	if s := m.sessionOfRun(runID); s != nil {
		s.approvalResolved(approvalID, allowed)
	}
}

func (m *Manager) sessionOfRun(runID string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byRun[runID]
}

// agentEnv is the whole base environment of an agent process: the node base environment without any env file
// value, the daemon's URL and the session, and a PATH that finds this hal9001 binary first.
func (m *Manager) agentEnv(sessionID string) []string {
	env := runner.DefaultBaseEnv(m.host.BasePATH())
	if dir := filepath.Dir(m.opt.Executable); dir != "" && dir != "." {
		env["PATH"] = dir + string(os.PathListSeparator) + env["PATH"]
	}
	env["HAL9001_URL"] = m.opt.URL
	env["HAL9001_ASSIST_SESSION"] = sessionID
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// allowRules are the per-turn permissions of the agent (REQUIREMENTS §7.2 gated policy plus these): edits inside
// the session's draft directory and the helper commands are allowed (a test only for a script the user has
// approved), files holding secrets are protected, and everything else goes to the approval queue.
func (s *session) allowRules() agent.AllowRules {
	m, sessionID, dir := s.m, s.id, s.dir
	testArgs, validateCmd := "", "sensors validate"
	if s.isFlow() {
		testArgs, validateCmd = `(?: --sample [0-9]+| --event [0-9A-HJKMNP-TV-Z]{26})?`, "flows validate"
	}
	bin := `(?:hal9001|` + regexp.QuoteMeta(m.opt.Executable) + `)`
	home := m.home
	data := filepath.Join(home, "data")
	rules := agent.AllowRules{
		// Rule 0 runs the draft with the user's credentials: it is vetoed unless the code is what the user has
		// tested or approved (see vetoTest).
		Commands: []string{
			`^` + bin + ` assist test(?: ` + sessionID + `)?` + testArgs + `$`,
			`^` + bin + ` assist draft(?: ` + sessionID + `)?$`,
			`^` + bin + ` ` + validateCmd + ` (?:\./)?` + regexp.QuoteMeta(s.draftFile()) + `$`,
		},
		CommandVeto: s.vetoTest,
		EditUnder:   []string{dir},
		Base:        dir,
		Protected: []string{
			filepath.Join(home, "env"), filepath.Join(home, "env.d"), filepath.Join(home, "config.yaml"),
			filepath.Join(data, "hal.db"), filepath.Join(data, "hal.db-wal"), filepath.Join(data, "hal.db-shm"),
			filepath.Join(data, "blobs"), filepath.Join(data, "runtimes"),
		},
	}
	if s.isFlow() {
		rules.EditDeny = []string{filepath.Join(dir, ReferenceDir)}
	}
	return rules
}

// redactBytes masks every workspace secret in b.
func (m *Manager) redactBytes(b []byte) []byte {
	_, _, red := m.host.Env("")
	return red.Bytes(b)
}

func invalid(path, message string) error {
	return &core.ValidationError{Diagnostics: []spec.Diagnostic{{
		Severity: spec.SeverityError, Code: spec.CodeInvalidValue, Path: path, Message: message,
	}}}
}

// availableEnv lists, sorted, every credential name a script of the sensor can read.
func (m *Manager) availableEnv(targetID string) []string {
	global, sensor := m.envNames(targetID)
	all := append(append([]string(nil), global...), sensor...)
	sort.Strings(all)
	return all
}

// envNames lists the variable names a sensor script sees: the global env file and the target sensor's own file.
func (m *Manager) envNames(targetID string) (global, sensor []string) {
	g, sv, _ := m.host.Env(targetID)
	for k := range g {
		global = append(global, k)
	}
	for k := range sv {
		sensor = append(sensor, k)
	}
	sort.Strings(global)
	sort.Strings(sensor)
	return global, sensor
}
