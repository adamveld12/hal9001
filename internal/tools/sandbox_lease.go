package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
)

var errLeaseClosed = errors.New("the conversation's sandbox is closed")

// lease is the sandbox of a conversation: a sandbox.Session that forwards every operation to the sandbox the
// conversation currently has and creates one when there is none.
//
// During a turn the lease holds its sandbox. AfterTurn hands it to the Keeper for the backend's keep_alive, and the next
// BeforeTurn (or operation) takes it back. When the Keeper closed it in the meantime the lease creates a new sandbox
// from the same image, repo and ref and BeforeTurn returns a notice for the conversation. The first acquisition takes
// over the sandbox of the agent run the conversation continues, if the Keeper still holds it.
//
// A new sandbox uses the conversation id as its workspace key, so it never meets the workspace a failed run left behind
// and its workdir can differ from the first one: the tool descriptions do not name the workdir.
type lease struct {
	p    *sandboxProvider
	run  RunSpec
	conv ConversationSandbox
	key  string // conversation id: the Keeper key of the idle sandbox and the workspace key of new sandboxes
	// ctx is the lifetime of the hub session's opener. Closing and logging use it without its cancellation.
	ctx context.Context

	mu      sync.Mutex
	cur     sandbox.Session // the sandbox in use; nil while idle in the Keeper or when there is none
	log     sandbox.RunLog  // the log of the current (or last) sandbox
	parked  bool            // the sandbox was handed to the Keeper by AfterTurn
	started bool            // the first sandbox was acquired
	closed  bool
	workdir string
	pending []string // notices produced outside BeforeTurn
}

var (
	_ sandbox.Session   = (*lease)(nil)
	_ sandbox.Describer = (*lease)(nil)
)

// provideLease returns the extension of a conversation's sandbox. No sandbox is created yet: the first BeforeTurn does.
func (p *sandboxProvider) provideLease(ctx context.Context, run RunSpec, policy *sandbox.Policy) (Extension, error) {
	cfg, err := p.m.Config(run.Sandbox)
	if err != nil {
		return Extension{}, fmt.Errorf("sandbox %s: %w", run.Sandbox, err)
	}
	if !cfg.Enabled {
		return Extension{}, fmt.Errorf("sandbox %s: %w", run.Sandbox, sandbox.ErrDisabled)
	}
	conv := *run.Conversation
	if conv.ConversationID == "" {
		return Extension{}, errors.New("sandbox: conversation without id")
	}
	l := &lease{p: p, run: run, conv: conv, key: conv.ConversationID, ctx: context.WithoutCancel(ctx)}
	st := &sandboxTools{name: run.Sandbox, sess: l, policy: policy, logFn: l.currentLog, lease: true}
	return Extension{
		Tools:      st.tools(),
		Sandbox:    l,
		BeforeTurn: l.BeforeTurn,
		AfterTurn:  l.AfterTurn,
		CloseWith:  func(bool) { _ = l.Close(l.ctx, true) },
	}, nil
}

func (l *lease) currentLog() sandbox.RunLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.log
}

