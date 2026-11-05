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
	// Event is the stored event: the new one, or, when Duplicate, the existing
	// event with the same dedup key (nil only if it has been pruned).
	Event      *store.Event
	Duplicate  bool
	DedupKey   string
	FlowRunIDs []string // flow runs created by fan-out (queued); empty when Duplicate
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
	// PauseSensor stops the scheduler polling one sensor (persisted; idempotent).
	// Poll now and event injection still work. principal is recorded as who paused it.
	PauseSensor(ctx context.Context, sensorID, principal string) (store.SensorState, error)
	// ResumeSensor lets the scheduler poll the sensor again (idempotent).
	ResumeSensor(ctx context.Context, sensorID string) (store.SensorState, error)
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

// AssistCreateRequest is POST /api/assist/sessions.
type AssistCreateRequest struct {
	TargetKind string // "sensor"
	// TargetID is the sensor to edit; empty authors a new sensor.
	TargetID string
	Runtime  string
	Model    string
	// YAML, when non-nil, is the initial draft instead of the sensor's file or the skeleton.
	YAML *string
	// Principal is the authenticated caller, recorded on the session's agent runs.
	Principal string
}

// AssistTestItem is one parsed item of a draft test run.
type AssistTestItem struct {
	DedupKey *string         `json:"dedup_key"`
	KeyError *string         `json:"key_error"`
	Item     json.RawMessage `json:"item"`
}

// AssistText is captured, redacted process output (the contract's TextBlob).
type AssistText struct {
	Text      string  `json:"text"`
	Size      int64   `json:"size"`
	Truncated bool    `json:"truncated"`
	Blob      *string `json:"blob"`
}

// AssistTestResult is the outcome of POST /api/assist/sessions/{id}/test.
type AssistTestResult struct {
	ExitCode   int              `json:"exit_code"`
	DurationMS int64            `json:"duration_ms"`
	TimedOut   bool             `json:"timed_out"`
	Items      []AssistTestItem `json:"items"`
	ItemCount  int              `json:"item_count"`
	ParseError *string          `json:"parse_error"`
	Stdout     AssistText       `json:"stdout"`
	Stderr     AssistText       `json:"stderr"`
}

// Assist is the AI-assisted sensor authoring service, implemented by
// *assist.Manager. Reads (sessions, messages, the draft) come straight from the
// store; everything that starts an agent turn, runs code or changes a draft goes
// through here. Errors follow the Engine conventions: store.ErrNotFound,
// ErrPaused, ErrInvalidState, *ValidationError, plus *DraftConflictError.
type Assist interface {
	CreateSession(ctx context.Context, req AssistCreateRequest) (*store.AssistSession, error)
	// SendMessage stores the user's message and starts one agent turn. yaml, when non-nil,
	// first replaces the draft. ErrInvalidState while a turn runs; ErrPaused under the kill switch.
	SendMessage(ctx context.Context, sessionID, text string, yaml *string) (*store.AssistSession, error)
	// UpdateDraft replaces the draft (updated_by user). expectedHash, when non-empty, must equal
	// the current draft hash, else *DraftConflictError.
	UpdateDraft(ctx context.Context, sessionID, yaml, expectedHash string) (*store.AssistSession, error)
	// TestDraft runs the draft like Poll now and returns the redacted result without recording anything.
	TestDraft(ctx context.Context, sessionID string) (AssistTestResult, error)
	// Cancel cancels the running turn; without one it returns the session unchanged.
	Cancel(ctx context.Context, sessionID string) (*store.AssistSession, error)
	// Close ends the session: stops the agent, removes the draft directory, marks it closed.
	Close(ctx context.Context, sessionID string) error
}

// DraftConflictError is UpdateDraft's hash mismatch (409 conflict).
type DraftConflictError struct {
	Expected, Current string
}

func (e *DraftConflictError) Error() string {
	return "the draft changed since it was read (hash " + e.Current + ", expected " + e.Expected + ")"
}
