package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
)

// toolInfo is what we know about a tool call from session/update, used to
// fill in fields a permission request omits.
type toolInfo struct {
	id      string
	kind    string
	title   string
	status  string
	rawIn   any
	content json.RawMessage
}

// run is the state of one agent process / AgentRun. It is also the
// acp.Client the agent talks to.
type run struct {
	e      *Executor
	p      plan
	engine *Engine

	ctx    context.Context
	cancel context.CancelCauseFunc

	conn *acp.ClientSideConnection
	proc *process

	mu        sync.Mutex // guards everything below and serialises Recorder calls
	rec       RunRecord
	seq       int
	tools     map[string]*toolInfo
	toolOrder []string        // tool call ids in arrival order
	cur       strings.Builder // current contiguous agent message run
	full      strings.Builder
	replaying bool // session/load replay: updates are not recorded as events
	modes     *acp.SessionModeState
	configs   []acp.SessionConfigOption
	prompted  bool // prompt returned a response (no transport error)
	promptErr error
	stop      acp.StopReason
	// lastReject is the latest tool call the harness rejected. Agents such as
	// codex end the whole turn (stopReason cancelled) when a call is rejected.
	lastReject *rejection

	rawMu sync.Mutex
}

var _ acp.Client = (*run)(nil)

func (r *run) snapshot() RunRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec
}

// save persists the record. Callers must not hold r.mu.
func (r *run) save() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.e.Recorder.RunUpdated(r.rec)
}

func (r *run) now() time.Time { return r.e.clock().Now() }

func (r *run) harness(payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.e.Recorder.Event(Event{RunID: r.rec.ID, Seq: r.seq, TS: r.now(), Kind: "harness", Payload: b})
}

// fail records the first failure; later ones are ignored.
func (r *run) fail(reason string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.rec.FailureReason != "" {
		return
	}
	r.rec.FailureReason = reason
	if err != nil {
		r.rec.Error = err.Error()
	}
}

func (r *run) failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.FailureReason != ""
}

// callFailed classifies a failed ACP call: a cancelled run is reported by
// finish from the cancellation cause; a dead process is an agent exit.
func (r *run) callFailed(what string, err error) {
	if r.ctx.Err() != nil {
		return
	}
	if r.processGone() {
		r.fail(ReasonAgentExit, fmt.Errorf("%s: %w (%s)", what, err, describeExit(r.proc)))
	} else {
		r.fail(ReasonAgentError, fmt.Errorf("%s: %w", what, err))
	}
}

// processGone reports whether the agent process has exited. When the
// connection already dropped, the exit is imminent, so give it a moment to be
// reaped instead of racing it.
func (r *run) processGone() bool {
	select {
	case <-r.conn.Done():
		return r.proc.waitExit(2 * time.Second)
	default:
		return r.proc.waitExit(0)
	}
}

func (r *run) argv() []string {
	spawn := r.p.req.Config.Ask
	if r.p.policy == PolicyAuto {
		spawn = r.p.req.Config.Full
	}
	argv := append([]string(nil), r.p.req.Config.Command...)
	return append(argv, spawn.Args...)
}

func (r *run) environ() []string {
	base := r.e.Environ
	if base == nil {
		base = osEnviron()
	}
	cfg := r.p.req.Config
	spawn := cfg.Ask
	if r.p.policy == PolicyAuto {
		spawn = cfg.Full
	}
	env := buildEnv(base, cfg.Env, spawn.Env)
	return append(env, r.p.req.Env...)
}

func (r *run) modeName() string {
	if r.p.policy == PolicyAuto {
		return r.p.req.Config.Modes.Full
	}
	return r.p.req.Config.Modes.Ask
}

func (r *run) rawFrame(dir string, line []byte) {
	r.rawMu.Lock()
	defer r.rawMu.Unlock()
	r.e.Recorder.Raw(RawFrame{RunID: r.rec.ID, TS: r.now(), Dir: dir, Line: line})
}

