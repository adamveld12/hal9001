package assist

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// session is the live state of one assist session. The row in the store is the source of truth for
// everything the API serves; this struct adds the draft directory, the agent process and the running turn.
type session struct {
	m   *Manager
	id  string
	dir string

	mu  sync.Mutex // guards everything below, and serialises draft changes
	row store.AssistSession
	ag  *agent.Session
	// halTools is the hal MCP session of the agent process (token valid while ag lives); runID is the running turn's
	// agent run id, "" between turns.
	halTools *tools.Session
	// fork is the node of the run a forked session continues (see fork.go); nil until resolved, and for a session that
	// is not a fork.
	fork *core.ForkNode
	// toolTest is the draft_test call that was just queued for approval, until approvalPending links it to its approval.
	toolTest  *pendingTest
	runID     atomic.Pointer[string]
	turn      *turn
	idle      *time.Timer
	approvals map[string]string   // tool call id -> approval id, for the running turn
	pending   map[string]struct{} // approvals waiting for a decision
	// Tests of a changed script the agent asked for, by tool call id; and by approval id once the approval exists.
	pendingTests  map[string]*pendingTest
	testApprovals map[string]*pendingTest
	closed        bool
}

// turn is one agent turn in flight.
type turn struct {
	runID  string
	cancel context.CancelCauseFunc
	done   chan struct{}
	// linked is set once the session's last_agent_run_id names this turn's run.
	linked bool
	// forkFirst marks the turn that restores the context of a forked session's run (see finishForkTurn).
	forkFirst bool

	// Conversation state, guarded by session.mu.
	text        *store.AssistMessage
	textBuf     strings.Builder
	textDirty   bool
	textCreated bool
	textTimer   *time.Timer
	tools       map[string]*toolMsg
}

func newSession(m *Manager, row store.AssistSession) *session {
	return &session{
		m: m, id: row.ID, dir: filepath.Join(m.dir, row.ID), row: row,
		approvals: map[string]string{}, pending: map[string]struct{}{},
		pendingTests: map[string]*pendingTest{}, testApprovals: map[string]*pendingTest{},
	}
}

// draftFile is the draft's file name in the draft directory.
func (s *session) draftFile() string { return draftFileOf(s.row.TargetKind) }

func (s *session) draftPath() string { return filepath.Join(s.dir, s.draftFile()) }

func (s *session) isFlow() bool { return s.row.TargetKind == store.AssistTargetFlow }

// isGeneral reports whether the session is a general chat: no draft, no draft file, no draft tools.
func (s *session) isGeneral() bool { return s.row.TargetKind == store.AssistTargetGeneral }

// errNoDraft is what a draft operation on a general session answers.
var errNoDraft = fmt.Errorf("%w: a general session has no draft (start a sensor, flow or tool session to draft)", core.ErrInvalidState)

// applyDraft sets the draft fields of the row from yaml: hash, parsed id, the validator's diagnostics and, for a
// flow, the prompt files in the draft directory.
func (s *session) applyDraft(yaml, by string) {
	row := &s.row
	row.DraftYAML = yaml
	row.DraftHash = spec.Hash([]byte(yaml))
	switch {
	case s.isGeneral():
		row.DraftID, row.DraftDiagnostics = "", marshalDiagnostics(nil)
	case s.isFlow():
		row.DraftID, row.DraftDiagnostics, row.DraftPrompts, row.DraftSensors = s.m.evaluateFlow(yaml, row.TargetID, s.dir)
	case s.isTool():
		row.DraftID, row.DraftDiagnostics = evaluateTool(yaml, row.TargetID)
	default:
		row.DraftID, row.DraftDiagnostics = evaluate(yaml, row.TargetID)
	}
	row.DraftUpdatedAt, row.DraftUpdatedBy = s.m.st.Now(), by
}

// evaluate validates a draft like a sensor file. A new sensor is named by the draft's id: line (the file name it
// is saved under, never stored in the file). When editing an existing sensor the name is the target's and an id:
// line that differs is ignored with a warning.
func evaluate(yaml, target string) (sensorID string, diagnostics json.RawMessage) {
	sensor, ds := spec.CheckSensor(target, []byte(yaml))
	for i := range ds {
		ds[i].File = SensorDraftFile
	}
	if sensor != nil {
		if spec.ValidID(sensor.ID) {
			sensorID = sensor.ID
		}
	}
	return sensorID, marshalDiagnostics(ds)
}

