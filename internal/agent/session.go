package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

// Session errors.
var (
	// ErrTurnRunning is returned by Prompt while another turn is running.
	ErrTurnRunning = errors.New("a turn is already running in this session")
	// ErrSessionClosed is returned by Prompt after Close.
	ErrSessionClosed = errors.New("session is closed")
)

// SessionRequest configures a Session. The process is not started until the
// first Prompt.
type SessionRequest struct {
	// Runtime is the runtime's name in config (recorded only).
	Runtime string
	Config  RuntimeConfig
	// Cwd must exist.
	Cwd string
	// Model is an optional model id for the agent's "model" config option.
	Model string
	// Env is extra KEY=VALUE environment for the agent process.
	Env        []string
	MCPServers []MCPServer
	// ToolsReady: see RunRequest.ToolsReady.
	ToolsReady func(ctx context.Context)
	// LoadSessionID is an ACP session id from an earlier process of the same
	// conversation. The first start tries session/load for it (when the agent
	// advertises loadSession) and falls back to session/new.
	LoadSessionID string
}

// TurnRequest is one session/prompt.
type TurnRequest struct {
	// RunID is the AgentRun id of this turn; a ULID is generated when empty.
	RunID string
	// Prompt is sent to session/prompt and recorded as RunRecord.Prompt.
	Prompt string
	// FallbackPrompt is sent (and recorded) instead of Prompt when this turn
	// had to start a new ACP session although an earlier session id was known
	// (LoadSessionID, or the process died and session/load was unavailable or
	// failed). It lets the caller include a recap of the conversation. Empty:
	// Prompt is used.
	FallbackPrompt string
	// Allow are extra permission rules for this turn.
	Allow AllowRules
	// Timeout bounds the turn including a (re)start (default 30m).
	Timeout time.Duration
	// ApprovalTimeout bounds a tool approval wait (default 1h).
	ApprovalTimeout time.Duration
}

// Session is a long-lived agent process serving several turns (session/prompt
// calls) of one ACP session. Each turn is its own AgentRun. All turns run
// under the gated policy (§7.2): read-class calls are allowed, everything else
// is allowed by the turn's AllowRules or decided through the approval broker.
//
// A process that exits between turns is restarted by the next Prompt, which
// reloads the ACP session with session/load when the agent supports it.
type Session struct {
	e   *Executor
	req SessionRequest
	rt  *router

	mu sync.Mutex // guards everything below
	// closed is set by Close.
	closed bool
	// turnDone is non-nil while a turn runs; cancelTurn cancels it.
	turnDone   chan struct{}
	cancelTurn context.CancelCauseFunc
	// live is the running process, nil when none is.
	live *liveProc
	// acpID is the ACP session id of the live or last process.
	acpID string
	// done is closed when the current process has exited or the session was
	// closed; a restarted process gets a new channel.
	done chan struct{}
}

// liveProc is one running agent process and what its start learned.
type liveProc struct {
	conn *acp.ClientSideConnection
	proc *process
	argv []string
	info *AgentInfo
	caps json.RawMessage
}

func (lp *liveProc) alive() bool {
	select {
	case <-lp.conn.Done():
		return false
	default:
	}
	return !lp.proc.waitExit(0)
}

// NewSession validates req and returns a Session. No process is started yet.
func (e *Executor) NewSession(req SessionRequest) (*Session, error) {
	switch {
	case len(req.Config.Command) == 0:
		return nil, errors.New("runtime command is empty")
	case e.Recorder == nil:
		return nil, errors.New("Executor.Recorder is nil")
	case e.Broker == nil:
		return nil, errors.New("sessions need an ApprovalBroker")
	}
	if _, err := os.Stat(req.Cwd); err != nil {
		return nil, fmt.Errorf("cwd: %w", err)
	}
	return &Session{e: e, req: req, rt: &router{}, done: make(chan struct{})}, nil
}

// ACPSessionID is the id of the live (or last) ACP session; empty before the
// first start.
func (s *Session) ACPSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acpID
}

// Alive reports whether the agent process is running and its connection open.
func (s *Session) Alive() bool {
	s.mu.Lock()
	lp := s.live
	s.mu.Unlock()
	return lp != nil && lp.alive()
}

