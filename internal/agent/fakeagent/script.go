// Package fakeagent implements a scripted ACP agent used by tests. The script
// format is documented in cmd/fake-acp-agent/README.md.
package fakeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// Step operations.
const (
	OpMessage      = "message"
	OpThought      = "thought"
	OpPlan         = "plan"
	OpToolCall     = "tool_call"
	OpToolUpdate   = "tool_call_update"
	OpPermission   = "permission"
	OpSleep        = "sleep"
	OpExit         = "exit"
	OpHang         = "hang"
	OpSpawnChild   = "spawn_child"
	OpIgnoreSIGTRM = "ignore_sigterm"
	OpWriteFile    = "write_file"
	OpDeleteFile   = "delete_file"
	OpExec         = "exec"
	OpStderr       = "stderr"
	OpError        = "error"
)

// Script drives one fake agent process. It is JSON.
type Script struct {
	AgentInfo *Info `json:"agent_info,omitempty"`
	// LoadSession advertises (and implements) session/load.
	LoadSession bool `json:"load_session,omitempty"`
	// Modes are the advertised session modes; the first is the initial mode
	// unless InitialMode is set. Empty means the agent advertises no modes.
	Modes       []Mode `json:"modes,omitempty"`
	InitialMode string `json:"initial_mode,omitempty"`
	// Models advertises a "model" select config option with these values.
	Models []string `json:"models,omitempty"`
	// SessionsDir persists sessions (JSONL) so session/load can replay them.
	// The --sessions-dir flag / FAKE_ACP_SESSIONS env override it.
	SessionsDir string `json:"sessions_dir,omitempty"`
	// LogPath receives one JSON line per ACP call received (see README).
	LogPath string `json:"log_path,omitempty"`
	// Startup steps run before initialize is answered (e.g. crash on start).
	Startup []Step `json:"startup,omitempty"`
	// Turns are tried in order for every session/prompt; the first whose When
	// matches runs. A turn without When always matches.
	Turns []Turn `json:"turns"`
}

// Info is the agent implementation info returned by initialize.
type Info struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Mode is an advertised session mode.
type Mode struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Turn is the reaction to one session/prompt.
type Turn struct {
	When  *When  `json:"when,omitempty"`
	Steps []Step `json:"steps"`
	// StopReason defaults to end_turn.
	StopReason string `json:"stop_reason,omitempty"`
}

// When selects a turn. All set fields must match.
type When struct {
	// Mode is the session's current mode id.
	Mode string `json:"mode,omitempty"`
	// PromptMatches is a regexp over the prompt text.
	PromptMatches string `json:"prompt_matches,omitempty"`
	// SessionTurn is the 0-based count of prompts already answered in this
	// session (persisted, so it survives session/load).
	SessionTurn *int `json:"session_turn,omitempty"`
}

// Content is tool-call content: text or a diff.
type Content struct {
	Type    string  `json:"type"` // text | diff
	Text    string  `json:"text,omitempty"`
	Path    string  `json:"path,omitempty"`
	OldText *string `json:"old_text,omitempty"`
	NewText string  `json:"new_text,omitempty"`
}

// PlanEntry is one plan item.
type PlanEntry struct {
	Content  string `json:"content"`
	Status   string `json:"status,omitempty"`   // pending | in_progress | completed
	Priority string `json:"priority,omitempty"` // high | medium | low
}

// Option is a permission option offered to the client.
type Option struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