// BeforeTurn makes sure the conversation has a sandbox and returns the notices for the user.
func (l *lease) BeforeTurn(ctx context.Context) ([]string, error) {
	_, notice, err := l.acquire(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	notices := l.pending
	l.pending = nil
	l.mu.Unlock()
	if notice != "" {
		notices = append(notices, notice)
	}
	return notices, nil
}

// AfterTurn hands the sandbox to the Keeper for keep_alive; with a keep_alive of 0 it is closed now.
func (l *lease) AfterTurn() {
	l.mu.Lock()
	if l.closed || l.cur == nil {
		l.mu.Unlock()
		return
	}
	sess, lg := l.cur, l.log
	l.cur = nil
	keep := l.p.keepAlive(l.run.Sandbox, spec.DefaultSandboxKeepAlive.Std())
	if keep > 0 && l.p.opt.Keeper != nil {
		if lg != nil {
			lg.Add(sandbox.LogEntry{
				Kind: sandbox.KindLifecycle, Title: "Reply finished; sandbox kept for " + spec.Duration(keep).String(),
				Fields: map[string]string{"keep_alive": spec.Duration(keep).String()},
			})
		}
		l.parked = true
		l.p.opt.Keeper.Hold(l.key, sess, keep, true)
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	_ = sess.Close(l.ctx, true)
}

// acquire returns the current sandbox, taking it back from the Keeper or creating it when needed. The notice, when
// not empty, tells the user that a new sandbox replaced the earlier one.
func (l *lease) acquire(ctx context.Context) (sandbox.Session, string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, "", errLeaseClosed
	}
	if l.cur != nil {
		return l.cur, "", nil
	}
	keeper := l.p.opt.Keeper
	if l.parked {
		l.parked = false
		if keeper != nil {
			if s, ok := keeper.Take(l.key); ok {
				l.cur = s
				return s, "", nil
			}
		}
		return l.recreate(ctx, true, "")
	}
	if !l.started {
		l.started = true
		if from := l.conv.FromAgentRunID; from != "" {
			if keeper != nil {
				if s, ok := keeper.Take(from); ok {
					return l.takeOver(s, from), "", nil
				}
			}
			return l.recreate(ctx, true, from)
		}
		return l.recreate(ctx, false, "")
	}
	return l.recreate(ctx, true, "")
}

// session is acquire for an operation: a notice produced here is kept for the next BeforeTurn.
func (l *lease) session(ctx context.Context) (sandbox.Session, error) {
	s, notice, err := l.acquire(ctx)
	if notice != "" {
		l.mu.Lock()
		l.pending = append(l.pending, notice)
		l.mu.Unlock()
	}
	return s, err
}

// takeOver makes the sandbox of agent run from the conversation's: a new log session for the conversation gets the
// sandbox's header, the run's log session ends with a hand-over entry. The caller holds mu.
func (l *lease) takeOver(s sandbox.Session, from string) sandbox.Session {
	l.cur = s
	if d, ok := s.(sandbox.Describer); ok {
		l.workdir = d.Describe().Workdir
	}
	lg := l.newLog()
	if lg == nil {
		return s
	}
	ns, oldLog, ok := sandbox.Adopt(s, lg)
	if !ok {
		return s
	}
	l.cur, l.log = ns, lg
	info := sandbox.SessionInfo{Workdir: l.workdir}
	if d, ok := ns.(sandbox.Describer); ok {
		info = d.Describe()
		l.workdir = info.Workdir
	}
	lg.Update(sandbox.SessionUpdate{Status: sandbox.StatusReady, Info: info})
	lg.Add(sandbox.LogEntry{
		Kind: sandbox.KindLifecycle, Title: "Reattached sandbox of run " + from,
		Fields: map[string]string{"agent_run": from, "conversation": l.key, "workdir": info.Workdir},
	})
	if oldLog != nil {
		oldLog.Add(sandbox.LogEntry{
			Kind: sandbox.KindLifecycle, Title: "Sandbox handed over to conversation " + l.key,
			Fields: map[string]string{"conversation": l.key},
		})
		oldLog.Update(sandbox.SessionUpdate{Status: sandbox.StatusClosed})
	}
	return ns
}

// recreate opens a new sandbox from the same image, repo and ref. With notify, it returns the notice that the earlier
// sandbox is gone; fromRun is the agent run whose sandbox the conversation could not take over (empty otherwise). The
// caller holds mu.
func (l *lease) recreate(ctx context.Context, notify bool, fromRun string) (sandbox.Session, string, error) {
	var notice string
	if notify {
		notice = l.goneNotice(fromRun)
	}
	lg := l.newLog()
	if lg != nil && notice != "" {
		lg.Add(sandbox.LogEntry{Kind: sandbox.KindLifecycle, Level: sandbox.LevelWarn, Title: notice})
	}
	rs := l.p.runSpec(ctx, l.run, l.run.Info(), l.key)
	if len(l.conv.Input) > 0 {
		rs.Input = l.conv.Input
	}
	rs.Log = lg
	sess, err := l.p.m.Open(ctx, l.run.Sandbox, rs)
	if err != nil {
		return nil, "", fmt.Errorf("sandbox %s: %w", l.run.Sandbox, err)
	}
	l.cur, l.log = sess, lg
	l.workdir = sess.Workdir()
	return sess, notice, nil
}

// newLog opens the log session of a new sandbox of this conversation (nil when no log is configured or it cannot be recorded).
func (l *lease) newLog() sandbox.RunLog {
	if l.p.opt.Log == nil {
		return nil
	}
	return l.p.opt.Log(l.ctx, l.run, l.run.Info())
}

// goneNotice is the notice for a conversation whose earlier sandbox was removed. For a sandbox of an agent run that
// the conversation could not take over (fromRun) the reason is not known: the keep_alive may have passed, another chat
// may hold it, or the daemon stopped in between.
func (l *lease) goneNotice(fromRun string) string {
	var gone string
	cfg, err := l.p.m.Config(l.run.Sandbox)
	switch {
	case err == nil && cfg.KeepAlive == 0:
		gone = "The sandbox was removed as soon as it was not in use (keep_alive is 0)"
	case err == nil && fromRun != "":
		gone = "The sandbox of run " + fromRun + " is no longer available (keep_alive is " + cfg.KeepAlive.String() + ")"
	case err == nil:
		gone = "The sandbox was removed after " + cfg.KeepAlive.String() + " without activity"
	default:
		gone = "The sandbox was removed"
	}
	var from []string
	if l.run.Image != "" {
		from = append(from, l.run.Image)
	}
	if l.run.Repo != "" {
		r := sandbox.CleanRepo(l.run.Repo)
		if l.run.Ref != "" {
			r += "@" + l.run.Ref
		}
		from = append(from, r)
	}
	src := "the sandbox's default image"
	if len(from) > 0 {
		src = strings.Join(from, " / ")
	}
	msg := gone + ", so a new one was created from " + src + "."
	persistentLocal := l.run.Sandbox == spec.SandboxLocal && err == nil && cfg.Lifecycle == spec.LifecyclePersistent
	if !persistentLocal {
		msg += " Changes made by the earlier run are gone."
	}
	return msg
}

func (l *lease) Exec(ctx context.Context, cmd string, stdin []byte, timeout time.Duration) (sandbox.ExecResult, error) {
	s, err := l.session(ctx)
	if err != nil {
		return sandbox.ExecResult{}, err
	}
	return s.Exec(ctx, cmd, stdin, timeout)
}

func (l *lease) ReadFile(ctx context.Context, path string) ([]byte, error) {
	s, err := l.session(ctx)
	if err != nil {
		return nil, err
	}
	return s.ReadFile(ctx, path)
}

func (l *lease) WriteFile(ctx context.Context, path string, data []byte) error {
	s, err := l.session(ctx)
	if err != nil {
		return err
	}
	return s.WriteFile(ctx, path, data)
}

func (l *lease) List(ctx context.Context, path string) ([]sandbox.Entry, error) {
	s, err := l.session(ctx)
	if err != nil {
		return nil, err
	}
	return s.List(ctx, path)
}

func (l *lease) Search(ctx context.Context, path, pattern string) ([]sandbox.Match, error) {
	s, err := l.session(ctx)
	if err != nil {
		return nil, err
	}
	return s.Search(ctx, path, pattern)
}

// Workdir is the workdir of the current sandbox, or of the last one while there is none.
func (l *lease) Workdir() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.workdir
}

// Describe describes the current sandbox (empty while there is none).
func (l *lease) Describe() sandbox.SessionInfo {
	l.mu.Lock()
	cur, wd := l.cur, l.workdir
	l.mu.Unlock()
	if d, ok := cur.(sandbox.Describer); ok {
		return d.Describe()
	}
	return sandbox.SessionInfo{Workdir: wd}
}

// Close closes the sandbox right away, whether it is in use or idle in the Keeper, and ends the lease. A chat has no
// run outcome, so the sandbox is always closed as for a successful run (its workspace is removed).
func (l *lease) Close(ctx context.Context, _ bool) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	cur, parked := l.cur, l.parked
	l.cur, l.parked = nil, false
	if cur == nil && parked && l.p.opt.Keeper != nil {
		if s, ok := l.p.opt.Keeper.Take(l.key); ok {
			cur = s
		}
	}
	l.mu.Unlock()
	if cur == nil {
		return nil
	}
	return cur.Close(ctx, true)
}