func marshalDiagnostics(ds []spec.Diagnostic) json.RawMessage {
	if ds == nil {
		ds = []spec.Diagnostic{}
	}
	b, err := json.Marshal(ds)
	if err != nil {
		b = []byte("[]")
	}
	return b
}

// initDir creates the draft directory with the draft, the instructions and, for a flow, its reference material. A
// general session has the directory (the agent's working directory) and nothing in it.
func (s *session) initDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("assist: create draft directory: %w", err)
	}
	if s.isGeneral() {
		return nil
	}
	if s.isFlow() {
		for _, d := range []string{PromptsDir, SensorsDir} {
			if err := os.MkdirAll(filepath.Join(s.dir, d), 0o700); err != nil {
				return fmt.Errorf("assist: create %s directory: %w", d, err)
			}
		}
	}
	if s.isFlow() {
		if err := s.m.writeReference(s.dir); err != nil {
			return err
		}
	} else if err := s.m.writeConfigReference(s.dir); err != nil {
		return err
	}
	return writeAtomic(s.draftPath(), []byte(s.row.DraftYAML))
}

// writeAtomic replaces path in one step, so a reader never sees half a file.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("assist: write %s: %w", filepath.Base(path), err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("assist: write %s: %w", filepath.Base(path), werr)
	}
	return nil
}

func (s *session) saveLocked() error {
	if err := s.m.st.UpdateAssistSession(s.m.ctx, &s.row); err != nil {
		s.m.log.Error("assist: store session", "session", s.id, "err", err)
		return err
	}
	return nil
}

func (s *session) copyRowLocked() *store.AssistSession {
	out := s.row
	return &out
}

// setDraftLocked makes yaml the draft: the file the agent sees and the stored copy. s.mu must be held.
func (s *session) setDraftLocked(yaml, by string) error {
	if err := writeAtomic(s.draftPath(), []byte(yaml)); err != nil {
		return err
	}
	s.applyDraft(yaml, by)
	return s.saveLocked()
}

// updateDraft is the editor's PUT: the agent's unsynced edits are adopted first, so the hash check sees them.
func (s *session) updateDraft(yaml, expected string) (*store.AssistSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	if s.isGeneral() {
		return nil, errNoDraft
	}
	if expected != "" && expected != s.row.DraftHash {
		return nil, &core.DraftConflictError{Expected: expected, Current: s.row.DraftHash}
	}
	if yaml != s.row.DraftYAML {
		if err := s.setDraftLocked(yaml, store.DraftByUser); err != nil {
			return nil, err
		}
	} else if s.isFlow() {
		// An unchanged push still refreshes what depends on the workspace: a prompt saved since, a new sensor.
		id, diags, prompts, sensors := s.m.evaluateFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
		if id != s.row.DraftID || string(diags) != string(s.row.DraftDiagnostics) || string(prompts) != string(s.row.DraftPrompts) ||
			string(sensors) != string(s.row.DraftSensors) {
			s.row.DraftID, s.row.DraftDiagnostics, s.row.DraftPrompts, s.row.DraftSensors = id, diags, prompts, sensors
			if err := s.saveLocked(); err != nil {
				return nil, err
			}
		}
	}
	return s.copyRowLocked(), nil
}

// adopt makes the session edit the existing sensor or flow id instead of a new one. The draft is validated again
// (an id that differs from the target is now flagged) and the instructions list the target's env keys.
func (s *session) adopt(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.row.Status == store.AssistClosed || s.row.TargetID != "" || s.row.DraftID != id {
		return nil
	}
	s.row.TargetID = id
	switch {
	case s.isFlow():
		s.row.DraftID, s.row.DraftDiagnostics, s.row.DraftPrompts, s.row.DraftSensors = s.m.evaluateFlow(s.row.DraftYAML, id, s.dir)
	case s.isTool():
		s.row.DraftID, s.row.DraftDiagnostics = evaluateTool(s.row.DraftYAML, id)
	default:
		s.row.DraftID, s.row.DraftDiagnostics = evaluate(s.row.DraftYAML, id)
	}
	return s.saveLocked()
}

