// Package agent is the ACP client that runs coding agents for agent nodes
// (REQUIREMENTS §7): process lifecycle, session setup, permission policy,
// proposal and per-tool approvals, cancellation, structured notes and
// worktrees. It talks to the outside world through three narrow interfaces:
// Recorder (what happened), ApprovalBroker (who decides) and Clock.
package agent

import (
	"context"
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
	// ReasonNoOutput: the run required an emit_<port> call and ended without a valid one, even after a reminder.
	ReasonNoOutput = "no_output"
	// ReasonPolicyEndedTurn: the policy rejected a tool call and the agent
	// ended its turn instead of continuing (e.g. codex).
	ReasonPolicyEndedTurn = "policy_rejected"
	// ReasonCancelled: the user cancelled a session turn.
	ReasonCancelled = "cancelled"
)

// Cancellation causes. A caller cancels the context it passes to
// Run/Resume/Session.Prompt with one of these (context.WithCancelCause) to say
// why.
var (
	// ErrKilled is the kill-switch cause: the run ends with status killed.
	ErrKilled = errors.New("agent run killed")
	// ErrInterrupted is the daemon-shutdown cause: status interrupted.
	ErrInterrupted = errors.New("agent run interrupted")
	// ErrCancelled is the user-cancel cause for one session turn: the turn
	// ends killed with reason cancelled and the session's process normally
	// stays usable.
	ErrCancelled = errors.New("agent run cancelled")

	errTimeout         = errors.New("agent run timed out")
	errApprovalTimeout = errors.New("approval timed out")
	// ErrDeniedCancel is the cause of a run cancelled because the user denied a tool call and chose to stop the run.
	ErrDeniedCancel = errors.New("tool call denied and run cancelled")
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
	// Lockdown is set by ApplyLockdown: what the run must check before it starts.
	Lockdown Lockdown `json:"lockdown"`
	// Unavailable, when set, is why the runtime has no command to start (the agent or its adapter is not installed)
	// and how to fix it; Command is empty and runs fail with this message.
	Unavailable string `json:"unavailable,omitempty"`
}

// Lockdown is the part of a runtime lockdown (docs/acp-lockdown.md) that is enforced when a run starts.
type Lockdown struct {
	// Kind is the runtime family the lockdown was applied for ("claude", "codex", "omp"); empty: none applied.
	Kind string `json:"kind,omitempty"`
	// RefuseCwdEntries are names that must not exist in a run's working directory (Codex trusts every cwd and would
	// start the MCP servers of <cwd>/.codex/config.toml).
	RefuseCwdEntries []string `json:"refuse_cwd_entries,omitempty"`
	// WaitForMCP makes the first prompt of a process wait (RunRequest.ToolsReady) until the agent's MCP client has
	// connected: omp starts its MCP servers asynchronously and answers a prompt that arrives first without their tools.
	WaitForMCP bool `json:"wait_for_mcp,omitempty"`
	// Err, when set, is why the lockdown could not be prepared; runs of the runtime fail with it instead of starting
	// an agent that still has its built-in tools.
	Err string `json:"err,omitempty"`
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

// AllowRules name the tools beyond hal's own that the agent may use.
type AllowRules struct {
	// Tools are regexes matched against the tool title and name: the tools of MCP servers the node declared.
	Tools []string `json:"tools,omitempty"`
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
	// Output, when set, ties the run to hal's submit_output tool (see OutputHook).
	Output *OutputHook
	// ToolsReady, when set, is called after the session is set up and before the first prompt of a process, to wait
	// until the agent can see the MCP tools hal injected (some runtimes do not wait for their MCP servers).
	ToolsReady func(ctx context.Context)
}

// OutputHook connects a run to the output tools of its hal tool session (submit_output or emit_<port>). The executor
// does not know the tools: it asks the hook whether the agent made a valid call.
type OutputHook struct {
	// Done reports that the agent made at least one valid call.
	Done func() bool
	// Value returns the first valid submission (a JSON object), or nil when there is none (submit_output).
	Value func() json.RawMessage
	// Emissions returns the valid calls in call order (emit_<port> tools).
	Emissions func() []Emission
	// LastError returns why the latest call was refused ("" when none was).
	LastError func() string
	// Required: a turn that ends without a valid call is followed by one reminder (FollowUp) in the same session, and
	// a run still without one fails with ReasonNoOutput. Without Required a missing call is not an error here (the
	// caller falls back to parsing the final message).
	Required bool
	// FollowUp is the reminder prompt (Required only).
	FollowUp string
	// ResumeNote is appended to the "Approved. Execute the plan." prompt of an approved proposal, so the agent knows
	// how the result is returned (Required only).
	ResumeNote string
	// AsNote turns a valid submission into the run's Note (its schema is the note schema).
	AsNote bool
}

// Emission is one valid emit_<port> call: the port and the validated arguments.
type Emission struct {
	Port   string
	Output json.RawMessage
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
	// Output is the valid submit_output submission (JSON object), nil when the run made none.
	Output json.RawMessage
	// Emissions are the valid emit_<port> calls in call order (the last one only when the node emits one result).
	Emissions []Emission
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
