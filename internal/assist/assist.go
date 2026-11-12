// Package assist runs AI-assisted sensor authoring: a chat session with a coding agent (an ACP runtime from
// config.yaml) that edits a draft sensor or flow.
//
// The agent has no file or shell access (docs/acp-lockdown.md). It reads and changes the draft through hal's MCP
// tools draft_read, draft_write, draft_test and validate (tools.go); the draft lives in a private directory,
// <home>/data/assist/<session>/, that only the daemon touches. Only the user's Save, through the normal sensor
// endpoints, persists a sensor. The agent never receives secrets: its environment holds no env-file values, and
// draft_test runs the draft through the daemon (TestDraft), which overlays the global env file and the target
// sensor's own env values, redacts the output, and records nothing.
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
	"hal9001/internal/tools"
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
	// Track registers a cancelable unit so Stop and a denied-and-cancelled tool call can cancel it; the returned function
	// unregisters it. The kill switch does not touch assist units: it only stops sensors and flow runs.
	Track(id string, cancel context.CancelCauseFunc) (untrack func())
	// Runtime returns a configured runtime with its placeholders resolved.
	Runtime(name string) (agent.RuntimeConfig, bool)
	// Env returns copies of the global env file values and of the sensor's own values (sensorID may be empty),
	// and a redactor that masks every secret of the workspace.
	Env(sensorID string) (global, sensor map[string]string, red *runner.Redactor)
	// ToolEnv is Env for a custom tool: the global env file values, the tool's own values (toolID may be empty)
	// and the workspace redactor.
	ToolEnv(toolID string) (global, tool map[string]string, red *runner.Redactor)
	// BasePATH is the PATH nodes and agents start with.
	BasePATH() string
	// Recorder returns the recorder that persists the agent runs of an assist session.
	Recorder(assistSessionID, model string) agent.Recorder
	// Broker is the approval queue.
	Broker() agent.ApprovalBroker
	// Tools is the hub of hal's per-run MCP tool sessions; every agent process gets one.
	Tools() *tools.Hub
	Clock() agent.Clock
	// TestFlow runs a validated flow draft once on one input as a dry-run test run and returns what every node did.
	TestFlow(ctx context.Context, req core.FlowTestRequest) (core.AssistFlowTestResult, error)
	// ForkNode resolves the agent node run a conversation is forked from: its flow, node settings (runtime, sandbox,
	// custom tools, emit targets) and the original ACP session. store.ErrNotFound for an unknown or non-flow agent run.
	ForkNode(ctx context.Context, agentRunID string) (core.ForkNode, error)
	// EmitTools returns the emit tools of a conversation forked from fork (emit_<port>, or emit_out for a node without
	// ports). A call does not start anything by itself: it raises an approval request, and only an approval starts a
	// new flow run of kind continuation. conversationID is the assist session id.
	EmitTools(ctx context.Context, fork core.ForkNode, conversationID string) ([]tools.Tool, error)
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
	// Executable is the hal9001 binary agents run as their `hal9001 mcp` server. Default: this process's executable.
	Executable string
	// IdleTimeout suspends an idle agent process (default 15m).
	IdleTimeout time.Duration
	// Version is hal9001's version, for the diagnostics block of github_issue_link.
	Version string
	// MCP is the remote MCP client behind mcp_servers_list, mcp_server_inspect and mcp_call, and the run token of
	// draft tests of sensors that declare mcp_servers. Optional.
	MCP MCPClient
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
	ui       UIPublisher // where navigate_ui and start_draft publish; nil until SetUIPublisher
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
	m := &Manager{
		opt: opt, st: opt.Store, host: opt.Host, log: opt.Log, ctx: context.WithoutCancel(opt.Host.Context()),
		home:     home,
		dir:      filepath.Join(home, "data", "assist"),
		sessions: map[string]*session{}, byRun: map[string]*session{},
	}
	m.registerTools()
	return m, nil
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

// CreateSession starts a session: its draft directory and draft (a general session has the directory only), no agent
// process yet.
func (m *Manager) CreateSession(ctx context.Context, req core.AssistCreateRequest) (*store.AssistSession, error) {
	switch req.TargetKind {
	case store.AssistTargetGeneral:
		if req.TargetID != "" {
			return nil, invalid("target.id", "a general session has no target id")
		}
	case store.AssistTargetSensor, store.AssistTargetFlow, store.AssistTargetTool:
	default:
		return nil, invalid("target.kind", "target.kind must be general, sensor, flow or tool")
	}
	if _, ok := m.host.Runtime(req.Runtime); !ok {
		return nil, &core.ValidationError{Diagnostics: []spec.Diagnostic{{
			Severity: spec.SeverityError, Code: spec.CodeUnknownRuntime, Path: "runtime", Message: fmt.Sprintf("runtime %q is not configured in config.yaml", req.Runtime),
		}}}
	}
	var yaml string
	switch {
	case req.TargetKind == store.AssistTargetGeneral:
		// no draft
	case req.YAML != nil:
		yaml = *req.YAML
	case req.TargetID != "":
		kind := spec.KindSensor
		switch req.TargetKind {
		case store.AssistTargetFlow:
			kind = spec.KindFlow
		case store.AssistTargetTool:
			kind = spec.KindTool
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
	case req.TargetKind == store.AssistTargetTool:
		yaml = toolSkeleton
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

// SendMessage stores the user's message and starts one agent turn. page, when non-nil, is rendered into this turn's
// prompt only (see renderPageContext). A general session has no draft and ignores yaml.
func (m *Manager) SendMessage(ctx context.Context, sessionID, text string, yaml *string, page *core.AssistPageContext) (*store.AssistSession, error) {
	if yaml != nil && len(*yaml) > maxDraftBytes {
		return nil, invalid("yaml", "the draft is too large")
	}
	s, err := m.session(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return s.send(text, yaml, page)
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

// TargetRenamed points the open sessions on file oldID of kind at newID, so a rename keeps the drawer attached.
func (m *Manager) TargetRenamed(ctx context.Context, kind, oldID, newID string) error {
	page, err := m.st.ListAssistSessions(ctx, store.AssistSessionFilter{TargetKind: kind, TargetID: oldID, ListOpts: store.ListOpts{Limit: 500}})
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range page.Items {
		s, err := m.session(ctx, row.ID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := s.retarget(oldID, newID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

// agentEnv is the whole base environment of an agent process: the node base environment without any env file value.
func (m *Manager) agentEnv() []string {
	env := runner.DefaultBaseEnv(m.host.BasePATH())
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
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
