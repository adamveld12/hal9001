// Package agent is the ACP client that runs coding agents for agent nodes
// (REQUIREMENTS §7): process lifecycle, session setup, permission policy,
// proposal and per-tool approvals, cancellation, structured notes and
// worktrees. It talks to the outside world through three narrow interfaces:
// Recorder (what happened), ApprovalBroker (who decides) and Clock.
package agent

import (
	"encoding/json"
	"errors"
	"time"
)

// Mode is the node's autonomy mode.
type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeApprove  Mode = "approve"
	ModeReadonly Mode = "readonly"
)

// ApproveStyle is the approve-mode flavour.
type ApproveStyle string

const (
	ApproveProposal ApproveStyle = "proposal"
	ApprovePerTool  ApproveStyle = "per_tool"
)

// Policy is the harness-side permission policy applied to every
// session/request_permission.
type Policy string

const (
	PolicyReadonly Policy = "readonly"
	PolicyGated    Policy = "gated"
	PolicyAuto     Policy = "auto"
)

// Phase of an AgentRun. Non-proposal runs are a single execute phase.
type Phase string

const (
	PhasePropose Phase = "propose"
	PhaseExecute Phase = "execute"
)

// ResumedVia records how a proposal's phase 2 reached the earlier session.
type ResumedVia string

const (
	ResumedLoad       ResumedVia = "load"
	ResumedNewSession ResumedVia = "new_session"
)

// Status is the agent_run.status value.
type Status string

const (
	StatusRunning          Status = "running"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusSucceeded        Status = "succeeded"
	StatusFailed           Status = "failed"
	StatusDenied           Status = "denied"
	StatusKilled           Status = "killed"
	StatusInterrupted      Status = "interrupted"
	StatusTimeout          Status = "timeout"
)

// Failure reasons recorded in RunRecord.FailureReason.
const (
	ReasonTimeout         = "timeout"
	ReasonApprovalTimeout = "approval_timeout"
	ReasonDenied          = "denied"
	ReasonKilled          = "killed"
	ReasonInterrupted     = "interrupted"
	ReasonSpawn           = "spawn_failed"
	ReasonAgentExit       = "agent_exit"
	ReasonAgentError      = "agent_error"
	ReasonModeUnavailable = "mode_unavailable"
	ReasonModelRejected   = "model_rejected"
	ReasonStopReason      = "stop_reason"
	ReasonEmptyProposal   = "empty_proposal"
	// ReasonPolicyEndedTurn: the policy rejected a tool call and the agent
	// ended its turn instead of continuing (e.g. codex).
	ReasonPolicyEndedTurn = "policy_rejected"
)

// Cancellation causes. A caller cancels the context it passes to Run/Resume
// with one of these (context.WithCancelCause) to say why.
var (
	// ErrKilled is the kill-switch cause: the run ends with status killed.
	ErrKilled = errors.New("agent run killed")
	// ErrInterrupted is the daemon-shutdown cause: status interrupted.
	ErrInterrupted = errors.New("agent run interrupted")

	errTimeout         = errors.New("agent run timed out")
	errApprovalTimeout = errors.New("approval timed out")
	errDeniedCancel    = errors.New("tool call denied and run cancelled")
)

// Defaults.
const (
	DefaultTimeout         = 30 * time.Minute
	DefaultApprovalTimeout = time.Hour
	DefaultKillGrace       = 10 * time.Second
	DefaultCancelGrace     = 5 * time.Second
	DefaultShutdownGrace   = 3 * time.Second
)

// ModeNames are the runtime's session mode names for the two harness
// intents. An empty name means "do not call session/set_mode": the runtime
// is made restrictive some other way (Ask/Full spawn args).
type ModeNames struct {
	Ask  string `json:"ask"`
	Full string `json:"full"`
}

