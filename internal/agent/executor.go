package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/oklog/ulid/v2"
)

// ProposalSuffix is appended to the prompt of an approve+proposal phase 1.
const ProposalSuffix = "\n\nInvestigate, then propose a concrete plan. Do not change anything: this phase is read-only, and your plan will be reviewed before anything is executed."

// ApprovedPrompt opens the phase 2 prompt of an approved proposal.
const ApprovedPrompt = "Approved. Execute the plan."

// Executor runs agent processes. The zero value is not usable: set Recorder
// and Broker (Broker may be nil only if no run uses approve mode).
type Executor struct {
	Recorder Recorder
	Broker   ApprovalBroker
	// Clock defaults to the wall clock.
	Clock Clock
	// Log defaults to slog.Default().
	Log *slog.Logger
	// Environ is the base environment of agent processes (e.g. the daemon's
	// env plus the env file); nil means os.Environ().
	Environ []string
	// ClientVersion is reported to the agent in initialize.
	ClientVersion string

	// KillGrace is how long a SIGTERMed process group gets before SIGKILL
	// (default 10s). CancelGrace is how long the agent gets to answer
	// session/cancel before the process is signalled (default 5s).
	// ShutdownGrace is how long a finished agent gets to exit on stdin EOF
	// before SIGTERM (default 3s).
	KillGrace     time.Duration
	CancelGrace   time.Duration
	ShutdownGrace time.Duration
}

func (e *Executor) clock() Clock {
	if e.Clock != nil {
		return e.Clock
	}
	return WallClock{}
}

func (e *Executor) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// plan is a validated unit of work for execute.
type plan struct {
	req    RunRequest
	phase  Phase
	policy Policy
	// prompt is sent to session/prompt.
	prompt      string
	parentRunID string
	// loadSession, when set, is the session to restore via session/load.
	loadSession string
	// fallbackPrompt is sent instead when session/load is unavailable and a
	// fresh session is created.
	fallbackPrompt string
}