// retarget makes the session edit newID instead of oldID after a rename. The draft is re-evaluated against the
// new name.
func (s *session) retarget(oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.row.Status == store.AssistClosed || s.row.TargetID != oldID {
		return nil
	}
	s.row.TargetID = newID
	switch {
	case s.isFlow():
		s.row.DraftID, s.row.DraftDiagnostics, s.row.DraftPrompts, s.row.DraftSensors = s.m.evaluateFlow(s.row.DraftYAML, newID, s.dir)
	case s.isTool():
		s.row.DraftID, s.row.DraftDiagnostics = evaluateTool(s.row.DraftYAML, newID)
	default:
		s.row.DraftID, s.row.DraftDiagnostics = evaluate(s.row.DraftYAML, newID)
	}
	return s.saveLocked()
}

func (s *session) usableLocked() error {
	if s.closed || s.row.Status == store.AssistClosed {
		return fmt.Errorf("%w: the session is closed", core.ErrInvalidState)
	}
	return nil
}

// send stores the user's message and starts a turn; page is rendered into this turn's prompt only.
func (s *session) send(text string, yaml *string, page *core.AssistPageContext) (*store.AssistSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	if s.turn != nil {
		return nil, fmt.Errorf("%w: a turn is already running in this session", core.ErrInvalidState)
	}
	if yaml != nil && !s.isGeneral() && *yaml != s.row.DraftYAML {
		if err := s.setDraftLocked(*yaml, store.DraftByUser); err != nil {
			return nil, err
		}
	}
	user := &store.AssistMessage{SessionID: s.id, Role: "user", Text: &text}
	if err := s.m.st.CreateAssistMessage(s.m.ctx, user); err != nil {
		return nil, err
	}
	if s.row.Title == "" {
		s.row.Title = store.AssistTitle(text)
	}

	hostCtx := s.m.host.Context()
	ctx, cancel := context.WithCancelCause(hostCtx)
	t := &turn{runID: s.m.st.NewID(), cancel: cancel, done: make(chan struct{}), tools: map[string]*toolMsg{}}
	untrack := s.m.host.Track("assist:"+t.runID, cancel)
	started := s.m.host.Spawn(func(context.Context) {
		defer untrack()
		defer cancel(nil)
		s.runTurn(ctx, t, text, page)
	})
	if !started {
		untrack()
		cancel(nil)
		return nil, fmt.Errorf("assist: the daemon is stopping")
	}
	s.turn = t
	s.runID.Store(&t.runID)
	s.stopIdleLocked()
	s.approvals, s.pending = map[string]string{}, map[string]struct{}{}
	s.pendingTests, s.testApprovals = map[string]*pendingTest{}, map[string]*pendingTest{}
	s.m.mu.Lock()
	s.m.byRun[t.runID] = s
	s.m.mu.Unlock()
	s.row.Status, s.row.Error = store.AssistRunning, ""
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s.copyRowLocked(), nil
}