// PolicySpawn is extra spawn configuration applied when the run's policy
// needs a restrictive (Ask: readonly, gated) or permissive (Full: auto)
// agent. Some runtimes pick their approval behaviour with a process flag
// instead of an ACP session mode.
type PolicySpawn struct {
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// RuntimeConfig is one entry of config.yaml `runtimes`.
type RuntimeConfig struct {
	Command []string          `json:"command"`
	Modes   ModeNames         `json:"modes"`
	Env     map[string]string `json:"env,omitempty"`
	// Ask applies to readonly and gated policies, Full to the auto policy.
	Ask  PolicySpawn `json:"ask"`
	Full PolicySpawn `json:"full"`
	// Elicitation advertises the form-elicitation client capability. Needed
	// for runtimes that deliver non-execute tool approvals as
	// elicitation/create instead of session/request_permission (omp); without
	// it they refuse those tools outright.
	Elicitation bool `json:"elicitation,omitempty"`
	// SessionMeta is sent as `_meta` on session/new and session/load. It
	// carries runtime-specific session options, e.g. for claude-agent-acp
	// {"claudeCode": {"options": {"settingSources": ["project"]}}} so the
	// user's own allow rules cannot bypass the harness's policy.
	SessionMeta map[string]any `json:"session_meta,omitempty"`
}

// MCPServer is an MCP server handed to the agent in session/new. Type is
// "stdio" (default when Command is set), "http" or "sse".
type MCPServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// AllowRules extend the read-only permission set (§7.2).
type AllowRules struct {
	// Tools are regexes matched against the tool title and name.
	Tools []string `json:"tools,omitempty"`
	// Commands are regexes matched against the command of execute-kind tools.
	Commands []string `json:"commands,omitempty"`
}

// PolicyFor is the permission policy of a node's phase (§7.2). Proposal
// phase 1 is read-only; the approved phase 2 is auto.
func PolicyFor(mode Mode, style ApproveStyle, phase Phase) Policy {
	switch mode {
	case ModeAuto:
		return PolicyAuto
	case ModeApprove:
		if style == ApprovePerTool {
			return PolicyGated
		}
		if phase == PhaseExecute {
			return PolicyAuto
		}
		return PolicyReadonly
	default:
		return PolicyReadonly
	}
}

// RunRequest describes one agent NodeRun (for proposal nodes: phase 1).
type RunRequest struct {
	// RunID is the AgentRun id; a ULID is generated when empty.
	RunID     string
	NodeRunID string
	// Runtime is the runtime's name in config (recorded only).
	Runtime string
	Config  RuntimeConfig
	Mode    Mode
	// Approve is the approve-mode style; empty means proposal.
	Approve ApproveStyle
	// Prompt is the rendered prompt including extra_prompt.
	Prompt string
	// Model is an optional model id for the agent's "model" config option.
	Model      string
	Cwd        string
	MCPServers []MCPServer
	Allow      AllowRules
	// Timeout bounds the whole run (default 30m).
	Timeout time.Duration
	// ApprovalTimeout bounds a per-tool approval wait (default 1h).
	ApprovalTimeout time.Duration
	// Env is extra KEY=VALUE environment for the agent process.
	Env []string
	// ExpectNote parses the final message as a structured note (§7.4).
	ExpectNote bool
}

func (r *RunRequest) style() ApproveStyle {
	if r.Approve == "" {
		return ApproveProposal
	}
	return r.Approve
}

// ResumeRequest is phase 2 of a proposal: the approved plan is executed.
type ResumeRequest struct {
	// RunRequest is the same request that produced the proposal. Its
	// RunID, when set, is the id of the new (execute-phase) AgentRun.
	RunRequest
	// PrevRunID is the propose-phase AgentRun.
	PrevRunID string
	// SessionID is the propose-phase session (RunRecord.SessionID).
	SessionID string
	// Proposal is the approved proposal markdown.
	Proposal string
	// Comment is the approver's optional comment.
	Comment string
}

// AgentInfo is what the agent reported in initialize.
type AgentInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// RunRecord is the agent_runs row (upserted through Recorder.RunUpdated as the
// run progresses) and also the result of Run/Resume.
type RunRecord struct {
	ID          string
	NodeRunID   string
	ParentRunID string // propose-phase run for an execute-phase resume
	Runtime     string
	Argv        []string // redaction of env secrets is the store adapter's job
	Cwd         string
	Mode        Mode
	Approve     ApproveStyle // approve mode only
	Policy      Policy
	Phase       Phase
	Prompt      string // exactly what was sent to session/prompt
	SessionID   string
	ResumedVia  ResumedVia
	// ResumeFallbackReason says why session/load was not used.
	ResumeFallbackReason string
	AgentInfo            *AgentInfo
	// Capabilities is the agent's initialize agentCapabilities JSON.
	Capabilities json.RawMessage
	Status       Status
	StopReason   string
	// FailureReason is one of the Reason* constants when Status is not
	// succeeded/awaiting_approval.
	FailureReason string
	Error         string
	// FinalMessage is the last contiguous run of agent message text;
	// FullMessage is all agent message text of the turn.
	FinalMessage string
	FullMessage  string
	// Note is set when ExpectNote and parsing succeeded; NoteParseError when
	// it failed (the caller then creates an inbox item from FinalMessage with
	// priority normal and the parse_error flag).
	Note           *Note
	NoteParseError string
	// Proposal and ApprovalID are set for a propose-phase run that produced
	// a proposal (Status awaiting_approval).
	Proposal   string
	ApprovalID string
	StartedAt  time.Time
	EndedAt    *time.Time
	// ExitCode is the agent process exit code (-1 when killed by a signal).
	ExitCode *int
	// Stderr is the tail of the agent's stderr.
	Stderr string
}
