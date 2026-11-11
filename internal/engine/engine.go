package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runtimes"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// Options configures New.
type Options struct {
	Store *store.Store
	// Home is the hal9001 home dir; run data lives under Home/data.
	Home string
	// BasePATH is the PATH nodes and agents start with (the login-shell PATH
	// captured at install time); empty means the daemon's own PATH.
	BasePATH string
	// Notifier delivers macOS notifications; nil disables them.
	Notifier core.Notifier
	Log      *slog.Logger
	// UIBaseURL is the origin of notification deep links, e.g. "http://localhost:9001".
	UIBaseURL string
	// Clock drives sensor schedules and agent run and approval timeouts; nil means the wall clock.
	Clock agent.Clock
	// MCPURL is the daemon's own base URL, handed to hal's MCP server in every agent session (e.g.
	// "http://127.0.0.1:41233"). It must be the address the daemon actually listens on, not the UI URL.
	MCPURL string
	// Executable is the hal9001 binary agents spawn as `hal9001 mcp`; default: this process's executable.
	Executable string
	// Runtimes finds the agents of runtimes that have no command of their own; default: a manager over Home/data
	// and BasePATH.
	Runtimes *runtimes.Manager
}

// Engine executes sensors, flows and agent runs. It implements core.Engine.
type Engine struct {
	opt   Options
	st    *store.Store
	log   *slog.Logger
	home  string
	data  string
	clock agent.Clock
	wt    *agent.Worktrees

	// ctx is the engine's lifetime: cancelled by Stop. Work started by
	// callers' requests runs on it, never on the request's context. dbCtx is
	// its uncancelable twin for bookkeeping that must complete during shutdown.
	ctx    context.Context
	cancel context.CancelCauseFunc
	dbCtx  context.Context

	mu   sync.RWMutex // guards snap
	snap *snapshot

	startOnce sync.Once
	started   atomic.Bool
	spawnMu   sync.Mutex // orders spawn against Stop so wg.Add never races wg.Wait
	stopping  atomic.Bool
	wg        sync.WaitGroup
	kick      chan struct{}

	ctl    sync.Mutex // guards ks and active
	ks     store.KillSwitch
	paused atomic.Bool
	active map[string]activeUnit

	pollMu  sync.Mutex
	polling map[string]bool

	loopMu sync.Mutex
	loops  map[string]*sensorLoop

	flowCache sync.Map // flowID@hash -> *spec.Flow
	locks     keyedMutex

	waitMu  sync.Mutex
	waiters map[string]chan agent.ToolDecision // tool_call approval id -> decision

	runtimes    *runtimes.Manager
	ownRuntimes bool // Stop closes the manager the engine created itself
	broker      *storeBroker
	observer    observerSlot
	tools       *tools.Hub

	assistTests sync.Map // flow run id -> *assistTest, for the runs of TestFlow
	simEmits    sync.Map // flow run id -> map[node id]core.SimulatedEmit: agent nodes of test runs that emit instead of running
	simulations sync.Map // flow run id -> "approved"|"denied": the simulated decision of approval nodes in test runs
}

// New returns an engine over opt.Store. Call Apply with the loaded workspace,
// then Start.
func New(opt Options) (*Engine, error) {
	if opt.Store == nil {
		return nil, errors.New("engine: Options.Store is required")
	}
	if opt.Home == "" {
		return nil, errors.New("engine: Options.Home is required")
	}
	home, err := filepath.Abs(opt.Home)
	if err != nil {
		return nil, fmt.Errorf("engine: resolve home: %w", err)
	}
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}
	clock := opt.Clock
	if clock == nil {
		clock = agent.WallClock{}
	}
	data := filepath.Join(home, "data")
	e := &Engine{
		opt:     opt,
		st:      opt.Store,
		log:     log,
		home:    home,
		data:    data,
		clock:   clock,
		wt:      &agent.Worktrees{Dir: filepath.Join(data, "worktrees")},
		kick:    make(chan struct{}, 1),
		active:  map[string]activeUnit{},
		polling: map[string]bool{},
		loops:   map[string]*sensorLoop{},
		waiters: map[string]chan agent.ToolDecision{},
	}
	e.ctx, e.cancel = context.WithCancelCause(context.Background())
	e.dbCtx = context.WithoutCancel(e.ctx)
	e.broker = &storeBroker{e: e}
	e.snap = newSnapshot(&spec.Workspace{Home: home})
	e.runtimes = opt.Runtimes
	if e.runtimes == nil {
		e.ownRuntimes = true
		e.runtimes = runtimes.New(runtimes.Options{
			Data: data, Path: opt.BasePATH, Log: log,
			EnvFile: func() map[string]string { return e.snapshot().env },
		})
	}
	if e.tools, err = e.newToolHub(); err != nil {
		return nil, fmt.Errorf("engine: tool hub: %w", err)
	}
	return e, nil
}

var _ core.Engine = (*Engine)(nil)

// Runtimes returns the manager that detects and installs the agents of runtimes.
func (e *Engine) Runtimes() *runtimes.Manager { return e.runtimes }

// Start reconciles state left by a previous process (running work becomes
// interrupted, per-tool approvals expire), then starts the dispatcher, the
// sensor schedulers and the retention job. It returns once they are running;
// ctx is only consulted for the startup queries, the engine runs until Stop.
func (e *Engine) Start(ctx context.Context) error {
	var err error
	e.startOnce.Do(func() { err = e.start(ctx) })
	return err
}

