package assist

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"hal9001/internal/agent"
	"hal9001/internal/conversation"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// A fork is a general session that continues the agent run of a flow node (docs/assist.md, "Continue a run in a chat").
// The row records the run (forked_from_agent_run_id). Everything about the node (runtime, sandbox, custom tools, the
// ports the chat may emit on, the original ACP session) is resolved from that run through Host.ForkNode whenever the
// agent process starts, so it follows the flow version the run executed and survives a daemon restart.

// forked reports whether the session continues an agent run.
func (s *session) forked() bool { return s.row.ForkedFromAgentRunID != "" }

// Fork creates the chat. The original run stays as it is.
func (m *Manager) Fork(ctx context.Context, agentRunID string) (*store.AssistSession, error) {
	if !ulidRE.MatchString(agentRunID) {
		return nil, fmt.Errorf("%w: agent run %q", store.ErrNotFound, agentRunID)
	}
	chain, err := m.st.AgentRunChain(ctx, agentRunID)
	if err != nil {
		return nil, err
	}
	head := chain[len(chain)-1]
	fork, err := m.host.ForkNode(ctx, head)
	if err != nil {
		return nil, err
	}
	run, err := m.st.GetAgentRun(ctx, head)
	if err != nil {
		return nil, err
	}
	switch run.Status {
	case store.AgentQueued, store.AgentRunning, store.AgentAwaitingApproval:
		return nil, fmt.Errorf("%w: the agent run is %s; continue it in a chat after it ended", core.ErrInvalidState, run.Status)
	}
	if _, ok := m.host.Runtime(fork.Runtime); !ok {
		return nil, &core.ValidationError{Diagnostics: []spec.Diagnostic{{
			Severity: spec.SeverityError, Code: spec.CodeUnknownRuntime, Path: "runtime",
			Message: fmt.Sprintf("runtime %q of the node is no longer configured in config.yaml", fork.Runtime),
		}}}
	}

	row := store.AssistSession{
		ID: m.st.NewID(), TargetKind: store.AssistTargetGeneral, Runtime: fork.Runtime, Model: fork.Model, Status: store.AssistIdle,
		ForkedFromAgentRunID: head,
	}
	s := newSession(m, row)
	s.fork = &fork
	s.applyDraft("", store.DraftByUser)
	if err := s.initDir(); err != nil {
		return nil, err
	}
	if err := m.st.CreateAssistSession(m.ctx, &s.row); err != nil {
		_ = os.RemoveAll(s.dir)
		return nil, err
	}
	divider := dividerText(head, "", "")
	if err := m.st.CreateAssistMessage(m.ctx, &store.AssistMessage{SessionID: s.id, Role: "system", Kind: store.MessageDivider, Text: &divider}); err != nil {
		m.log.Error("assist: store fork divider", "session", s.id, "err", err)
	}
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	if fork.Sandbox != "" {
		s.takeOverSandbox()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyRowLocked(), nil
}

// takeOverSandbox opens the session's hal tools now instead of at the first turn, and runs the before-turn hook, so a
// sandbox that the original run's keep-alive still holds is handed over at once and cannot expire while the user types
// the first message. A failure is not the fork's: the first turn opens the tools again and reports the error there.
func (s *session) takeOverSandbox() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.agentSessionLocked(); err != nil {
		s.m.log.Warn("assist: prepare forked session", "session", s.id, "err", err)
		return
	}
	notices, err := s.halTools.BeforeTurn(s.m.host.Context())
	s.postNotices(notices)
	if err != nil {
		s.m.log.Warn("assist: take over sandbox", "session", s.id, "err", err)
		s.dropAgentLocked()
	}
}

// dropAgentLocked closes the agent session and its hal tools (nothing has run in them). s.mu must be held.
func (s *session) dropAgentLocked() {
	if s.ag != nil {
		s.ag.Close()
		s.halTools.Close()
	}
	s.ag, s.halTools = nil, nil
}