// Done returns a channel closed when the current process has exited or Close
// was called. A restarted process gets a new channel, so call Done again after
// each Prompt.
func (s *Session) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// Prompt runs one turn and returns its record, which is also delivered through
// Recorder.RunUpdated as the turn progresses. The error is only for an invalid
// request, a closed session or a turn already running; runtime failures are
// reported in the record.
//
// A turn starts the agent process when none is running, so start failures land
// on this turn's record. Cancel ctx with a cause (ErrCancelled, ErrKilled,
// ErrInterrupted) to say why the turn ends.
func (s *Session) Prompt(ctx context.Context, req TurnRequest) (RunRecord, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return RunRecord{}, errors.New("prompt is empty")
	}
	r, end, err := s.beginTurn(ctx, req)
	if err != nil {
		return RunRecord{}, err
	}
	defer end()

	r.save()
	lp, keep := s.drive(r)
	s.rt.set(nil)
	if !keep && lp != nil {
		s.mu.Lock()
		s.detachLocked(lp)
		s.mu.Unlock()
		r.shutdown()
	}
	r.finish()
	s.endTurn()
	return r.snapshot(), nil
}

// beginTurn registers a new turn: it checks the session state and builds the
// turn's run with its own context.
func (s *Session) beginTurn(ctx context.Context, req TurnRequest) (*run, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return nil, nil, ErrSessionClosed
	case s.turnDone != nil:
		return nil, nil, ErrTurnRunning
	}
	loadID := s.acpID
	if loadID == "" {
		loadID = s.req.LoadSessionID
	}
	r, err := newRun(s.e, plan{
		req: RunRequest{
			RunID:           req.RunID,
			Runtime:         s.req.Runtime,
			Config:          s.req.Config,
			Mode:            ModeApprove,
			Approve:         ApprovePerTool,
			Prompt:          req.Prompt,
			Model:           s.req.Model,
			Cwd:             s.req.Cwd,
			MCPServers:      s.req.MCPServers,
			ToolsReady:      s.req.ToolsReady,
			Allow:           req.Allow,
			Timeout:         req.Timeout,
			ApprovalTimeout: req.ApprovalTimeout,
			Env:             s.req.Env,
		},
		phase:          PhaseExecute,
		policy:         PolicyFor(ModeApprove, ApprovePerTool, PhaseExecute),
		prompt:         req.Prompt,
		loadSession:    loadID,
		fallbackPrompt: req.FallbackPrompt,
	})
	if err != nil {
		return nil, nil, err
	}
	end := r.begin(ctx, orDefault(req.Timeout, DefaultTimeout))
	s.turnDone, s.cancelTurn = make(chan struct{}), r.cancel
	s.rt.set(r)
	return r, end, nil
}

// endTurn releases the turn and retires a process that died during it.
func (s *Session) endTurn() {
	s.mu.Lock()
	close(s.turnDone)
	s.turnDone, s.cancelTurn = nil, nil
	var dead *liveProc
	if lp := s.live; lp != nil && !lp.alive() {
		s.detachLocked(lp)
		dead = lp
	}
	s.mu.Unlock()
	if dead != nil {
		s.stop(dead)
	}
}

// drive runs the turn on the live process, starting one when needed. It
// returns the process the turn used (nil when it could not be spawned) and
// whether that process may keep serving the session.
func (s *Session) drive(r *run) (lp *liveProc, keep bool) {
	lp = s.reapStale()
	if lp != nil {
		s.attach(r, lp)
	} else {
		var ok bool
		if lp, ok = s.start(r); !ok {
			return lp, false
		}
	}
	r.prompt()
	return lp, r.processUsable()
}

// reapStale returns the live process, or nil after stopping one that died
// while no turn was running.
func (s *Session) reapStale() *liveProc {
	s.mu.Lock()
	lp := s.live
	if lp == nil || lp.alive() {
		s.mu.Unlock()
		return lp
	}
	s.detachLocked(lp)
	s.mu.Unlock()
	s.stop(lp)
	return nil
}

// attach binds the turn to the process that is already running.
func (s *Session) attach(r *run, lp *liveProc) {
	r.conn, r.proc = lp.conn, lp.proc
	id := s.ACPSessionID()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Argv, r.rec.AgentInfo, r.rec.Capabilities, r.rec.SessionID = lp.argv, lp.info, lp.caps, id
}

// start spawns the agent for the turn and runs the handshake (initialize,
// session/load or session/new, mode, model). ok reports whether the prompt may
// be sent; lp is nil when the spawn failed.
func (s *Session) start(r *run) (lp *liveProc, ok bool) {
	conn, proc, err := s.e.dial(r.argv(), r.rec.Cwd, r.environ(), s.rt, s.rt.rawFrame)
	if err != nil {
		r.fail(ReasonSpawn, err)
		return nil, false
	}
	r.conn, r.proc = conn, proc
	lp = &liveProc{conn: conn, proc: proc, argv: r.argv()}
	r.mu.Lock()
	r.rec.Argv = lp.argv
	r.mu.Unlock()

	s.mu.Lock()
	select {
	case <-s.done:
		s.done = make(chan struct{})
	default:
	}
	s.live = lp
	s.mu.Unlock()
	go s.watch(lp)

	ok = r.handshake()
	r.mu.Lock()
	lp.info, lp.caps = r.rec.AgentInfo, r.rec.Capabilities
	id := r.rec.SessionID
	r.mu.Unlock()
	if id != "" {
		s.mu.Lock()
		s.acpID = id
		s.mu.Unlock()
	}
	if ok {
		r.save()
	}
	return lp, ok
}

