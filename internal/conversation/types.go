// Package conversation is the one model over assistant chats (assist sessions) and the agent runs of flow agent
// nodes. A conversation is a chat (source "chat", id = the assist session id) or a flow agent run (source "flow", id =
// the id of the run's last phase). The package builds the list and detail views, the shared message transcript (a
// chat's stored messages, or a flow run's events folded into the same shape) and the deterministic compaction that a
// forked chat replays as context.
package conversation

import (
	"encoding/json"
	"time"
)

// Sources of a conversation.
const (
	SourceChat = "chat"
	SourceFlow = "flow"
)

// Conversation is one entry of the conversation list. Fields that do not apply are null.
type Conversation struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Kind is the assist target kind of a chat (general, sensor, flow, tool), or "agent" for a flow conversation.
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Status is the normalised status: running, awaiting_approval, idle, succeeded, failed, killed, denied,
	// interrupted, timeout or closed (see docs/api.md for the mapping).
	Status string `json:"status"`
	// Running is true while a turn or run is in progress (status running or awaiting_approval).
	Running bool `json:"running"`
	// Continuable: a flow run that ended can be continued in a chat; so can a chat that is not closed.
	Continuable bool    `json:"continuable"`
	Runtime     string  `json:"runtime"`
	Model       *string `json:"model"`
	FlowID      *string `json:"flow_id"`
	FlowRunID   *string `json:"flow_run_id"`
	NodeID      *string `json:"node_id"`
	NodeRunID   *string `json:"node_run_id"`
	// AgentRunID is the head agent run of a flow conversation; AssistSessionID the session of a chat.
	AgentRunID      *string `json:"agent_run_id"`
	AssistSessionID *string `json:"assist_session_id"`
	// ForkedFrom is set for a chat that continues the agent run of a flow node.
	ForkedFrom *ForkRef `json:"forked_from"`
	// ForkContext is how a fork restored the run's context: resumed or replayed; null before its first turn.
	ForkContext *string `json:"fork_context"`
	// ForkCount is the number of chats forked from this flow conversation.
	ForkCount int        `json:"fork_count"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	EndedAt   *time.Time `json:"ended_at"`
	// Preview is the start of the last user or assistant text of a chat, or the result of a flow run.
	Preview string `json:"preview"`
}

// ForkRef names the agent run a fork continues.
type ForkRef struct {
	AgentRunID string  `json:"agent_run_id"`
	FlowID     *string `json:"flow_id"`
	FlowRunID  *string `json:"flow_run_id"`
	NodeID     *string `json:"node_id"`
}

// Detail is one conversation with the chats forked from it.
type Detail struct {
	Conversation
	// Forks are the conversations forked from it, newest first (at most MaxForks).
	Forks []Conversation `json:"forks"`
}

// Message is one entry of a transcript, in the wire shape of an assist message.
type Message struct {
	// ID is the message id of a chat, or "<agent_run_id>:<zero padded seq>" for a flow conversation.
	ID string `json:"id"`
	// Role is user, assistant, tool or system.
	Role string `json:"role"`
	// Kind is "" for an ordinary message, "divider" for the "continued from run" line of a fork, "notice" for a system
	// notice (for example that the sandbox was recreated).
	Kind       string          `json:"kind"`
	Text       *string         `json:"text"`
	AgentRunID *string         `json:"agent_run_id"`
	ToolCall   json.RawMessage `json:"tool_call"`
	ApprovalID *string         `json:"approval_id"`
	CreatedAt  time.Time       `json:"created_at"`
}

// ToolCall is the stored form of a tool call message (the wire AssistToolCall).
type ToolCall struct {
	ToolCallID string            `json:"tool_call_id"`
	Title      string            `json:"title"`
	Kind       string            `json:"kind"`
	Status     string            `json:"status"`
	RawInput   json.RawMessage   `json:"raw_input"`
	Content    []json.RawMessage `json:"content"`
}

// Messages is the transcript of a conversation: its newest MaxMessages entries, oldest first.
type Messages struct {
	Items []Message `json:"items"`
	// Truncated is true when older entries were left out.
	Truncated bool `json:"truncated"`
}

// Limits.
const (
	// MaxMessages bounds a transcript; the newest messages are kept.
	MaxMessages = 2000
	// MaxForks bounds Detail.Forks.
	MaxForks = 50
	// PreviewLength is the length of Conversation.Preview in characters.
	PreviewLength = 160
)

// Normalised statuses beyond the store's assist and agent statuses.
const (
	StatusRunning          = "running"
	StatusAwaitingApproval = "awaiting_approval"
	StatusClosed           = "closed"
)