// drive runs the whole session: spawn, handshake, prompt, shutdown. It leaves
// the outcome in r.rec / r.stop / r.promptErr for finish.
func (r *run) drive() {
	proc, err := startProcess(r.argv(), r.rec.Cwd, r.environ())
	if err != nil {
		r.fail(ReasonSpawn, err)
		return
	}
	r.proc = proc
	r.mu.Lock()
	r.rec.Argv = r.argv()
	r.mu.Unlock()
	defer r.shutdown()

	tw := newTeeWriter(proc.stdin, func(l []byte) { r.rawFrame("send", l) })
	tr := newTeeReader(proc.stdout, func(l []byte) { r.rawFrame("recv", l) })
	r.conn = acp.NewClientSideConnection(r, tw, tr)
	r.conn.SetLogger(r.e.log())

	if !r.handshake() {
		return
	}
	r.save()
	r.prompt()
}

// handshake does initialize, session setup, mode and model. It reports
// whether the prompt may be sent.
func (r *run) handshake() bool {
	init, err := r.conn.Initialize(r.ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		// No fs or terminal capabilities: agents use their own tools (§7.3).
		ClientCapabilities: r.clientCapabilities(),
		ClientInfo:         &acp.Implementation{Name: "hal9001", Version: r.e.ClientVersion},
	})
	if err != nil {
		r.callFailed("initialize", err)
		return false
	}
	caps, _ := json.Marshal(init.AgentCapabilities)
	r.mu.Lock()
	r.rec.Capabilities = caps
	if init.AgentInfo != nil {
		ai := &AgentInfo{Name: init.AgentInfo.Name, Version: init.AgentInfo.Version}
		if init.AgentInfo.Title != nil {
			ai.Title = *init.AgentInfo.Title
		}
		r.rec.AgentInfo = ai
	}
	r.mu.Unlock()

	mcp := convertMCP(r.p.req.MCPServers)
	loaded := false
	if r.p.loadSession != "" {
		switch {
		case !init.AgentCapabilities.LoadSession:
			r.mu.Lock()
			r.rec.ResumeFallbackReason = "agent does not advertise loadSession"
			r.mu.Unlock()
		default:
			r.mu.Lock()
			r.replaying = true
			r.mu.Unlock()
			resp, err := r.conn.LoadSession(r.ctx, acp.LoadSessionRequest{SessionId: acp.SessionId(r.p.loadSession), Cwd: r.rec.Cwd, McpServers: mcp, Meta: r.p.req.Config.SessionMeta})
			r.mu.Lock()
			r.replaying = false
			r.mu.Unlock()
			if err != nil {
				if r.ctx.Err() != nil {
					return false
				}
				r.mu.Lock()
				r.rec.ResumeFallbackReason = "session/load failed: " + err.Error()
				r.mu.Unlock()
				break
			}
			loaded = true
			r.mu.Lock()
			r.rec.SessionID = r.p.loadSession
			r.rec.ResumedVia = ResumedLoad
			r.modes, r.configs = resp.Modes, resp.ConfigOptions
			r.mu.Unlock()
		}
	}
	if !loaded {
		resp, err := r.conn.NewSession(r.ctx, acp.NewSessionRequest{Cwd: r.rec.Cwd, McpServers: mcp, Meta: r.p.req.Config.SessionMeta})
		if err != nil {
			r.callFailed("session/new", err)
			return false
		}
		r.mu.Lock()
		r.rec.SessionID = string(resp.SessionId)
		if r.p.loadSession != "" {
			r.rec.ResumedVia = ResumedNewSession
			r.rec.Prompt = r.p.fallbackPrompt
		}
		r.modes, r.configs = resp.Modes, resp.ConfigOptions
		r.mu.Unlock()
	}
	r.save()

	if !r.applyMode() {
		return false
	}
	return r.applyModel()
}

