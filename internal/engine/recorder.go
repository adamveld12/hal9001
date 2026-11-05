package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/runner"
	"hal9001/internal/store"
)

// maxRawBytes caps the raw JSON-RPC kept in memory per agent run.
const maxRawBytes = 64 << 20

// runRecorder persists everything an agent run produces (agent.Recorder):
// the agent_runs row, the transcript, permission requests and the raw
// JSON-RPC traffic, with env-file secrets redacted.
type runRecorder struct {
	e   *Engine
	red *runner.Redactor
	// redFn, when set, supplies the redactor of the moment instead of red: an assist session outlives
	// many env changes, a node run does not.
	redFn    func() *runner.Redactor
	model    string
	worktree string
	// assist, when set, is the assist session the run is a turn of (the run has no node run).
	assist string

	mu    sync.Mutex
	perms map[string]bool // permission request ids already created
	raw   bytes.Buffer
}

var _ agent.Recorder = (*runRecorder)(nil)

func (r *runRecorder) redactor() *runner.Redactor {
	if r.redFn != nil {
		return r.redFn()
	}
	return r.red
}

// RunUpdated upserts the agent_runs row. Fields the record does not carry
// (the stored result, raw traffic) are kept from the existing row.
func (r *runRecorder) RunUpdated(rec agent.RunRecord) {
	ctx := r.e.dbCtx
	a, err := r.e.st.GetAgentRun(ctx, rec.ID)
	create := errors.Is(err, store.ErrNotFound)
	switch {
	case create:
		a = &store.AgentRun{ID: rec.ID, NodeRunID: rec.NodeRunID, AssistSessionID: r.assist, PrevAgentRunID: rec.ParentRunID, Runtime: rec.Runtime, Model: r.model}
	case err != nil:
		r.e.log.Error("engine: load agent run", "agent_run", rec.ID, "err", err)
		return
	}
	a.Mode, a.ApproveStyle = store.AgentMode(rec.Mode), store.ApproveStyle(rec.Approve)
	a.Policy, a.Phase = store.Policy(rec.Policy), store.AgentPhase(rec.Phase)
	a.Cwd, a.WorktreePath = rec.Cwd, r.worktree
	if rec.SessionID != "" {
		a.SessionID = rec.SessionID
	}
	if rec.ResumedVia != "" {
		a.ResumedVia = store.ResumedVia(rec.ResumedVia)
	}
	if rec.Prompt != "" {
		a.Prompt = store.Inline(r.redactor().String(rec.Prompt))
	}
	if len(rec.Argv) > 0 {
		a.Argv = r.redactor().Argv(rec.Argv)
	}
	if rec.AgentInfo != nil {
		a.AgentInfo, _ = json.Marshal(rec.AgentInfo)
	}
	if len(rec.Capabilities) > 0 {
		a.Capabilities = rec.Capabilities
	}
	a.Status, a.StopReason = store.AgentStatus(rec.Status), rec.StopReason
	a.Error = r.redactor().String(failureText(rec))
	if create {
		started := rec.StartedAt
		a.StartedAt = &started
	}
	a.EndedAt = rec.EndedAt
	if rec.FinalMessage != "" || rec.Note != nil || rec.NoteParseError != "" || rec.ResumeFallbackReason != "" {
		a.Result = store.Inline(string(r.resultJSON(rec)))
	}
	if rec.Status != agent.StatusRunning {
		r.mu.Lock()
		if r.raw.Len() > 0 {
			a.RawJSONRPC = store.Inline(r.raw.String())
		}
		r.mu.Unlock()
	}
	if create {
		err = r.e.st.CreateAgentRun(ctx, a)
	} else {
		err = r.e.st.UpdateAgentRun(ctx, a)
	}
	if err != nil {
		r.e.log.Error("engine: record agent run", "agent_run", rec.ID, "err", err)
	}
}

// failureText is "<reason>: <message>", the form stored in agent_runs.error.
func failureText(rec agent.RunRecord) string {
	switch {
	case rec.FailureReason == "":
		return rec.Error
	case rec.Error == "":
		return rec.FailureReason
	default:
		return rec.FailureReason + ": " + rec.Error
	}
}