func (e *Executor) validate(req *RunRequest) error {
	switch req.Mode {
	case ModeAuto, ModeReadonly:
	case ModeApprove:
		switch req.style() {
		case ApproveProposal, ApprovePerTool:
		default:
			return fmt.Errorf("unknown approve style %q", req.Approve)
		}
		if e.Broker == nil {
			return errors.New("approve mode needs an ApprovalBroker")
		}
	default:
		return fmt.Errorf("unknown agent mode %q", req.Mode)
	}
	if len(req.Config.Command) == 0 {
		return errors.New("runtime command is empty")
	}
	if req.Cwd == "" {
		return errors.New("cwd is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return errors.New("prompt is empty")
	}
	if e.Recorder == nil {
		return errors.New("Executor.Recorder is nil")
	}
	return nil
}

// Run executes an agent node. For approve+proposal it runs phase 1 and, on
// success, returns a record with Status awaiting_approval, the Proposal and the
// broker's ApprovalID; the agent process has exited. Resume continues it.
//
// The returned error is only for an invalid request; every runtime failure
// is reported in the record. ctx cancellation ends the run: cancel it with
// ErrKilled (kill switch) or ErrInterrupted (shutdown) as the cause.
func (e *Executor) Run(ctx context.Context, req RunRequest) (RunRecord, error) {
	if err := e.validate(&req); err != nil {
		return RunRecord{}, err
	}
	p := plan{req: req, phase: PhaseExecute, prompt: req.Prompt}
	if req.Mode == ModeApprove && req.style() == ApproveProposal {
		p.phase = PhasePropose
		p.prompt = req.Prompt + ProposalSuffix
	}
	p.policy = PolicyFor(req.Mode, req.style(), p.phase)
	return e.execute(ctx, p)
}

// Resume runs phase 2 of an approved proposal under the auto policy. It
// reloads the proposal's session when the agent advertises loadSession and
// otherwise starts a new session whose prompt carries the original prompt, the
// proposal and the comment; RunRecord.ResumedVia says which happened.
func (e *Executor) Resume(ctx context.Context, rr ResumeRequest) (RunRecord, error) {
	req := rr.RunRequest
	req.Mode, req.Approve = ModeApprove, ApproveProposal
	if err := e.validate(&req); err != nil {
		return RunRecord{}, err
	}
	if rr.SessionID == "" || strings.TrimSpace(rr.Proposal) == "" {
		return RunRecord{}, errors.New("resume needs the proposal's session id and proposal text")
	}
	approved := ApprovedPrompt
	if c := strings.TrimSpace(rr.Comment); c != "" {
		approved += "\n\n" + c
	}
	fallback := rr.Prompt + "\n\n---\nYour earlier proposal for this task:\n\n" + rr.Proposal + "\n---\n\n" + approved
	return e.execute(ctx, plan{
		req:            req,
		phase:          PhaseExecute,
		policy:         PolicyFor(ModeApprove, ApproveProposal, PhaseExecute),
		prompt:         approved,
		parentRunID:    rr.PrevRunID,
		loadSession:    rr.SessionID,
		fallbackPrompt: fallback,
	})
}

// DenyProposal records a denied proposal on its propose-phase run: the node
// routes to port denied and no further agent process runs.
func (e *Executor) DenyProposal(rec RunRecord) RunRecord {
	now := e.clock().Now()
	rec.Status = StatusDenied
	rec.FailureReason = ReasonDenied
	rec.EndedAt = &now
	e.Recorder.RunUpdated(rec)
	return rec
}

func (e *Executor) execute(parent context.Context, p plan) (RunRecord, error) {
	r, err := newRun(e, p)
	if err != nil {
		return RunRecord{}, err
	}
	end := r.begin(parent, orDefault(p.req.Timeout, DefaultTimeout))
	defer end()

	r.save()
	r.drive()
	r.finish()
	return r.snapshot(), nil
}

// begin gives the run its context: cancelled by the caller (with a cause) or
// by the timeout. The returned func releases it.
func (r *run) begin(parent context.Context, timeout time.Duration) (end func()) {
	ctx, cancel := context.WithCancelCause(parent)
	r.ctx, r.cancel = ctx, cancel
	timer := r.e.clock().After(timeout)
	go func() {
		select {
		case <-timer:
			cancel(errTimeout)
		case <-ctx.Done():
		}
	}()
	return func() { cancel(nil) }
}

// dial starts the agent process and opens the ACP connection on its stdio. Raw
// JSON-RPC lines are reported to raw.
func (e *Executor) dial(argv []string, cwd string, env []string, client acp.Client, raw func(dir string, line []byte)) (*acp.ClientSideConnection, *process, error) {
	proc, err := startProcess(argv, cwd, env)
	if err != nil {
		return nil, nil, err
	}
	gate := newUpdateGate()
	proc.updates = gate
	tw := newTeeWriter(proc.stdin, func(l []byte) { raw("send", l) })
	tr := newTeeReader(proc.stdout, func(l []byte) {
		gate.observe(l)
		raw("recv", l)
	})
	conn := acp.NewClientSideConnection(gatedClient{Client: client, gate: gate}, tw, tr)
	conn.SetLogger(e.log())
	proc.eof = conn.Done()
	return conn, proc, nil
}

func newID() string { return ulid.Make().String() }

func newRun(e *Executor, p plan) (*run, error) {
	cwd := p.req.Cwd
	if _, err := os.Stat(cwd); err != nil {
		return nil, fmt.Errorf("cwd: %w", err)
	}
	if p.req.Allow.Base == "" {
		p.req.Allow.Base = cwd
	}
	eng, err := NewEngine(p.policy, p.req.Allow)
	if err != nil {
		return nil, err
	}
	id := p.req.RunID
	if id == "" {
		id = newID()
	}
	var style ApproveStyle
	if p.req.Mode == ModeApprove {
		style = p.req.style()
	}
	r := &run{
		e:      e,
		p:      p,
		engine: eng,
		tools:  map[string]*toolInfo{},
		rec: RunRecord{
			ID:          id,
			NodeRunID:   p.req.NodeRunID,
			ParentRunID: p.parentRunID,
			Runtime:     p.req.Runtime,
			Cwd:         cwd,
			Mode:        p.req.Mode,
			Approve:     style,
			Policy:      p.policy,
			Phase:       p.phase,
			Prompt:      p.prompt,
			Status:      StatusRunning,
			StartedAt:   e.clock().Now(),
		},
	}
	return r, nil
}