func (s *session) stopIdleLocked() {
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

// agentSessionLocked returns the ACP session, creating it on first use. s.mu must be held.
func (s *session) agentSessionLocked() (*agent.Session, error) {
	if s.ag != nil {
		return s.ag, nil
	}
	rt, ok := s.m.host.Runtime(s.row.Runtime)
	if !ok {
		return nil, fmt.Errorf("runtime %q is no longer configured in config.yaml", s.row.Runtime)
	}
	rs := tools.RunSpec{
		Kind: tools.KindAssist, RunID: s.id,
		Info: func() tools.RunInfo {
			info := tools.RunInfo{AssistSessionID: s.id}
			if id := s.runID.Load(); id != nil {
				info.AgentRunID = *id
			}
			return info
		},
	}
	cwd, loadID := s.dir, s.row.ACPSessionID
	if s.forked() {
		f, err := s.forkNodeLocked()
		if err != nil {
			return nil, fmt.Errorf("resolve the run this chat continues: %w", err)
		}
		if f != nil {
			plan := s.planForkLocked(*f)
			cwd, loadID = plan.cwd, plan.loadID
			s.forkRunSpecLocked(&rs, *f)
		}
	}
	ht, err := s.m.host.Tools().Open(s.m.host.Context(), rs)
	if err != nil {
		return nil, fmt.Errorf("hal tools: %w", err)
	}
	ex := &agent.Executor{
		Recorder:      &sessionRecorder{s: s, inner: s.m.host.Recorder(s.id, s.row.Model)},
		Broker:        s.m.host.Broker(),
		Clock:         s.m.host.Clock(),
		Log:           s.m.log,
		Environ:       s.m.agentEnv(),
		ClientVersion: "hal9001",
	}
	ag, err := ex.NewSession(agent.SessionRequest{
		Runtime: s.row.Runtime, Config: rt, Cwd: cwd, Model: s.row.Model, LoadSessionID: loadID,
		ToolsReady: tools.ToolsReady(ht),
		MCPServers: []agent.MCPServer{{
			Name: tools.ServerName, Type: "stdio", Command: s.m.opt.Executable, Args: tools.ServerArgs(s.m.opt.URL, ht.Token()),
		}},
	})
	if err != nil {
		ht.Close()
		return nil, err
	}
	s.ag, s.halTools = ag, ht
	return ag, nil
}

// runTurn drives one agent turn and records its outcome.
func (s *session) runTurn(ctx context.Context, t *turn, text string, page *core.AssistPageContext) {
	rec, perr := s.prompt(ctx, t, text, page)
	s.finishTurn(t, rec, perr)
}

// prompt prepares the turn and runs it. The prompt is hal's instructions (the context block and the kind's preamble),
// then the page the user is looking at when the message carried one, then the user's text.
func (s *session) prompt(ctx context.Context, t *turn, text string, page *core.AssistPageContext) (agent.RunRecord, error) {
	s.mu.Lock()
	ag, err := s.agentSessionLocked()
	if err == nil {
		// Samples, saved prompts and the app's configuration change between turns.
		switch {
		case s.isGeneral():
		case s.isFlow():
			err = s.m.writeReference(s.dir)
		default:
			err = s.m.writeConfigReference(s.dir)
		}
	}
	row, ht, fork := s.row, s.halTools, s.fork
	var plan forkPlan
	if err == nil && fork != nil {
		plan = s.planForkLocked(*fork)
	}
	s.mu.Unlock()
	if err != nil {
		return agent.RunRecord{}, err
	}
	// The tools make sure what they need exists (a conversation's sandbox is recreated when it was removed) and report
	// what the user should know; the notices land in the conversation before the agent's answer.
	notices, err := ht.BeforeTurn(ctx)
	s.postNotices(notices)
	if err != nil {
		return agent.RunRecord{}, fmt.Errorf("prepare the turn: %w", err)
	}
	msgs, err := s.m.st.ListAssistMessages(s.m.ctx, s.id)
	if err != nil {
		return agent.RunRecord{}, err
	}
	var head, restore string
	switch row.TargetKind {
	case store.AssistTargetGeneral:
		head = generalPreamble()
		if fork != nil {
			head += forkPreamble(*fork)
		}
		restore = "Your previous agent session could not be restored, so this is a new one. "
	case store.AssistTargetFlow:
		global, _ := s.m.envNames("")
		head = flowPreamble(row.TargetID, global, s.m.configSummary())
	case store.AssistTargetTool:
		global, own := s.m.envNamesOf(row.TargetKind, row.TargetID)
		head = toolPreamble(row.TargetID, global, own, s.m.configSummary())
	default:
		global, sensor := s.m.envNames(row.TargetID)
		head = preamble(row.TargetID, global, sensor, s.m.configSummary())
	}
	if restore == "" {
		restore = "Your previous agent session could not be restored, so this is a new one. Read the current " + draftFileOf(row.TargetKind) + " first. "
	}
	head = tools.Preamble(tools.PreambleData{
		Info: tools.RunInfo{AgentRunID: t.runID, AssistSessionID: s.id}, Now: s.m.host.Clock().Now(), Tools: ht.Tools(), SystemPrompt: spec.Load(s.m.home).Config.Value.SystemPrompt,
	}) + head
	// The turn's own user message is the last one; the recap covers what came before it.
	var before []store.AssistMessage
	if n := len(msgs); n > 0 {
		before = msgs[:n-1]
	}
	pageText := renderPageContext(page)
	req := agent.TurnRequest{
		RunID:          t.runID,
		Prompt:         head + pageText + text,
		FallbackPrompt: head + restore + "The conversation so far:\n" + recap(before) + "\n" + pageText + "The user now says:\n\n" + text,
	}
	if fork != nil {
		s.forkTurn(ctx, t, &req, *fork, plan, forkPrompt{head: head, restore: restore, page: pageText, text: text, before: before, forkContext: row.ForkContext})
	}
	return ag.Prompt(ctx, req)
}

// preamble prefixes every prompt: it names the draft and repeats what changes between turns.
func preamble(targetID string, global, sensor []string, summary string) string {
	var b strings.Builder
	b.WriteString("[hal9001 sensor assistant] You have no file or shell access: you read and change the draft with the tools draft_read, draft_write, draft_test and validate. " +
		"Call draft_read with path `instructions` first: it holds the sensor format, the output and key rules and how to test.\n")
	if targetID != "" {
		b.WriteString("You are editing the existing sensor `" + targetID + "`; keep its id.\n")
	} else {
		b.WriteString("You are creating a new sensor.\n")
	}
	b.WriteString("Env variable names the script can use: " + nameList(append(append([]string(nil), global...), sensor...)) + ".\n")
	b.WriteString("Test the draft with draft_test; the first test after you change the script asks the user to approve it.\n")
	b.WriteString(uiToolsLine)
	b.WriteString(summary + "\n")
	return b.String()
}

// finishTurn records the outcome of a turn and returns the session to idle or failed.
func (s *session) finishTurn(t *turn, rec agent.RunRecord, runErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushTextLocked(t)
	s.pending = map[string]struct{}{}

	status, errText, note := outcome(rec, runErr, s.m.redactString)
	s.row.Status, s.row.Error = status, errText
	s.finishForkTurn(t, rec)
	if rec.ID != "" {
		s.row.LastAgentRunID = rec.ID
	}
	if s.ag != nil {
		if id := s.ag.ACPSessionID(); id != "" {
			s.row.ACPSessionID = id
		}
	}
	if note != "" {
		msg := &store.AssistMessage{SessionID: s.id, Role: "system", AgentRunID: t.runID, Text: &note}
		if err := s.m.st.CreateAssistMessage(s.m.ctx, msg); err != nil {
			s.m.log.Error("assist: store system message", "session", s.id, "err", err)
		}
	}
	if s.halTools != nil {
		s.halTools.AfterTurn()
	}
	s.turn = nil
	s.runID.Store(nil)
	s.m.mu.Lock()
	delete(s.m.byRun, t.runID)
	s.m.mu.Unlock()
	if !s.closed {
		_ = s.saveLocked()
		s.armIdleLocked()
	}
	if obs := s.m.turnObserver(); obs != nil {
		result := rec.FinalMessage
		if result == "" {
			result = note
		}
		obs.TurnEnded(s.id, status == store.AssistFailed, errText, result)
	}
	close(t.done)
}

// outcome maps a finished turn to the session status, its error text and a note for the conversation.
func outcome(rec agent.RunRecord, runErr error, redact func(string) string) (status store.AssistStatus, errText, note string) {
	if runErr != nil {
		text := redact(runErr.Error())
		return store.AssistFailed, text, "The turn could not run: " + text
	}
	detail := redact(strings.TrimSpace(rec.Error))
	switch rec.Status {
	case agent.StatusSucceeded:
		return store.AssistIdle, "", ""
	case agent.StatusKilled:
		if rec.FailureReason == agent.ReasonCancelled {
			return store.AssistIdle, "", "The turn was cancelled."
		}
		return store.AssistFailed, "stopped by the kill switch", "The turn was stopped by the kill switch."
	case agent.StatusDenied:
		// The user said no (and, for agents that cannot continue after a rejection, the turn ended).
		return store.AssistIdle, "", "The turn ended because a tool call was denied. Send another message to continue."
	case agent.StatusInterrupted:
		return store.AssistFailed, "the daemon stopped during the turn", "The daemon stopped during the turn. Send another message to continue."
	case agent.StatusTimeout:
		return store.AssistFailed, "the turn timed out", "The turn timed out."
	default:
		reason := rec.FailureReason
		if reason == "" {
			reason = string(rec.Status)
		}
		text := reason
		if detail != "" {
			text += ": " + detail
		}
		return store.AssistFailed, text, "The turn failed: " + text
	}
}

func (m *Manager) redactString(v string) string {
	_, _, red := m.host.Env("")
	return red.String(v)
}

func (s *session) redact(v string) string { return s.m.redactString(v) }

// armIdleLocked suspends the agent process after the idle timeout. s.mu must be held.
func (s *session) armIdleLocked() {
	s.stopIdleLocked()
	if s.ag == nil {
		return
	}
	ag := s.ag
	s.idle = time.AfterFunc(s.m.opt.IdleTimeout, func() { ag.Suspend() })
}

// onRunUpdated links the session to the turn's agent run once the run exists.
func (s *session) onRunUpdated(rec agent.RunRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turn
	if t == nil || t.runID != rec.ID {
		return
	}
	// The agent's own session id survives a daemon restart that interrupts this turn.
	changed := rec.SessionID != "" && rec.SessionID != s.row.ACPSessionID
	if changed {
		s.row.ACPSessionID = rec.SessionID
	}
	if !t.linked {
		t.linked, changed = true, true
		s.row.LastAgentRunID = rec.ID
	}
	if changed {
		_ = s.saveLocked()
	}
}

// scriptApprovedLocked reports whether id is a script the user has tested or approved in this session, or the saved
// script of the sensor being edited (which already runs with these credentials). It also returns the text a changed
// script is shown against. s.mu must be held.
func (s *session) scriptApprovedLocked(id scriptID, available []string) (approved bool, base string) {
	saved, ok := s.m.savedScript(s.row.TargetKind, s.row.TargetID, available)
	return s.approvedAgainstLocked(id, saved, ok)
}

// approvedAgainstLocked is scriptApprovedLocked with the saved script given (hasSaved false: there is none).
func (s *session) approvedAgainstLocked(id, saved scriptID, hasSaved bool) (approved bool, base string) {
	if id.empty() {
		return true, "" // nothing runs with credentials
	}
	h := id.hash()
	if s.row.ApprovedScriptHash != "" && s.row.ApprovedScriptHash == h {
		return true, s.row.ApprovedScript
	}
	base = s.row.ApprovedScript
	if hasSaved {
		if saved.hash() == h {
			return true, saved.text()
		}
		if base == "" {
			base = saved.text()
		}
	}
	return false, base
}

// savedScript is the script of the saved sensor or flow file, when the session edits an existing one.
func (m *Manager) savedScript(kind, target string, available []string) (scriptID, bool) {
	if target == "" {
		return scriptID{}, false
	}
	if kind == store.AssistTargetFlow {
		raw, _, err := spec.ReadFile(m.opt.Home, spec.KindFlow, target)
		if err != nil {
			return scriptID{}, false
		}
		flow, ds := spec.ParseFlow(target, raw)
		if flow == nil || spec.HasErrors(ds) {
			return scriptID{}, false
		}
		return scriptOfFlow(flow, available), true
	}
	if kind == store.AssistTargetTool {
		raw, _, err := spec.ReadFile(m.opt.Home, spec.KindTool, target)
		if err != nil {
			return scriptID{}, false
		}
		tool, ds := spec.ParseTool(target, raw)
		if tool == nil || spec.HasErrors(ds) {
			return scriptID{}, false
		}
		return scriptOfTool(tool, available), true
	}
	raw, _, err := spec.ReadFile(m.opt.Home, spec.KindSensor, target)
	if err != nil {
		return scriptID{}, false
	}
	sensor, ds := spec.ParseSensor(target, raw)
	if sensor == nil || spec.HasErrors(ds) {
		return scriptID{}, false
	}
	return scriptOf(sensor, available), true
}

// draftScriptLocked returns the identity of what a test of the current draft runs with credentials, and the
// credential names available to it. ok is false when the draft does not validate. s.mu must be held.
func (s *session) draftScriptLocked() (id scriptID, available []string, ok bool) {
	if s.isFlow() {
		flow, _, ds, _, _ := s.m.checkFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
		if flow == nil || spec.HasErrors(ds) {
			return scriptID{}, nil, false
		}
		available = s.m.availableEnv("")
		return scriptOfFlow(flow, available), available, true
	}
	if s.isTool() {
		tool, ds := spec.CheckTool("", []byte(s.row.DraftYAML))
		if tool == nil || spec.HasErrors(ds) {
			return scriptID{}, nil, false
		}
		available = s.m.availableEnvOf(s.row.TargetKind, s.row.TargetID)
		return scriptOfTool(tool, available), available, true
	}
	sensor, ds := spec.CheckSensor("", []byte(s.row.DraftYAML))
	if sensor == nil || spec.HasErrors(ds) {
		return scriptID{}, nil, false
	}
	available = s.m.availableEnv(s.row.TargetID)
	return scriptOf(sensor, available), available, true
}

// markApprovedLocked records id as the script the user last tested or approved. s.mu must be held.
func (s *session) markApprovedLocked(id scriptID) {
	s.row.ApprovedScript, s.row.ApprovedScriptHash = id.text(), id.hash()
	_ = s.saveLocked()
}

// approvalPending records a gated tool call: the conversation shows it with its approval, and the session waits.
func (s *session) approvalPending(runID, toolCallID, approvalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turn
	if t == nil || t.runID != runID {
		return
	}
	if tool, ok := halToolOf(toolCallID); ok {
		// A hal tool call: the agent's own tool call has another id, so find it by tool name.
		if tm := s.unlinkedToolMsg(t, tool); tm != nil {
			toolCallID = tm.call.ToolCallID
		}
		if tool == "draft_test" && s.toolTest != nil {
			s.pendingTests[toolCallID] = s.toolTest
			s.toolTest = nil
		}
	}
	if pt := s.pendingTests[toolCallID]; pt != nil {
		s.testApprovals[approvalID] = pt
	}
	s.linkApproval(t, toolCallID, approvalID)
	s.pending[approvalID] = struct{}{}
	if s.row.Status == store.AssistRunning {
		s.row.Status = store.AssistAwaitingApproval
		_ = s.saveLocked()
	}
	if obs := s.m.turnObserver(); obs != nil {
		obs.TurnWaiting(s.id, approvalID)
	}
}

// approvalResolved returns the session to running once no approval is pending.
func (s *session) approvalResolved(approvalID string, allowed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pt := s.testApprovals[approvalID]; pt != nil {
		delete(s.testApprovals, approvalID)
		if allowed {
			s.markApprovedLocked(pt.id) // before the agent's command runs: the daemon re-checks it
		}
	}
	delete(s.pending, approvalID)
	if len(s.pending) == 0 && s.turn != nil && s.row.Status == store.AssistAwaitingApproval {
		s.row.Status = store.AssistRunning
		_ = s.saveLocked()
		if obs := s.m.turnObserver(); obs != nil {
			obs.TurnResumed(s.id)
		}
	}
}

// cancel cancels the running turn and waits for it to wind down.
func (s *session) cancel(ctx context.Context) (*store.AssistSession, error) {
	s.mu.Lock()
	if err := s.usableLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		t.cancel(agent.ErrCancelled)
		select {
		case <-t.done:
		case <-time.After(cancelWait):
		case <-ctx.Done():
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyRowLocked(), nil
}

// close ends the session: the running turn is cancelled, the agent stopped, the draft directory removed.
func (s *session) close(ctx context.Context) error {
	s.mu.Lock()
	if s.closed || s.row.Status == store.AssistClosed {
		s.mu.Unlock()
		return nil
	}
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		t.cancel(agent.ErrCancelled)
		select {
		case <-t.done:
		case <-time.After(cancelWait):
		case <-ctx.Done():
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.stopIdleLocked()
	if s.ag != nil {
		ag, ht := s.ag, s.halTools
		s.mu.Unlock()
		ag.Close()
		ht.Close()
		s.mu.Lock()
	}
	if err := os.RemoveAll(s.dir); err != nil {
		s.m.log.Warn("assist: remove draft directory", "session", s.id, "err", err)
	}
	now := s.m.st.Now()
	s.row.Status, s.row.ClosedAt = store.AssistClosed, &now
	return s.saveLocked()
}

// shutdown stops the agent process without touching the session's state (daemon stopping).
func (s *session) shutdown() {
	s.mu.Lock()
	s.stopIdleLocked()
	ag, ht := s.ag, s.halTools
	s.mu.Unlock()
	if ag != nil {
		ag.Close()
		ht.Close()
	}
}

// halToolOf parses the tool call id hal gives its own tool calls ("hal:<tool>:<id>").
func halToolOf(toolCallID string) (string, bool) {
	rest, ok := strings.CutPrefix(toolCallID, "hal:")
	if !ok {
		return "", false
	}
	tool, _, _ := strings.Cut(rest, ":")
	return tool, tool != ""
}

// unlinkedToolMsg is the newest unfinished tool message of the turn for hal tool `tool` that has no approval yet
// (runtimes title an MCP tool call mcp__hal__<tool> or similar). s.mu must be held.
func (s *session) unlinkedToolMsg(t *turn, tool string) *toolMsg {
	var found *toolMsg
	for _, tm := range t.tools {
		if tm.msg.ApprovalID != "" || tm.call.Status == "completed" || tm.call.Status == "failed" || !strings.HasSuffix(tm.call.Title, tool) {
			continue
		}
		if found == nil || tm.msg.CreatedAt.After(found.msg.CreatedAt) {
			found = tm
		}
	}
	return found
}