// resultJSON is the agent_runs.result document:
// {"final_message": string|null, "note": Note|null, "parse_error": bool,
// "resume_fallback": string|null}. resume_fallback says why a proposal resume
// used a new session instead of session/load.
func (r *runRecorder) resultJSON(rec agent.RunRecord) []byte {
	var final any
	if rec.FinalMessage != "" {
		final = rec.FinalMessage
	}
	var note any
	if rec.Note != nil {
		note = rec.Note
	}
	var fallback any
	if rec.ResumeFallbackReason != "" {
		fallback = rec.ResumeFallbackReason
	}
	b, err := json.Marshal(map[string]any{"final_message": final, "note": note, "parse_error": rec.NoteParseError != "", "resume_fallback": fallback})
	if err != nil {
		return []byte(`{"final_message":null,"note":null,"parse_error":false,"resume_fallback":null}`)
	}
	return r.redactor().Bytes(b)
}

func (r *runRecorder) Event(ev agent.Event) {
	err := r.e.st.AppendAgentEvent(r.e.dbCtx, &store.AgentEvent{
		AgentRunID: ev.RunID, Seq: int64(ev.Seq), TS: ev.TS, Kind: ev.Kind, ToolCallID: ev.ToolCallID,
		Payload: store.Inline(string(r.redactor().Bytes(ev.Payload))),
	})
	if err != nil {
		r.e.log.Error("engine: record agent event", "agent_run", ev.RunID, "seq", ev.Seq, "err", err)
	}
}

// Permission creates the permission request on first sight and records the
// decision on later calls. A request already decided through the approval
// queue keeps that decision.
func (r *runRecorder) Permission(p agent.PermissionRecord) {
	pr := &store.PermissionRequest{
		ID: p.ID, AgentRunID: p.RunID, ToolCallID: p.ToolCallID, ToolKind: p.ToolKind,
		Title: r.redactor().String(p.Title), Options: optionsJSON(p.Options),
		Decision: store.PermDecision(p.Decision), DecidedBy: store.PermDecidedBy(p.DecidedBy),
		MatchedRule: p.MatchedRule, Reason: r.redactor().String(p.Reason), RequestedAt: p.RequestedAt, DecidedAt: p.DecidedAt,
	}
	input := p.RawInput
	if len(input) == 0 {
		input = p.Content // e.g. codex edits carry only a diff
	}
	pr.RawInput = store.Inline(string(r.redactor().Bytes(input)))

	r.mu.Lock()
	if r.perms == nil {
		r.perms = map[string]bool{}
	}
	known := r.perms[p.ID]
	r.perms[p.ID] = true
	r.mu.Unlock()

	var err error
	if known {
		err = r.e.st.UpdatePermissionRequest(r.e.dbCtx, pr)
		if errors.Is(err, store.ErrConflict) {
			err = nil // already decided through its approval
		}
	} else {
		err = r.e.st.CreatePermissionRequest(r.e.dbCtx, pr)
	}
	if err != nil {
		r.e.log.Error("engine: record permission request", "permission", p.ID, "err", err)
	}
}

// optionsJSON stores the offered options as [{optionId, name, kind}].
func optionsJSON(opts []agent.PermissionOption) json.RawMessage {
	type opt struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	}
	out := make([]opt, len(opts))
	for i, o := range opts {
		out[i] = opt{o.ID, o.Name, o.Kind}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return b
}

// Raw buffers one JSON-RPC line; the buffer is stored when the run stops.
func (r *runRecorder) Raw(f agent.RawFrame) {
	dir := "->"
	if f.Dir == "recv" {
		dir = "<-"
	}
	line := bytes.TrimSpace(f.Line)
	if !json.Valid(line) {
		b, _ := json.Marshal(string(line))
		line = b
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(struct {
		T   string          `json:"t"`
		Dir string          `json:"dir"`
		Msg json.RawMessage `json:"msg"`
	}{f.TS.UTC().Format(time.RFC3339Nano), dir, line})
	if err != nil {
		return
	}
	entry := bytes.TrimRight(buf.Bytes(), "\n")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.raw.Len()+len(entry) > maxRawBytes {
		return
	}
	r.raw.Write(r.redactor().Bytes(entry))
	r.raw.WriteByte('\n')
}
