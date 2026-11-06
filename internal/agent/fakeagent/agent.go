package fakeagent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/coder/acp-go-sdk"
)

// Agent is the scripted ACP agent. Create with New.
type Agent struct {
	script      *Script
	conn        *acp.AgentSideConnection
	sessionsDir string
	logPath     string

	mu       sync.Mutex
	sessions map[string]*session

	logMu sync.Mutex

	capsMu      sync.Mutex
	elicitation bool // client advertised form elicitation
}

type session struct {
	id    string
	cwd   string
	mode  string
	turns int // completed prompts
}

// persisted session record (one JSON line per record).
type record struct {
	T      string          `json:"t"` // session | mode | prompt | update | turn_done
	Cwd    string          `json:"cwd,omitempty"`
	Mode   string          `json:"mode,omitempty"`
	Text   string          `json:"text,omitempty"`
	Update json.RawMessage `json:"update,omitempty"`
}

// New builds the agent. sessionsDir and logPath override the script's values
// when non-empty. The returned value implements acp.Agent (and acp.AgentLoader
// only when the script advertises load_session).
func New(script *Script, sessionsDir, logPath string) acp.Agent {
	a := &Agent{
		script:      script,
		sessionsDir: firstNonEmpty(sessionsDir, script.SessionsDir),
		logPath:     firstNonEmpty(logPath, script.LogPath),
		sessions:    map[string]*session{},
	}
	if script.LoadSession {
		return &loadingAgent{a}
	}
	return a
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// loadingAgent adds session/load on top of Agent.
type loadingAgent struct{ *Agent }

func (a *Agent) SetAgentConnection(c *acp.AgentSideConnection) { a.conn = c }

func (a *Agent) logCall(entry map[string]any) {
	if a.logPath == "" {
		return
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	a.logMu.Lock()
	defer a.logMu.Unlock()
	f, err := os.OpenFile(a.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test harness path
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(b, '\n'))
}

func (a *Agent) Initialize(_ context.Context, p acp.InitializeRequest) (acp.InitializeResponse, error) {
	a.logCall(map[string]any{"method": "initialize", "pid": os.Getpid(), "client_capabilities": p.ClientCapabilities, "client_info": p.ClientInfo})
	a.capsMu.Lock()
	a.elicitation = p.ClientCapabilities.Elicitation != nil && p.ClientCapabilities.Elicitation.Form != nil
	a.capsMu.Unlock()
	info := a.script.AgentInfo
	if info == nil {
		info = &Info{Name: "fake-acp-agent", Version: "0.0.0"}
	}
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersionNumber,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: a.script.LoadSession,
		},
		AgentInfo:   &acp.Implementation{Name: info.Name, Version: info.Version},
		AuthMethods: []acp.AuthMethod{},
	}, nil
}

func (a *Agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *Agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *Agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionClose)
}

func (a *Agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionList)
}

func (a *Agent) ResumeSession(context.Context, acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	return acp.ResumeSessionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionResume)
}

func (a *Agent) initialMode() string {
	if a.script.InitialMode != "" {
		return a.script.InitialMode
	}
	if len(a.script.Modes) > 0 {
		return a.script.Modes[0].ID
	}
	return ""
}

func (a *Agent) modeState(current string) *acp.SessionModeState {
	if len(a.script.Modes) == 0 {
		return nil
	}
	st := &acp.SessionModeState{CurrentModeId: acp.SessionModeId(current)}
	for _, m := range a.script.Modes {
		st.AvailableModes = append(st.AvailableModes, acp.SessionMode{Id: acp.SessionModeId(m.ID), Name: firstNonEmpty(m.Name, m.ID)})
	}
	return st
}

func (a *Agent) configOptions(model string) []acp.SessionConfigOption {
	if len(a.script.Models) == 0 {
		return nil
	}
	opts := make(acp.SessionConfigSelectOptionsUngrouped, 0, len(a.script.Models))
	for _, m := range a.script.Models {
		opts = append(opts, acp.SessionConfigSelectOption{Name: m, Value: acp.SessionConfigValueId(m)})
	}
	cat := acp.SessionConfigOptionCategoryModel
	return []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id:           "model",
		Name:         "Model",
		Type:         "select",
		Category:     &cat,
		CurrentValue: acp.SessionConfigValueId(model),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &opts},
	}}}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "fake-" + hex.EncodeToString(b[:])
}

func (a *Agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	a.logCall(map[string]any{"method": "session/new", "cwd": p.Cwd, "mcp_servers": p.McpServers, "meta": p.Meta})
	s := &session{id: newID(), cwd: p.Cwd, mode: a.initialMode()}
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
	a.persist(s.id, record{T: "session", Cwd: s.cwd, Mode: s.mode})
	model := ""
	if len(a.script.Models) > 0 {
		model = a.script.Models[0]
	}
	return acp.NewSessionResponse{SessionId: acp.SessionId(s.id), Modes: a.modeState(s.mode), ConfigOptions: a.configOptions(model)}, nil
}

func (a *Agent) SetSessionMode(_ context.Context, p acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	a.logCall(map[string]any{"method": "session/set_mode", "mode": p.ModeId})
	s, err := a.lookup(string(p.SessionId))
	if err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	ok := false
	for _, m := range a.script.Modes {
		ok = ok || m.ID == string(p.ModeId)
	}
	if !ok {
		return acp.SetSessionModeResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown mode " + string(p.ModeId)})
	}
	a.mu.Lock()
	s.mode = string(p.ModeId)
	a.mu.Unlock()
	a.persist(s.id, record{T: "mode", Mode: s.mode})
	return acp.SetSessionModeResponse{}, nil
}