// watch retires the process when it exits between turns. A process that dies
// during a turn is retired by the turn.
func (s *Session) watch(lp *liveProc) {
	select {
	case <-lp.conn.Done():
	case <-lp.proc.done:
	}
	s.mu.Lock()
	if s.live != lp || s.turnDone != nil {
		s.mu.Unlock()
		return
	}
	s.detachLocked(lp)
	s.mu.Unlock()
	s.stop(lp)
}

// detachLocked forgets lp as the live process and signals Done. The caller
// stops the process afterwards, outside the lock.
func (s *Session) detachLocked(lp *liveProc) {
	if lp == nil || s.live != lp {
		return
	}
	s.live = nil
	s.closeDoneLocked()
}

func (s *Session) closeDoneLocked() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// stop ends a detached process: stdin EOF, SIGTERM, SIGKILL.
func (s *Session) stop(lp *liveProc) {
	lp.proc.stop(orDefault(s.e.ShutdownGrace, DefaultShutdownGrace), orDefault(s.e.KillGrace, DefaultKillGrace))
}

// Suspend stops the process but keeps the session usable: the next Prompt
// restarts it and resumes the ACP session with session/load. It does nothing
// while a turn runs.
func (s *Session) Suspend() {
	s.mu.Lock()
	lp := s.live
	if s.closed || s.turnDone != nil || lp == nil {
		s.mu.Unlock()
		return
	}
	s.detachLocked(lp)
	s.mu.Unlock()
	s.stop(lp)
}

// Close stops the process and makes later Prompt calls return
// ErrSessionClosed. A running turn is cancelled (it ends killed) and Close
// returns once the process is gone. Close is idempotent.
func (s *Session) Close() {
	s.mu.Lock()
	s.closed = true
	cancel, turnDone := s.cancelTurn, s.turnDone
	s.mu.Unlock()
	if cancel != nil {
		cancel(ErrKilled)
		<-turnDone
	}
	s.mu.Lock()
	lp := s.live
	s.detachLocked(lp)
	s.closeDoneLocked()
	s.mu.Unlock()
	if lp != nil {
		s.stop(lp)
	}
}

// processUsable reports whether the agent process can serve another turn once
// this one is over: the agent answered every call, is still running, and the
// turn was not ended by a cause that kills the process. A turn ended by a
// user cancel, a timeout or a denied-and-cancelled approval was told to stop
// with session/cancel; if the agent answered, it is ready for the next turn.
func (r *run) processUsable() bool {
	r.mu.Lock()
	promptErr := r.promptErr
	r.mu.Unlock()
	if promptErr != nil {
		return false
	}
	switch cause := context.Cause(r.ctx); {
	case cause == nil,
		errors.Is(cause, ErrCancelled),
		errors.Is(cause, errTimeout),
		errors.Is(cause, errApprovalTimeout),
		errors.Is(cause, ErrDeniedCancel):
	default:
		return false
	}
	select {
	case <-r.conn.Done():
		return false
	default:
	}
	return !r.proc.waitExit(0)
}

// router is the acp.Client of a session's connection. It forwards calls to the
// turn that is running; with no turn, updates (such as a session/load replay)
// are dropped and permission requests are answered cancelled.
type router struct {
	noCapabilities

	mu  sync.Mutex
	cur *run
}

var _ acp.Client = (*router)(nil)

func (rt *router) set(r *run) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.cur = r
}

func (rt *router) turn() *run {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.cur
}

func (rt *router) rawFrame(dir string, line []byte) {
	if r := rt.turn(); r != nil {
		r.rawFrame(dir, line)
	}
}

func (rt *router) SessionUpdate(ctx context.Context, n acp.SessionNotification) error {
	if r := rt.turn(); r != nil {
		return r.SessionUpdate(ctx, n)
	}
	return nil
}

func (rt *router) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	if r := rt.turn(); r != nil {
		return r.RequestPermission(ctx, p)
	}
	return cancelledResponse(), nil
}

func (rt *router) UnstableCreateElicitation(ctx context.Context, req acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	if r := rt.turn(); r != nil {
		return r.UnstableCreateElicitation(ctx, req)
	}
	return acp.UnstableCreateElicitationResponse{Cancel: &acp.UnstableCreateElicitationCancel{Action: "cancel"}}, nil
}

func (rt *router) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil
}