// postNotices stores system notices as notice messages of the conversation.
func (s *session) postNotices(notices []string) {
	for _, n := range notices {
		if strings.TrimSpace(n) == "" {
			continue
		}
		text := s.redact(n)
		msg := &store.AssistMessage{SessionID: s.id, Role: "system", Kind: store.MessageNotice, Text: &text}
		if err := s.m.st.CreateAssistMessage(s.m.ctx, msg); err != nil {
			s.m.log.Error("assist: store notice", "session", s.id, "err", err)
		}
	}
}

// dividerText is the line at the start of a forked chat. contextKind is store.ForkResumed or store.ForkReplayed once
// the first turn showed how the run's context was restored; reason is why session/load was not used, when it was tried.
func dividerText(runID, contextKind, reason string) string {
	t := "Continued from run " + runID
	switch contextKind {
	case store.ForkResumed:
		t += "; session resumed"
	case store.ForkReplayed:
		t += "; earlier context replayed"
		if reason != "" {
			t += " (" + reason + ")"
		}
	}
	return t
}

// forkNodeLocked resolves the node of the run the session continues. It is nil (without an error) when the run no
// longer exists: the chat then goes on as a general chat. s.mu must be held.
func (s *session) forkNodeLocked() (*core.ForkNode, error) {
	if s.fork != nil {
		return s.fork, nil
	}
	f, err := s.m.host.ForkNode(s.m.host.Context(), s.row.ForkedFromAgentRunID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.fork = &f
	return s.fork, nil
}

// forkPlan says how the agent session of a fork starts.
type forkPlan struct {
	// resume: the first turn loads the run's own ACP session (session/load) instead of receiving a replay.
	resume bool
	// cwd is the agent's working directory; loadID the ACP session to load ("" starts a new one).
	cwd, loadID string
}

// planForkLocked decides how the agent session starts (docs/assist.md, "Resume or replay"). The run's own session is
// loaded when the runtime recorded loadSession for the run, the run has a session id and its working directory still
// exists (the runtime finds its session under that directory). Otherwise the chat starts a new session in its own
// directory and the first prompt carries a compacted transcript. Once the first turn has shown how the context was
// restored (fork_context) later starts keep that working directory and load the chat's own session. s.mu must be held.
func (s *session) planForkLocked(f core.ForkNode) forkPlan {
	resumable := f.LoadSession && f.SessionID != "" && dirExists(f.Cwd)
	switch s.row.ForkContext {
	case store.ForkResumed:
		p := forkPlan{cwd: s.dir, loadID: firstNonEmpty(s.row.ACPSessionID, f.SessionID)}
		if dirExists(f.Cwd) {
			p.cwd = f.Cwd
		}
		return p
	case store.ForkReplayed:
		return forkPlan{cwd: s.dir, loadID: s.row.ACPSessionID}
	}
	switch {
	case s.row.ACPSessionID != "": // an earlier first turn was interrupted after its session existed
		p := forkPlan{cwd: s.dir, loadID: s.row.ACPSessionID}
		if resumable {
			p.cwd = f.Cwd
		}
		return p
	case resumable:
		return forkPlan{resume: true, cwd: f.Cwd, loadID: f.SessionID}
	}
	return forkPlan{cwd: s.dir}
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// forkRunSpecLocked fills the tool session spec of a fork from its node: the node's sandbox and custom tools, the
// conversation sandbox lease and the emit tools. A failure of EmitTools leaves the chat without them and is reported as
// a notice. s.mu must be held.
func (s *session) forkRunSpecLocked(rs *tools.RunSpec, f core.ForkNode) {
	rs.Sandbox, rs.Image, rs.Repo, rs.Ref = f.Sandbox, f.Image, f.Repo, f.Ref
	rs.Tools, rs.ToolApprove = f.ToolIDs, f.ToolApprove
	rs.Conversation = &tools.ConversationSandbox{ConversationID: s.id, FromAgentRunID: f.AgentRunID, NodeRunID: f.NodeRunID, Input: f.Input}
	extra, err := s.m.host.EmitTools(s.m.host.Context(), f, s.id)
	if err != nil {
		s.m.log.Warn("assist: emit tools of a forked chat", "session", s.id, "err", err)
		s.postNotices([]string{"Sending results back to the flow is not available in this chat: " + err.Error()})
		return
	}
	rs.ExtraTools = extra
}

// hasAgentOutput reports whether an agent turn of this chat already produced something: the context of the run has then
// reached the agent.
func hasAgentOutput(msgs []store.AssistMessage) bool {
	for _, m := range msgs {
		if m.AgentRunID != "" && (m.Role == "assistant" || m.Role == "tool") {
			return true
		}
	}
	return false
}

// forkContextBlock is the compacted transcript of the original run, as the first prompt of a replayed fork or as the
// fallback of a resumed one.
func (s *session) forkContextBlock(ctx context.Context, f core.ForkNode) (string, error) {
	msgs, err := conversation.Service{Store: s.m.st}.Messages(ctx, f.AgentRunID)
	if err != nil {
		return "", err
	}
	return "Transcript of run " + f.AgentRunID + " (flow " + f.FlowID + ", node " + f.NodeID + "), shortened:\n\n" +
		conversation.Compact(msgs.Items, conversation.DefaultBudget), nil
}

// finishForkTurn records, after the first turn that started an agent session, how the run's context was restored and
// updates the divider to say so. s.mu must be held.
func (s *session) finishForkTurn(t *turn, rec agent.RunRecord) {
	if !t.forkFirst || rec.SessionID == "" || s.row.ForkContext != "" {
		return
	}
	kind, reason := store.ForkReplayed, ""
	if rec.ResumedVia == agent.ResumedLoad {
		kind = store.ForkResumed
	} else {
		reason = rec.ResumeFallbackReason
	}
	s.row.ForkContext = kind
	msgs, err := s.m.st.ListAssistMessages(s.m.ctx, s.id)
	if err != nil {
		s.m.log.Error("assist: read fork divider", "session", s.id, "err", err)
		return
	}
	for _, m := range msgs {
		if m.Kind != store.MessageDivider {
			continue
		}
		text := dividerText(s.row.ForkedFromAgentRunID, kind, s.redact(reason))
		m.Text = &text
		if err := s.m.st.UpdateAssistMessage(s.m.ctx, &m); err != nil {
			s.m.log.Error("assist: update fork divider", "session", s.id, "err", err)
		}
		return
	}
}

// forkPrompt is what forkTurn needs to build the prompts of a turn of a forked chat.
type forkPrompt struct {
	head, restore, page, text string
	before                    []store.AssistMessage
	forkContext               string
}

// forkTurn sets the prompts of a turn of a forked chat. The first turn restores the context of the run: with the plan
// to resume, the prompt is the plain one and the fallback (used when session/load fails or is unavailable) carries the
// compacted transcript; otherwise the prompt itself carries it. Later turns use the usual recap of the chat, with a
// pointer to the run for what the recap leaves out.
func (s *session) forkTurn(ctx context.Context, t *turn, req *agent.TurnRequest, f core.ForkNode, plan forkPlan, p forkPrompt) {
	first := !hasAgentOutput(p.before)
	t.forkFirst = first && p.forkContext == ""
	user := "The user now says:\n\n" + p.text
	if !first {
		req.FallbackPrompt = p.head + p.restore + "This chat continues run " + f.AgentRunID + " (read it with agent_run_events when you need more than the recap).\n" +
			"The conversation so far:\n" + recap(p.before) + "\n" + p.page + user
		return
	}
	block, err := s.forkContextBlock(ctx, f)
	if err != nil {
		s.m.log.Warn("assist: transcript of the run a chat continues", "session", s.id, "run", f.AgentRunID, "err", err)
		block = "The transcript of run " + f.AgentRunID + " could not be read; use agent_run_events to read it.\n"
	}
	if plan.resume {
		req.FallbackPrompt = p.head + p.restore + block + "\n" + p.page + user
		return
	}
	req.Prompt = p.head + "This chat continues the run below; its transcript is your earlier context.\n\n" + block + "\n" + p.page + user
	req.FallbackPrompt = req.Prompt
}