func (a *Agent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "only select options are supported"})
	}
	a.logCall(map[string]any{"method": "session/set_config_option", "config_id": p.ValueId.ConfigId, "value": p.ValueId.Value})
	if p.ValueId.ConfigId != "model" {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown config option " + string(p.ValueId.ConfigId)})
	}
	found := false
	for _, m := range a.script.Models {
		found = found || m == string(p.ValueId.Value)
	}
	if !found {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown model " + string(p.ValueId.Value)})
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: a.configOptions(string(p.ValueId.Value))}, nil
}

func (a *Agent) Cancel(_ context.Context, p acp.CancelNotification) error {
	// The SDK cancels the in-flight Prompt context; this only records it.
	a.logCall(map[string]any{"method": "session/cancel", "session_id": p.SessionId})
	return nil
}

func (a *Agent) lookup(id string) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok {
		return nil, acp.NewInvalidParams(map[string]any{"error": "unknown session " + id})
	}
	return s, nil
}

// LoadSession replays the persisted history of a session.
func (a *loadingAgent) LoadSession(ctx context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.logCall(map[string]any{"method": "session/load", "session_id": p.SessionId, "cwd": p.Cwd, "mcp_servers": p.McpServers, "meta": p.Meta})
	if a.sessionsDir == "" {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "agent has no sessions dir"})
	}
	f, err := os.Open(a.sessionPath(string(p.SessionId))) //nolint:gosec // test harness path
	if err != nil {
		return acp.LoadSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown session " + string(p.SessionId)})
	}
	defer func() { _ = f.Close() }()

	s := &session{id: string(p.SessionId), cwd: p.Cwd, mode: a.initialMode()}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return acp.LoadSessionResponse{}, fmt.Errorf("corrupt session file: %w", err)
		}
		var upd *acp.SessionUpdate
		switch r.T {
		case "mode":
			s.mode = r.Mode
		case "turn_done":
			s.turns++
		case "prompt":
			u := acp.UpdateUserMessageText(r.Text)
			upd = &u
		case "update":
			var u acp.SessionUpdate
			if err := json.Unmarshal(r.Update, &u); err != nil {
				return acp.LoadSessionResponse{}, fmt.Errorf("corrupt session update: %w", err)
			}
			upd = &u
		}
		if upd != nil {
			if err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: *upd}); err != nil {
				return acp.LoadSessionResponse{}, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	a.mu.Lock()
	a.sessions[s.id] = s
	a.mu.Unlock()
	model := ""
	if len(a.script.Models) > 0 {
		model = a.script.Models[0]
	}
	return acp.LoadSessionResponse{Modes: a.modeState(s.mode), ConfigOptions: a.configOptions(model)}, nil
}

func (a *Agent) sessionPath(id string) string {
	return filepath.Join(a.sessionsDir, filepath.Base(id)+".jsonl")
}

func (a *Agent) persist(id string, r record) {
	if a.sessionsDir == "" {
		return
	}
	if err := os.MkdirAll(a.sessionsDir, 0o750); err != nil {
		return
	}
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	f, err := os.OpenFile(a.sessionPath(id), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test harness path
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(b, '\n'))
}

func promptText(blocks []acp.ContentBlock) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Text != nil {
			sb.WriteString(b.Text.Text)
		}
	}
	return sb.String()
}

func (a *Agent) pickTurn(s *session, prompt string) (*Turn, error) {
	for i := range a.script.Turns {
		t := &a.script.Turns[i]
		w := t.When
		if w == nil {
			return t, nil
		}
		if w.Mode != "" && w.Mode != s.mode {
			continue
		}
		if w.SessionTurn != nil && *w.SessionTurn != s.turns {
			continue
		}
		if w.PromptMatches != "" && !regexp.MustCompile(w.PromptMatches).MatchString(prompt) {
			continue
		}
		return t, nil
	}
	return nil, fmt.Errorf("no script turn matches (mode=%q, session_turn=%d, prompt=%q)", s.mode, s.turns, prompt)
}

func (a *Agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	s, err := a.lookup(string(p.SessionId))
	if err != nil {
		return acp.PromptResponse{}, err
	}
	text := promptText(p.Prompt)
	a.mu.Lock()
	mode, turnIdx := s.mode, s.turns
	a.mu.Unlock()
	a.logCall(map[string]any{"method": "session/prompt", "session_id": s.id, "mode": mode, "session_turn": turnIdx, "text": text})
	a.persist(s.id, record{T: "prompt", Text: text})

	turn, err := a.pickTurn(s, text)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	ts := &turnState{a: a, s: s, ctx: ctx, prompt: text, emitted: map[string]bool{}}
	cancelled, err := ts.run(turn.Steps)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	a.persist(s.id, record{T: "turn_done"})
	a.mu.Lock()
	s.turns++
	a.mu.Unlock()
	if cancelled || ctx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	return acp.PromptResponse{StopReason: acp.StopReason(firstNonEmpty(turn.StopReason, string(acp.StopReasonEndTurn)))}, nil
}

var errUnsupportedStartupOp = errors.New("op not allowed in startup")

func (a *Agent) clientElicitation() bool {
	a.capsMu.Lock()
	defer a.capsMu.Unlock()
	return a.elicitation
}
