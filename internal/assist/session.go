package assist

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// draftPollInterval is how often the draft file is compared with the stored draft while a turn runs. Agents
// write the file when a tool call completes, which also triggers a check; polling covers agents that write
// without reporting it.
const draftPollInterval = 400 * time.Millisecond

// session is the live state of one assist session. The row in the store is the source of truth for
// everything the API serves; this struct adds the draft directory, the agent process and the running turn.
type session struct {
	m   *Manager
	id  string
	dir string

	mu        sync.Mutex // guards everything below, and serialises draft changes
	row       store.AssistSession
	ag        *agent.Session
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

// applyDraft sets the draft fields of the row from yaml: hash, parsed id, the validator's diagnostics and, for a
// flow, the prompt files in the draft directory.
func (s *session) applyDraft(yaml, by string) {
	row := &s.row
	row.DraftYAML = yaml
	row.DraftHash = spec.Hash([]byte(yaml))
	if s.isFlow() {
		row.DraftID, row.DraftDiagnostics, row.DraftPrompts = s.m.evaluateFlow(yaml, row.TargetID, s.dir)
	} else {
		row.DraftID, row.DraftDiagnostics = evaluate(yaml, row.TargetID)
	}
	row.DraftUpdatedAt, row.DraftUpdatedBy = s.m.st.Now(), by
}

// evaluate validates a draft like a sensor file. For a new sensor the id need not exist yet. When editing an
// existing sensor a changed id is flagged: saving would create a different sensor.
func evaluate(yaml, target string) (sensorID string, diagnostics json.RawMessage) {
	sensor, ds := spec.CheckSensor("", []byte(yaml))
	for i := range ds {
		ds[i].File = SensorDraftFile
	}
	if sensor != nil {
		if spec.ValidID(sensor.ID) {
			sensorID = sensor.ID
		}
		if target != "" && sensor.ID != target {
			ds = append(ds, spec.Diagnostic{
				File: SensorDraftFile, Path: "id", Severity: spec.SeverityWarning, Code: spec.CodeIDMismatch,
				Message: fmt.Sprintf("id %q differs from the sensor being edited (%q): saving would create a different sensor", sensor.ID, target),
			})
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

// initDir creates the draft directory with the draft, the instructions and, for a flow, its reference material.
func (s *session) initDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("assist: create draft directory: %w", err)
	}
	if s.isFlow() {
		if err := os.MkdirAll(filepath.Join(s.dir, PromptsDir), 0o700); err != nil {
			return fmt.Errorf("assist: create prompts directory: %w", err)
		}
	}
	if err := writeAtomic(s.draftPath(), []byte(s.row.DraftYAML)); err != nil {
		return err
	}
	return s.writeInstructions()
}

// writeInstructions (re)writes AGENTS.md and CLAUDE.md with the current env variable names, and for a flow
// refreshes the reference material (sensors, prompts, runtimes, samples).
func (s *session) writeInstructions() error {
	var text []byte
	if s.isFlow() {
		if err := s.m.writeReference(s.dir); err != nil {
			return err
		}
		global, _ := s.m.envNames("")
		text = []byte(flowInstructions(s.id, s.row.TargetID, global))
	} else {
		global, sensor := s.m.envNames(s.row.TargetID)
		text = []byte(instructions(s.id, s.row.TargetID, global, sensor))
	}
	for _, name := range []string{instructionFile, instructionFile2} {
		if err := writeAtomic(filepath.Join(s.dir, name), text); err != nil {
			return err
		}
	}
	return nil
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

// syncFromDiskLocked adopts the draft file when the agent changed it. A missing file is restored from the
// stored draft. s.mu must be held.
func (s *session) syncFromDiskLocked() error {
	b, err := os.ReadFile(s.draftPath())
	if os.IsNotExist(err) {
		return writeAtomic(s.draftPath(), []byte(s.row.DraftYAML))
	}
	if err != nil {
		return fmt.Errorf("assist: read the draft: %w", err)
	}
	if spec.Hash(b) == s.row.DraftHash && (!s.isFlow() || !s.m.promptsChanged(s.dir, s.row.DraftPrompts)) {
		return nil
	}
	if len(b) > maxDraftBytes {
		b = b[:maxDraftBytes] // an agent that wrote far more than a sensor: keep what fits
	}
	s.applyDraft(string(b), store.DraftByAgent)
	return s.saveLocked()
}

func (s *session) syncFromDisk() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if err := s.syncFromDiskLocked(); err != nil {
		s.m.log.Warn("assist: sync draft", "session", s.id, "err", err)
	}
}

// updateDraft is the editor's PUT: the agent's unsynced edits are adopted first, so the hash check sees them.
func (s *session) updateDraft(yaml, expected string) (*store.AssistSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	if err := s.syncFromDiskLocked(); err != nil {
		return nil, err
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
		id, diags, prompts := s.m.evaluateFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
		if id != s.row.DraftID || string(diags) != string(s.row.DraftDiagnostics) || string(prompts) != string(s.row.DraftPrompts) {
			s.row.DraftID, s.row.DraftDiagnostics, s.row.DraftPrompts = id, diags, prompts
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
	if s.isFlow() {
		s.row.DraftID, s.row.DraftDiagnostics, s.row.DraftPrompts = s.m.evaluateFlow(s.row.DraftYAML, id, s.dir)
	} else {
		s.row.DraftID, s.row.DraftDiagnostics = evaluate(s.row.DraftYAML, id)
	}
	if err := s.saveLocked(); err != nil {
		return err
	}
	return s.writeInstructions()
}

func (s *session) usableLocked() error {
	if s.closed || s.row.Status == store.AssistClosed {
		return fmt.Errorf("%w: the session is closed", core.ErrInvalidState)
	}
	return nil
}

// send stores the user's message and starts a turn.
func (s *session) send(text string, yaml *string) (*store.AssistSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	if s.turn != nil {
		return nil, fmt.Errorf("%w: a turn is already running in this session", core.ErrInvalidState)
	}
	if err := s.syncFromDiskLocked(); err != nil {
		return nil, err
	}
	if yaml != nil && *yaml != s.row.DraftYAML {
		if err := s.setDraftLocked(*yaml, store.DraftByUser); err != nil {
			return nil, err
		}
	}
	user := &store.AssistMessage{SessionID: s.id, Role: "user", Text: &text}
	if err := s.m.st.CreateAssistMessage(s.m.ctx, user); err != nil {
		return nil, err
	}

	hostCtx := s.m.host.Context()
	ctx, cancel := context.WithCancelCause(hostCtx)
	t := &turn{runID: s.m.st.NewID(), cancel: cancel, done: make(chan struct{}), tools: map[string]*toolMsg{}}
	untrack := s.m.host.Track("assist:"+t.runID, cancel)
	started := s.m.host.Spawn(func(context.Context) {
		defer untrack()
		defer cancel(nil)
		s.runTurn(ctx, t, text)
	})
	if !started {
		untrack()
		cancel(nil)
		return nil, fmt.Errorf("assist: the daemon is stopping")
	}
	s.turn = t
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
	ex := &agent.Executor{
		Recorder:      &sessionRecorder{s: s, inner: s.m.host.Recorder(s.id, s.row.Model)},
		Broker:        s.m.host.Broker(),
		Clock:         s.m.host.Clock(),
		Log:           s.m.log,
		Environ:       s.m.agentEnv(s.id),
		ClientVersion: "hal9001",
	}
	ag, err := ex.NewSession(agent.SessionRequest{
		Runtime: s.row.Runtime, Config: rt, Cwd: s.dir, Model: s.row.Model, LoadSessionID: s.row.ACPSessionID,
	})
	if err != nil {
		return nil, err
	}
	s.ag = ag
	return ag, nil
}

// runTurn drives one agent turn and records its outcome.
func (s *session) runTurn(ctx context.Context, t *turn, text string) {
	stopPoll := make(chan struct{})
	var polled sync.WaitGroup
	polled.Add(1)
	go func() {
		defer polled.Done()
		tick := time.NewTicker(draftPollInterval)
		defer tick.Stop()
		for {
			select {
			case <-stopPoll:
				return
			case <-tick.C:
				s.syncFromDisk()
			}
		}
	}()

	rec, perr := s.prompt(ctx, t, text)

	close(stopPoll)
	polled.Wait()
	s.finishTurn(t, rec, perr)
}

// prompt prepares the turn and runs it.
func (s *session) prompt(ctx context.Context, t *turn, text string) (agent.RunRecord, error) {
	s.mu.Lock()
	ag, err := s.agentSessionLocked()
	if err == nil {
		err = s.writeInstructions()
	}
	row := s.row
	s.mu.Unlock()
	if err != nil {
		return agent.RunRecord{}, err
	}
	msgs, err := s.m.st.ListAssistMessages(s.m.ctx, s.id)
	if err != nil {
		return agent.RunRecord{}, err
	}
	global, sensor := s.m.envNames(row.TargetID)
	head := preamble(row.TargetID, global, sensor)
	if row.TargetKind == store.AssistTargetFlow {
		global, _ = s.m.envNames("")
		head = flowPreamble(row.TargetID, global)
	}
	// The turn's own user message is the last one; the recap covers what came before it.
	var before []store.AssistMessage
	if n := len(msgs); n > 0 {
		before = msgs[:n-1]
	}
	return ag.Prompt(ctx, agent.TurnRequest{
		RunID:  t.runID,
		Prompt: head + text,
		FallbackPrompt: head + "Your previous agent session could not be restored, so this is a new one. " +
			"Read the current " + draftFileOf(row.TargetKind) + " first. The conversation so far:\n" + recap(before) + "\nThe user now says:\n\n" + text,
		Allow: s.allowRules(),
	})
}

// preamble prefixes every prompt: it names the draft and repeats what changes between turns.
func preamble(targetID string, global, sensor []string) string {
	var b strings.Builder
	b.WriteString("[hal9001 sensor assistant] The draft is `" + SensorDraftFile + "` in the working directory: edit only that file. The sensor format, the output and key rules and how to test are in AGENTS.md.\n")
	if targetID != "" {
		b.WriteString("You are editing the existing sensor `" + targetID + "`; keep its id.\n")
	} else {
		b.WriteString("You are creating a new sensor.\n")
	}
	b.WriteString("Env variable names the script can use: " + nameList(append(append([]string(nil), global...), sensor...)) + ".\n")
	b.WriteString("Run `hal9001 assist test` to test the draft; the first test after you change the script asks the user to approve it.\n\n")
	return b.String()
}

// finishTurn records the outcome of a turn and returns the session to idle or failed.
func (s *session) finishTurn(t *turn, rec agent.RunRecord, runErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncFromDiskLocked(); err != nil {
		s.m.log.Warn("assist: final draft sync", "session", s.id, "err", err)
	}
	s.flushTextLocked(t)
	s.pending = map[string]struct{}{}

	status, errText, note := outcome(rec, runErr, s.m.redactString)
	s.row.Status, s.row.Error = status, errText
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
	s.turn = nil
	s.m.mu.Lock()
	delete(s.m.byRun, t.runID)
	s.m.mu.Unlock()
	if !s.closed {
		_ = s.saveLocked()
		s.armIdleLocked()
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
	if id.empty() {
		return true, "" // nothing runs with credentials
	}
	h := id.hash()
	if s.row.ApprovedScriptHash != "" && s.row.ApprovedScriptHash == h {
		return true, s.row.ApprovedScript
	}
	base = s.row.ApprovedScript
	if saved, ok := s.m.savedScript(s.row.TargetKind, s.row.TargetID, available); ok {
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
		flow, ds := spec.ParseFlow(raw)
		if flow == nil || spec.HasErrors(ds) {
			return scriptID{}, false
		}
		return scriptOfFlow(flow, available), true
	}
	raw, _, err := spec.ReadFile(m.opt.Home, spec.KindSensor, target)
	if err != nil {
		return scriptID{}, false
	}
	sensor, ds := spec.ParseSensor(raw)
	if sensor == nil || spec.HasErrors(ds) {
		return scriptID{}, false
	}
	return scriptOf(sensor, available), true
}

// draftScriptLocked returns the identity of what a test of the current draft runs with credentials, and the
// credential names available to it. ok is false when the draft does not validate. s.mu must be held.
func (s *session) draftScriptLocked() (id scriptID, available []string, ok bool) {
	if s.isFlow() {
		flow, _, ds, _ := s.m.checkFlow(s.row.DraftYAML, s.row.TargetID, s.dir)
		if flow == nil || spec.HasErrors(ds) {
			return scriptID{}, nil, false
		}
		available = s.m.availableEnv("")
		return scriptOfFlow(flow, available), available, true
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

// vetoTest is the CommandVeto of `hal9001 assist test`: the test executes the draft's script with the user's
// credentials, so it is allowed without asking only while that script is one the user has tested or approved. The
// draft file is read now, so an edit the agent just made counts. A changed script is asked about: the veto's reason
// goes to the permission request and the script and its diff to the conversation.
func (s *session) vetoTest(req agent.ToolRequest, rule int) string {
	if rule != 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncFromDiskLocked(); err != nil {
		return "the draft could not be read, so its script cannot be compared with the approved one"
	}
	id, available, ok := s.draftScriptLocked()
	if !ok {
		return "" // a draft that does not validate is not run: the test answers 422
	}
	approved, base := s.scriptApprovedLocked(id, available)
	if approved {
		return ""
	}
	s.pendingTests[req.ToolCallID] = &pendingTest{id: id, base: base}
	return "the draft's code changed since the user last tested or approved it, and a test runs it with real credentials"
}

// approvalPending records a gated tool call: the conversation shows it with its approval, and the session waits.
func (s *session) approvalPending(runID, toolCallID, approvalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turn
	if t == nil || t.runID != runID {
		return
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
		s.mu.Unlock()
		s.ag.Close()
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
	ag := s.ag
	s.mu.Unlock()
	if ag != nil {
		ag.Close()
	}
}
