// Package core is the contract between the HTTP API (internal/api) and the
// execution engine (internal/engine). The API serves reads straight from the
// store and file CRUD through internal/spec; everything that executes or
// changes run state goes through Engine.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// Sentinel errors the API maps to problem types. Engines wrap them with
// context; callers match with errors.Is. store.ErrNotFound and
// store.ErrConflict are also returned as-is and map to not-found / conflict.
var (
	ErrPaused       = errors.New("paused")        // kill switch active: 423 paused
	ErrInvalidState = errors.New("invalid state") // e.g. approval already decided, node not runnable: 409 invalid-state
	ErrUnknownRef   = errors.New("unknown reference")
)

// ValidationError carries spec diagnostics (422 validation-failed).
type ValidationError struct {
	Diagnostics []spec.Diagnostic
}

func (e *ValidationError) Error() string { return "validation failed" }

// InjectResult is the outcome of POST /api/events.
type InjectResult struct {
	Event      *store.Event // nil when Duplicate
	Duplicate  bool
	FlowRunIDs []string // flow runs created by fan-out (queued)
}

// TestNodeRequest is POST /api/flows/{id}/test-node. Flow, when non-nil, is an
// unsaved editor copy that is validated and used instead of the file on disk.
type TestNodeRequest struct {
	FlowID string
	Flow   *spec.Flow
	NodeID string
	Input  json.RawMessage
	// EventID optionally supplies the original event for templates and HAL_EVENT_FILE.
	EventID string
	DryRun  bool
}

// ApprovalDecision is a user decision on an approval.
type ApprovalDecision struct {
	Approve bool
	Comment string
	// CancelRun applies to tool_call approvals: deny and cancel the agent run.
	CancelRun bool
	// Principal is the authenticated principal making the decision.
	Principal string
}

// ProbeResult is the outcome of POST /api/runtimes/{name}/probe.
type ProbeResult struct {
	OK              bool            `json:"ok"`
	AgentInfo       json.RawMessage `json:"agent_info,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	Modes           []string        `json:"modes,omitempty"`
	ConfigOptionIDs []string        `json:"config_option_ids,omitempty"`
	Error           string          `json:"error,omitempty"`
	Duration        time.Duration   `json:"duration"`
	ResolvedCommand []string        `json:"resolved_command,omitempty"`
}

// Engine is implemented by *engine.Engine.
type Engine interface {
	// InjectEvent runs item through dedup and fan-out exactly like a polled item.
	InjectEvent(ctx context.Context, sensorID string, item json.RawMessage) (InjectResult, error)
	// PollSensor runs one poll now and returns the recorded sensor run.
	PollSensor(ctx context.Context, sensorID string) (*store.SensorRun, error)
	// Replay creates a kind=replay flow run of eventID through the current flow version.
	Replay(ctx context.Context, eventID, flowID string, dryRun bool) (*store.FlowRun, error)
	// TestNode runs a single node as a kind=test flow run and waits for it to finish.
	TestNode(ctx context.Context, req TestNodeRequest) (*store.FlowRun, *store.NodeRun, error)
	// DecideApproval applies a decision and resumes/cancels the agent accordingly.
	DecideApproval(ctx context.Context, approvalID string, d ApprovalDecision) (*store.Approval, error)
	// Pause activates the kill switch; killRunning also terminates running nodes.
	Pause(ctx context.Context, killRunning bool, principal string) (store.KillSwitch, error)
	Resume(ctx context.Context, principal string) (store.KillSwitch, error)
	ProbeRuntime(ctx context.Context, name string) (ProbeResult, error)
	// Apply hands the engine a freshly loaded workspace (startup and hot reload).
	Apply(ws *spec.Workspace)
}

// Notification is a macOS notification request. URL is an absolute deep link
// into the UI (e.g. http://localhost:9001/inbox/<id>).
type Notification struct {
	Kind  string // "inbox" | "approval"
	Title string
	Body  string
	URL   string
}

// Notifier delivers notifications; implemented by internal/notify.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
