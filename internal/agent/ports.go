package agent

import (
	"context"
	"encoding/json"
	"time"
)

// Clock abstracts time so approval, run and sensor-schedule timeouts are testable.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
	// NewTimer returns a timer that fires once d has elapsed; stop it when it is no longer needed.
	NewTimer(d time.Duration) Timer
}

// Timer is a one-shot timer of a Clock.
type Timer interface {
	// C receives once when the timer fires.
	C() <-chan time.Time
	// Stop cancels the timer; it reports whether it was still pending.
	Stop() bool
}

// WallClock is the Clock of production: the system clock, in UTC.
type WallClock struct{}

func (WallClock) Now() time.Time                         { return time.Now().UTC() }
func (WallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (WallClock) NewTimer(d time.Duration) Timer         { return wallTimer{time.NewTimer(d)} }

type wallTimer struct{ t *time.Timer }

func (w wallTimer) C() <-chan time.Time { return w.t.C }
func (w wallTimer) Stop() bool          { return w.t.Stop() }

// Event is one row of agent_events: a session/update the agent streamed.
type Event struct {
	RunID      string
	Seq        int // 1-based, gap-free per run, in arrival order
	TS         time.Time
	Kind       string // message, thought, user_message, tool_call, tool_call_update, plan, mode, usage, ... or "harness"
	ToolCallID string
	// Payload is the session/update body as JSON.
	Payload json.RawMessage
}

// RawFrame is one JSON-RPC line exchanged with the agent.
type RawFrame struct {
	RunID string
	TS    time.Time
	// Dir is "send" (client to agent) or "recv" (agent to client).
	Dir  string
	Line []byte
}

// PermissionOption is one option offered by the agent.
type PermissionOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Permission decisions and deciders (permission_requests columns).
const (
	DecisionAllow     = "allow"
	DecisionReject    = "reject"
	DecisionCancelled = "cancelled"
	DecisionExpired   = "expired"

	DecidedByPolicy  = "policy"
	DecidedByUser    = "user"
	DecidedByTimeout = "timeout"
)

// PermissionRecord is one permission_requests row. It is upserted by ID: first
// with an empty Decision while a per-tool approval is pending, then again once
// decided.
type PermissionRecord struct {
	ID         string
	RunID      string
	ToolCallID string
	ToolKind   string
	Title      string
	RawInput   json.RawMessage
	// Content is the tool call content (diffs, text) as JSON, for display.
	Content     json.RawMessage
	Options     []PermissionOption
	Decision    string
	DecidedBy   string
	MatchedRule string
	Reason      string
	RequestedAt time.Time
	DecidedAt   *time.Time
}

// Recorder receives everything an AgentRun produces. RunUpdated, Event and
// Permission calls for one run are serialised and arrive in order; Raw may be
// called concurrently with them (it is fed by the transport goroutines), and
// implementations must tolerate several runs at once. Failures are the
// recorder's to log: a store error must not kill the agent.
type Recorder interface {
	// RunUpdated upserts the agent_runs row.
	RunUpdated(RunRecord)
	Event(Event)
	Permission(PermissionRecord)
	Raw(RawFrame)
}

// ToolVerdict is a user's decision on a gated tool call.
type ToolVerdict string

const (
	VerdictAllow ToolVerdict = "allow"
	VerdictDeny  ToolVerdict = "deny"
	// VerdictDenyCancel rejects the call and ends the run (status denied).
	VerdictDenyCancel ToolVerdict = "deny_cancel"
)

// ProposalApproval asks the broker to register a pending proposal approval.
type ProposalApproval struct {
	RunID     string
	NodeRunID string
	SessionID string
	Proposal  string
}

// ToolDecision is the user's answer to a gated tool call or an agent's request_approval.
type ToolDecision struct {
	Verdict ToolVerdict
	// Comment is the approver's steering text.
	Comment string
	// Held reports that the approval was raised held (the conversation's steering cap was reached): its denial
	// tells the agent to stop and wait.
	Held bool
}

// ApprovalBroker connects the agent client to the approval queue.
type ApprovalBroker interface {
	// SubmitProposal registers a pending proposal approval and returns its id.
	// It must not block: the agent process has exited and the approval
	// survives daemon restarts.
	SubmitProposal(ctx context.Context, p ProposalApproval) (approvalID string, err error)
}