// Step is one action in a turn. Op selects which fields apply.
type Step struct {
	Op string `json:"op"`

	// message / thought / stderr / error
	Text    string   `json:"text,omitempty"`
	Chunks  []string `json:"chunks,omitempty"`
	DelayMS int      `json:"delay_ms,omitempty"` // between chunks

	// tool_call / tool_call_update / permission
	ID         string    `json:"id,omitempty"`
	Title      string    `json:"title,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	Status     string    `json:"status,omitempty"`
	RawInput   any       `json:"raw_input,omitempty"`
	RawOutput  any       `json:"raw_output,omitempty"`
	Content    []Content `json:"content,omitempty"`
	Locations  []string  `json:"locations,omitempty"`
	Options    []Option  `json:"options,omitempty"`
	OnAllow    []Step    `json:"on_allow,omitempty"`
	OnReject   []Step    `json:"on_reject,omitempty"`
	OnCancel   []Step    `json:"on_cancel,omitempty"`
	NoToolCall bool      `json:"no_tool_call,omitempty"` // permission: do not emit the tool_call first
	NoAutoDone bool      `json:"no_auto_update,omitempty"`
	// Via selects how permission asks the client: "" = session/request_permission,
	// "elicitation" = an `elicitation/create` form as omp does for non-execute
	// tools (name in Name, details in Text).
	Via  string `json:"via,omitempty"`
	Name string `json:"name,omitempty"`
	// CancelOnReject ends the turn with stopReason cancelled when the client
	// rejects the call, as codex-acp does (its only reject option is "cancel").
	CancelOnReject bool `json:"cancel_on_reject,omitempty"`

	// plan
	Entries []PlanEntry `json:"entries,omitempty"`

	// sleep (ms) / exit (code) / hang
	MS           int  `json:"ms,omitempty"`
	Code         int  `json:"code,omitempty"`
	IgnoreCancel bool `json:"ignore_cancel,omitempty"`

	// write_file / delete_file / exec / spawn_child
	Path    string   `json:"path,omitempty"` // relative to the session cwd
	Data    string   `json:"data,omitempty"`
	Cmd     []string `json:"cmd,omitempty"`
	PidFile string   `json:"pid_file,omitempty"`
}

// Load reads and validates a script file.
func Load(path string) (*Script, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from the test harness
	if err != nil {
		return nil, err
	}
	var s Script
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse script %s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("script %s: %w", path, err)
	}
	return &s, nil
}

// Write serialises the script to path.
func (s *Script) Write(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Validate rejects malformed scripts so a typo fails loudly instead of
// silently doing nothing.
func (s *Script) Validate() error {
	modes := map[string]bool{}
	for _, m := range s.Modes {
		modes[m.ID] = true
	}
	if s.InitialMode != "" && !modes[s.InitialMode] {
		return fmt.Errorf("initial_mode %q is not in modes", s.InitialMode)
	}
	if err := validateSteps("startup", s.Startup); err != nil {
		return err
	}
	for i, t := range s.Turns {
		where := fmt.Sprintf("turns[%d]", i)
		if t.When != nil {
			if t.When.Mode != "" && !modes[t.When.Mode] {
				return fmt.Errorf("%s.when.mode %q is not in modes", where, t.When.Mode)
			}
			if _, err := regexp.Compile(t.When.PromptMatches); err != nil {
				return fmt.Errorf("%s.when.prompt_matches: %w", where, err)
			}
		}
		if err := validateSteps(where, t.Steps); err != nil {
			return err
		}
	}
	return nil
}

func validateSteps(where string, steps []Step) error {
	for i, st := range steps {
		w := fmt.Sprintf("%s.steps[%d]", where, i)
		switch st.Op {
		case OpMessage, OpThought, OpStderr:
			if st.Text == "" && len(st.Chunks) == 0 {
				return fmt.Errorf("%s: %s needs text or chunks", w, st.Op)
			}
		case OpToolCall, OpToolUpdate:
			if st.ID == "" {
				return fmt.Errorf("%s: %s needs id", w, st.Op)
			}
		case OpPermission:
			if st.ID == "" {
				return fmt.Errorf("%s: permission needs id", w)
			}
			for _, branch := range [][]Step{st.OnAllow, st.OnReject, st.OnCancel} {
				if err := validateSteps(w, branch); err != nil {
					return err
				}
			}
		case OpWriteFile, OpDeleteFile:
			if st.Path == "" {
				return fmt.Errorf("%s: %s needs path", w, st.Op)
			}
		case OpExec, OpSpawnChild:
			if len(st.Cmd) == 0 {
				return fmt.Errorf("%s: %s needs cmd", w, st.Op)
			}
		case OpPlan, OpSleep, OpExit, OpHang, OpIgnoreSIGTRM, OpError:
		default:
			return fmt.Errorf("%s: unknown op %q", w, st.Op)
		}
	}
	return nil
}
