package agent

import (
	"context"
	"encoding/json"
	"time"
)

// Clock abstracts time so approval and run timeouts are testable.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now().UTC() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

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

// Recorder receives everything an AgentRun produces. Calls for one run are
// serialised in order; implementations must tolerate calls from several runs
// concurrently. Failures are the recorder's to log: a store error must not
// kill the agent.
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

// ToolApproval asks the broker to register a pending tool-call approval and
// wait for it.
type ToolApproval struct {
	RunID               string
	NodeRunID           string
	PermissionRequestID string
	ToolCallID          string
	ToolKind            string
	Title               string
	RawInput            json.RawMessage
	// Content is the tool call content, including diffs.
	Content json.RawMessage
	// ExpiresAt is when the harness will give up waiting.
	ExpiresAt time.Time
}

// ToolDecision is the user's answer to a ToolApproval.
type ToolDecision struct {
	Verdict ToolVerdict
	Comment string
}

// ApprovalBroker connects the agent client to the approval queue. Wave 2
// implements it over the store and the API's approve/deny endpoints.
type ApprovalBroker interface {
	// SubmitProposal registers a pending proposal approval and returns its id.
	// It must not block: the agent process has exited and the approval
	// survives daemon restarts.
	SubmitProposal(ctx context.Context, p ProposalApproval) (approvalID string, err error)
	// AwaitToolApproval registers a pending approval for a gated tool call
	// and blocks until a user decides. When ctx is done first (approval
	// timeout, run cancelled) it must mark the approval expired and return
	// ctx.Err(); the agent distinguishes the cause itself.
	AwaitToolApproval(ctx context.Context, a ToolApproval) (ToolDecision, error)
}