func convertMCP(in []MCPServer) []acp.McpServer {
	out := make([]acp.McpServer, 0, len(in))
	for _, s := range in {
		switch s.Type {
		case "http", "sse":
			hdrs := make([]acp.HttpHeader, 0, len(s.Headers))
			for k, v := range s.Headers {
				hdrs = append(hdrs, acp.HttpHeader{Name: k, Value: v})
			}
			if s.Type == "http" {
				out = append(out, acp.McpServer{Http: &acp.McpServerHttpInline{Name: s.Name, Type: "http", Url: s.URL, Headers: hdrs}})
			} else {
				out = append(out, acp.McpServer{Sse: &acp.McpServerSseInline{Name: s.Name, Type: "sse", Url: s.URL, Headers: hdrs}})
			}
		default:
			env := make([]acp.EnvVariable, 0, len(s.Env))
			for k, v := range s.Env {
				env = append(env, acp.EnvVariable{Name: k, Value: v})
			}
			args := s.Args
			if args == nil {
				args = []string{}
			}
			out = append(out, acp.McpServer{Stdio: &acp.McpServerStdio{Name: s.Name, Command: s.Command, Args: args, Env: env}})
		}
	}
	return out
}

func (r *run) configSelect(cat acp.SessionConfigOptionCategory) *acp.SessionConfigOptionSelect {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, o := range r.configs {
		if o.Select != nil && o.Select.Category != nil && *o.Select.Category == cat {
			return o.Select
		}
	}
	return nil
}

func selectValues(s *acp.SessionConfigOptionSelect) map[string]bool {
	vals := map[string]bool{}
	if g := s.Options.Ungrouped; g != nil {
		for _, o := range *g {
			vals[string(o.Value)] = true
		}
	}
	if g := s.Options.Grouped; g != nil {
		for _, grp := range *g {
			for _, o := range grp.Options {
				vals[string(o.Value)] = true
			}
		}
	}
	return vals
}

// applyMode sets the runtime's session mode for the policy. An empty mode
// name in the runtime config means the runtime is restricted by spawn
// arguments instead, so nothing is called.
func (r *run) applyMode() bool {
	name := r.modeName()
	if name == "" {
		return true
	}
	sid := acp.SessionId(r.rec.SessionID)
	r.mu.Lock()
	modes := r.modes
	r.mu.Unlock()
	var err error
	switch sel := r.configSelect(acp.SessionConfigOptionCategoryMode); {
	case modes != nil:
		found := false
		var avail []string
		for _, m := range modes.AvailableModes {
			avail = append(avail, string(m.Id))
			found = found || string(m.Id) == name
		}
		if !found {
			r.fail(ReasonModeUnavailable, fmt.Errorf("runtime does not offer session mode %q (offers %v)", name, avail))
			return false
		}
		_, err = r.conn.SetSessionMode(r.ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: acp.SessionModeId(name)})
	case sel != nil && selectValues(sel)[name]:
		_, err = r.conn.SetSessionConfigOption(r.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{SessionId: sid, ConfigId: sel.Id, Value: acp.SessionConfigValueId(name)}})
	default:
		_, err = r.conn.SetSessionMode(r.ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: acp.SessionModeId(name)})
	}
	if err != nil {
		if r.ctx.Err() == nil {
			r.fail(ReasonModeUnavailable, fmt.Errorf("set session mode %q: %w", name, err))
		}
		return false
	}
	r.harness(map[string]any{"action": "set_mode", "mode": name, "policy": r.p.policy})
	return true
}

// applyModel sets the node's model through the agent's model config option.
// An agent without one keeps its default; that is noted in the transcript.
func (r *run) applyModel() bool {
	model := r.p.req.Model
	if model == "" {
		return true
	}
	sel := r.configSelect(acp.SessionConfigOptionCategoryModel)
	if sel == nil {
		r.harness(map[string]any{"action": "set_model", "model": model, "applied": false, "note": "agent offers no model config option; using its default"})
		return true
	}
	_, err := r.conn.SetSessionConfigOption(r.ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: acp.SessionId(r.rec.SessionID), ConfigId: sel.Id, Value: acp.SessionConfigValueId(model),
	}})
	if err != nil {
		if r.ctx.Err() == nil {
			r.fail(ReasonModelRejected, fmt.Errorf("set model %q: %w", model, err))
		}
		return false
	}
	r.harness(map[string]any{"action": "set_model", "model": model, "applied": true})
	return true
}