func (e *Engine) start(ctx context.Context) error {
	res, err := e.st.MarkInterrupted(ctx)
	if err != nil {
		return fmt.Errorf("engine: mark interrupted: %w", err)
	}
	// Runs do not survive a restart, so neither do their MCP capability tokens.
	if _, err := e.st.RevokeAllMCPTokens(ctx); err != nil {
		return fmt.Errorf("engine: revoke mcp tokens: %w", err)
	}
	if res != (store.InterruptResult{}) {
		e.log.Info("engine: reconciled interrupted work", "result", res)
	}
	ks, err := e.st.GetKillSwitch(ctx)
	if err != nil {
		return fmt.Errorf("engine: load kill switch: %w", err)
	}
	e.ctl.Lock()
	e.ks = ks
	e.paused.Store(ks.Paused)
	e.ctl.Unlock()
	if err := e.reconcileProposals(ctx); err != nil {
		return fmt.Errorf("engine: reconcile proposals: %w", err)
	}
	if err := e.reconcileRequests(ctx); err != nil {
		return fmt.Errorf("engine: reconcile approval nodes: %w", err)
	}
	e.started.Store(true)
	e.spawn(e.dispatchLoop)
	e.spawn(e.retentionLoop)
	e.spawn(e.requestExpiryLoop)
	e.syncSensors()
	e.wake()
	return nil
}

// Stop shuts the engine down: schedulers stop, running nodes are cancelled
// with agent.ErrInterrupted and recorded as interrupted (proposal approvals
// stay pending and survive), and Stop waits for them until ctx ends.
func (e *Engine) Stop(ctx context.Context) error {
	e.spawnMu.Lock()
	already := e.stopping.Swap(true)
	e.spawnMu.Unlock()
	if already {
		return nil
	}
	e.stopLoops()
	e.cancelActive(agent.ErrInterrupted)
	e.cancel(agent.ErrInterrupted)
	if e.ownRuntimes {
		e.runtimes.Close()
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine: stop: %w", ctx.Err())
	}
}

// spawn runs fn on the engine's lifetime and tracks it for Stop. It reports
// false, without running fn, once Stop has begun.
func (e *Engine) spawn(fn func(ctx context.Context)) bool {
	e.spawnMu.Lock()
	defer e.spawnMu.Unlock()
	if e.stopping.Load() {
		return false
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		fn(e.ctx)
	}()
	return true
}

// wake nudges the dispatcher.
func (e *Engine) wake() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// track registers a cancelable running unit (a node run or poll) so the kill
// switch and Stop can cancel it; the returned function unregisters it. If the
// kill switch already demands killing running work, it is cancelled at once.
func (e *Engine) track(id string, cancel context.CancelCauseFunc) (untrack func()) {
	e.ctl.Lock()
	e.active[id] = activeUnit{cancel: cancel, killable: true}
	killNow := e.ks.Paused && e.ks.KillRunning
	e.ctl.Unlock()
	if killNow {
		cancel(agent.ErrKilled)
	}
	return e.untracker(id)
}

// trackExempt is track for a unit the kill switch must not cancel (an assist turn or test): Stop and cancelOne still do.
func (e *Engine) trackExempt(id string, cancel context.CancelCauseFunc) (untrack func()) {
	e.ctl.Lock()
	e.active[id] = activeUnit{cancel: cancel}
	e.ctl.Unlock()
	return e.untracker(id)
}

func (e *Engine) untracker(id string) func() {
	return func() {
		e.ctl.Lock()
		delete(e.active, id)
		e.ctl.Unlock()
	}
}

// activeUnit is a running, cancelable unit of work. killable: the kill switch's "pause and kill running" cancels it.
type activeUnit struct {
	cancel   context.CancelCauseFunc
	killable bool
}

// cancelOne cancels one tracked unit (a node run, or "assist:<run id>").
func (e *Engine) cancelOne(id string, cause error) {
	e.ctl.Lock()
	u := e.active[id]
	e.ctl.Unlock()
	if u.cancel != nil {
		u.cancel(cause)
	}
}

// cancelActive cancels every tracked unit with cause; the kill switch (agent.ErrKilled) leaves the exempt ones.
func (e *Engine) cancelActive(cause error) {
	e.ctl.Lock()
	fns := make([]context.CancelCauseFunc, 0, len(e.active))
	for _, u := range e.active {
		if u.killable || !errors.Is(cause, agent.ErrKilled) {
			fns = append(fns, u.cancel)
		}
	}
	e.ctl.Unlock()
	for _, c := range fns {
		c(cause)
	}
}

func (e *Engine) isPaused() bool { return e.paused.Load() }

// keyedMutex serialises work per key (one flow run's reconciliation, one node
// run's approval handling).
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	ent := k.m[key]
	if ent == nil {
		ent = &keyedEntry{}
		k.m[key] = ent
	}
	ent.refs++
	k.mu.Unlock()
	ent.mu.Lock()
	return func() {
		ent.mu.Unlock()
		k.mu.Lock()
		ent.refs--
		if ent.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}

// ensureDir creates dir (and parents) and returns it.
func ensureDir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("engine: create %s: %w", dir, err)
	}
	return dir, nil
}