// prompt sends session/prompt and waits for its response. On cancellation of
// the run it sends session/cancel and gives the agent CancelGrace to answer.
func (r *run) prompt() {
	sid := acp.SessionId(r.rec.SessionID)
	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		// Not bound to r.ctx: cancellation is orchestrated below so the
		// agent can still answer session/cancel with stopReason cancelled.
		resp, err := r.conn.Prompt(context.Background(), acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(r.rec.Prompt)}})
		done <- result{resp, err}
	}()

	var res result
	select {
	case res = <-done:
	case <-r.ctx.Done():
		_ = r.conn.Cancel(context.Background(), acp.CancelNotification{SessionId: sid})
		t := time.NewTimer(orDefault(r.e.CancelGrace, DefaultCancelGrace))
		defer t.Stop()
		select {
		case res = <-done:
		case <-t.C:
			res.err = errors.New("agent did not answer session/cancel in time")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if res.err != nil {
		r.promptErr = res.err
		return
	}
	r.prompted = true
	r.stop = res.resp.StopReason
}

// shutdown stops the process and records exit details and the stderr tail.
func (r *run) shutdown() {
	exitWait := orDefault(r.e.ShutdownGrace, DefaultShutdownGrace)
	if r.ctx.Err() != nil {
		exitWait = 0 // the run was cancelled: go straight to SIGTERM
	}
	r.proc.stop(exitWait, orDefault(r.e.KillGrace, DefaultKillGrace))
	code := r.proc.exitCode()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.ExitCode = &code
	r.rec.Stderr = r.proc.stderr.String()
}

// finish derives the final status. The agent's completed response wins over a
// cancellation that raced it; otherwise the cancellation cause decides.
func (r *run) finish() {
	r.mu.Lock()
	rec := &r.rec
	rec.FinalMessage = strings.TrimSpace(r.cur.String())
	rec.FullMessage = strings.TrimSpace(r.full.String())
	if rec.FinalMessage == "" {
		rec.FinalMessage = rec.FullMessage
	}
	rec.StopReason = string(r.stop)
	completed := r.prompted && r.stop != acp.StopReasonCancelled
	promptErr := r.promptErr
	r.mu.Unlock()

	cause := context.Cause(r.ctx)
	switch {
	case completed && !r.failed():
		r.finishCompleted()
		return
	case errors.Is(cause, errTimeout):
		r.end(StatusTimeout, ReasonTimeout, "run exceeded its timeout")
	case errors.Is(cause, errApprovalTimeout):
		r.end(StatusFailed, ReasonApprovalTimeout, "a tool approval was not decided in time")
	case errors.Is(cause, errDeniedCancel):
		r.end(StatusDenied, ReasonDenied, "a tool call was denied and the run cancelled")
	case errors.Is(cause, ErrInterrupted):
		r.end(StatusInterrupted, ReasonInterrupted, "run interrupted")
	case cause != nil:
		r.end(StatusKilled, ReasonKilled, "run killed")
	case r.failed():
		r.end(StatusFailed, "", "")
	case r.stop == acp.StopReasonCancelled && r.hasRejection():
		r.rejectionEndedTurn()
	case promptErr != nil:
		reason := ReasonAgentError
		if r.processGone() {
			reason = ReasonAgentExit
			promptErr = fmt.Errorf("%w (%s)", promptErr, describeExit(r.proc))
		}
		r.end(StatusFailed, reason, promptErr.Error())
	default:
		r.end(StatusFailed, ReasonStopReason, "agent ended the turn with stopReason "+string(r.stop))
	}
}

// end finalises a non-successful run; reason/msg are kept if already set.
func (r *run) end(st Status, reason, msg string) {
	now := r.now()
	r.mu.Lock()
	r.rec.Status = st
	r.rec.EndedAt = &now
	if r.rec.FailureReason == "" {
		r.rec.FailureReason = reason
	}
	if r.rec.Error == "" {
		r.rec.Error = msg
	}
	rec := r.rec
	r.e.Recorder.RunUpdated(rec)
	r.mu.Unlock()
}

func (r *run) finishCompleted() {
	r.mu.Lock()
	stop := r.stop
	final, full := r.rec.FinalMessage, r.rec.FullMessage
	r.mu.Unlock()

	switch stop {
	case acp.StopReasonEndTurn:
	default:
		r.end(StatusFailed, ReasonStopReason, "agent ended the turn with stopReason "+string(stop))
		return
	}

	if r.p.phase == PhasePropose {
		if final == "" {
			r.end(StatusFailed, ReasonEmptyProposal, "agent produced no proposal text")
			return
		}
		id, err := r.e.Broker.SubmitProposal(context.WithoutCancel(r.ctx), ProposalApproval{
			RunID: r.rec.ID, NodeRunID: r.rec.NodeRunID, SessionID: r.rec.SessionID, Proposal: final,
		})
		if err != nil {
			r.end(StatusFailed, ReasonAgentError, "register proposal approval: "+err.Error())
			return
		}
		now := r.now()
		r.mu.Lock()
		r.rec.Proposal, r.rec.ApprovalID = final, id
		r.rec.Status = StatusAwaitingApproval
		r.rec.EndedAt = &now
		r.e.Recorder.RunUpdated(r.rec)
		r.mu.Unlock()
		return
	}

	now := r.now()
	r.mu.Lock()
	r.rec.Status = StatusSucceeded
	r.rec.EndedAt = &now
	if r.p.req.ExpectNote {
		note, err := ParseNote(final)
		if err != nil {
			note, err = parseNoteFallback(final, full, err)
		}
		if err != nil {
			r.rec.NoteParseError = err.Error()
		} else {
			r.rec.Note = &note
		}
	}
	r.e.Recorder.RunUpdated(r.rec)
	r.mu.Unlock()
}

// parseNoteFallback retries on the whole message when the final message run
// held no note (the agent may have split its answer around tool calls).
func parseNoteFallback(final, full string, first error) (Note, error) {
	if full != "" && full != final {
		if n, err := ParseNote(full); err == nil {
			return n, nil
		}
	}
	return Note{}, first
}

// clientCapabilities advertises nothing but, optionally, form elicitation.
func (r *run) clientCapabilities() acp.ClientCapabilities {
	var c acp.ClientCapabilities
	if r.p.req.Config.Elicitation {
		c.Elicitation = &acp.ElicitationCapabilities{Form: &acp.ElicitationFormCapabilities{}}
	}
	return c
}

// rejection is a tool call the harness answered with reject.
type rejection struct {
	by    string // DecidedByPolicy or DecidedByUser
	title string
}

func (r *run) hasRejection() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastReject != nil
}

// rejectionEndedTurn classifies a turn the agent ended with stopReason
// cancelled right after the harness rejected a call: a user's deny is a
// denied run, a policy rejection is a failure that says so.
func (r *run) rejectionEndedTurn() {
	r.mu.Lock()
	rej := *r.lastReject
	r.mu.Unlock()
	if rej.by == DecidedByUser {
		r.end(StatusDenied, ReasonDenied, fmt.Sprintf("agent ended its turn after the user denied %q", rej.title))
		return
	}
	r.end(StatusFailed, ReasonPolicyEndedTurn, fmt.Sprintf("agent ended its turn after the policy rejected %q", rej.title))
}

// awaitToolDetails waits briefly for the tool_call notification of id.
func (r *run) awaitToolDetails(id string) *toolInfo {
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		r.mu.Lock()
		ti := r.tools[id]
		if ti != nil && (ti.title != "" || ti.rawIn != nil) {
			cp := *ti
			r.mu.Unlock()
			return &cp
		}
		r.mu.Unlock()
		if time.Now().After(deadline) || r.ctx.Err() != nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}
