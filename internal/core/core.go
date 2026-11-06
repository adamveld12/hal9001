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

// PollOptions are the run options of a manual poll. The zero value is the
// plain "Poll now": new items become events and fan out to every enabled
// subscribed flow.
type PollOptions struct {
	// RerunSeen also starts a kind=replay run of every item whose dedup key was
	// already seen, into each target flow. The dedup state does not change.
	RerunSeen bool
	// Flows restricts fan-out and re-runs to these flows. Each must exist and
	// subscribe to the sensor; unlike the default targets it may be disabled.
	// Empty means every enabled flow subscribed to the sensor.
	Flows []string
}

// PollResult is the outcome of a manual poll.
type PollResult struct {
	Run         *store.SensorRun
	NewEvents   int      // items that became events
	RerunEvents int      // already-seen items re-run into at least one flow
	FlowRunIDs  []string // runs created: live runs of new events, then replay runs
}

// RunFlowOptions are the options of Engine.RunFlow.
type RunFlowOptions struct {
	RerunSeen bool
}

// FlowSensorResult is one subscribed sensor's part of RunFlow. Poll is nil and
// Error set when the poll could not be started (the sensor is missing or
// invalid, or a poll of it is already running).
type FlowSensorResult struct {
	SensorID string
	Poll     *PollResult
	Error    string
}

// RunFlowResult is the outcome of POST /api/flows/{id}/run.
type RunFlowResult struct {
	FlowID     string
	Sensors    []FlowSensorResult // in the flow's subscribe order
	FlowRunIDs []string           // every run created, in Sensors order
}

// ResetOptions are the options of Engine.ResetSensor.
type ResetOptions struct {
	// Run immediately runs the sensor now (a manual poll, never baseline).
	Run bool
	// Flows restricts that run to these flows (see PollOptions.Flows).
	Flows []string
}

// ResetResult is the outcome of POST /api/sensors/{id}/reset.
type ResetResult struct {
	State       store.SensorState
	KeysCleared int64
	Poll        *PollResult // set when the reset ran the sensor
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
	// PollSensor runs one poll now (a manual poll: it never applies
	// first_run: baseline) and returns the recorded sensor run with what the
	// poll started.
	PollSensor(ctx context.Context, sensorID string, opts PollOptions) (PollResult, error)
	// RunFlow polls every sensor the flow subscribes to now, in parallel, with
	// the flow as the only target. It works for a disabled flow.
	RunFlow(ctx context.Context, flowID string, opts RunFlowOptions) (RunFlowResult, error)
	// ResetSensor clears the sensor's dedup keys and incremental state, records
	// principal as who did it, and optionally runs the sensor at once.
	ResetSensor(ctx context.Context, sensorID, principal string, opts ResetOptions) (ResetResult, error)
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
	// RemoveQueued ends a flow run waiting in the queue as cancelled and records
	// principal. A run that already runs a node fails with ErrInvalidState.
	RemoveQueued(ctx context.Context, flowRunID, principal string) (*store.FlowRun, error)
	// ClearQueue removes every queued run (of one flow when flowID is set) and returns their ids.
	ClearQueue(ctx context.Context, flowID, principal string) ([]string, error)
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
	TargetKind string // "sensor" | "flow"
	// TargetID is the sensor or flow to edit; empty authors a new one.
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

// AssistTestResult is the sensor variant of the outcome of POST /api/assist/sessions/{id}/test.
type AssistTestResult struct {
	Kind       string           `json:"kind"`
	ExitCode   int              `json:"exit_code"`
	DurationMS int64            `json:"duration_ms"`
	TimedOut   bool             `json:"timed_out"`
	Items      []AssistTestItem `json:"items"`
	ItemCount  int              `json:"item_count"`
	ParseError *string          `json:"parse_error"`
	Stdout     AssistText       `json:"stdout"`
	Stderr     AssistText       `json:"stderr"`
}

// AssistTestRequest is the optional body of POST /api/assist/sessions/{id}/test (flow sessions).
// At most one field is set.
type AssistTestRequest struct {
	SampleIndex *int
	EventID     string
	Input       json.RawMessage
}

// AssistSample is one sample input of a flow session.
type AssistSample struct {
	Index    int             `json:"index"`
	SensorID string          `json:"sensor_id"`
	Payload  json.RawMessage `json:"payload"`
}

// AssistTestOutcome is the result of a draft test: exactly one of Sensor and Flow is set.
type AssistTestOutcome struct {
	Sensor *AssistTestResult
	Flow   *AssistFlowTestResult
}

// MarshalJSON writes the variant that is set.
func (o AssistTestOutcome) MarshalJSON() ([]byte, error) {
	if o.Flow != nil {
		return json.Marshal(o.Flow)
	}
	return json.Marshal(o.Sensor)
}

// FlowTestRequest asks the engine to run a flow draft once on one input as a test run (see Engine.TestFlow).
// Flow is already parsed and validated by the caller.
type FlowTestRequest struct {
	Flow *spec.Flow
	// Input is the JSON input of the entry nodes; EventID optionally names the stored event it came from.
	Input   json.RawMessage
	EventID string
}

// AssistFlowTestOutput is one payload a node emitted.
type AssistFlowTestOutput struct {
	Port    string          `json:"port"`
	Payload json.RawMessage `json:"payload"`
}

// AssistInboxPreview is the inbox item an inbox node would create.
type AssistInboxPreview struct {
	Title          string                   `json:"title"`
	Priority       string                   `json:"priority"`
	SummaryMD      string                   `json:"summary_md"`
	SuggestedReply string                   `json:"suggested_reply"`
	Links          []AssistInboxPreviewLink `json:"links"`
}

// AssistInboxPreviewLink is a link of an AssistInboxPreview.
type AssistInboxPreviewLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// AssistFlowTestNode is what one node did in a flow test.
type AssistFlowTestNode struct {
	NodeID      string                 `json:"node_id"`
	NodeRunID   string                 `json:"node_run_id"`
	Status      string                 `json:"status"`
	Decision    *string                `json:"decision"`
	Reason      *string                `json:"reason"`
	Error       *string                `json:"error"`
	Ports       []string               `json:"ports"`
	Outputs     []AssistFlowTestOutput `json:"outputs"`
	WouldCreate *AssistInboxPreview    `json:"would_create"`
	Stdout      *string                `json:"stdout"`
	Stderr      *string                `json:"stderr"`
	ExitCode    *int                   `json:"exit_code"`
	DurationMS  *int64                 `json:"duration_ms"`
}

// AssistFlowTestEdge is an edge a payload travelled along in a flow test.
type AssistFlowTestEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port"`
}

// AssistFlowTestResult is the flow variant of the outcome of POST /api/assist/sessions/{id}/test.
type AssistFlowTestResult struct {
	Kind       string               `json:"kind"`
	FlowRunID  string               `json:"flow_run_id"`
	Status     string               `json:"status"`
	Input      json.RawMessage      `json:"input"`
	Nodes      []AssistFlowTestNode `json:"nodes"`
	EdgesTaken []AssistFlowTestEdge `json:"edges_taken"`
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
	// TestDraft runs the draft and returns the redacted result without recording anything a user would see.
	// Sensor sessions run it like Poll now; flow sessions run it once on the sample req names. The code runs
	// with real credentials: a test by the user always runs and marks the script approved; a test byAgent runs
	// only while the script equals the approved one (else ErrInvalidState).
	TestDraft(ctx context.Context, sessionID string, byAgent bool, req AssistTestRequest) (AssistTestOutcome, error)
	// Samples lists the sample inputs of a flow session (empty for a sensor session).
	Samples(ctx context.Context, sessionID string) ([]AssistSample, error)
	// Cancel cancels the running turn; without one it returns the session unchanged.
	Cancel(ctx context.Context, sessionID string) (*store.AssistSession, error)
	// Close ends the session: stops the agent, removes the draft directory, marks it closed.
	Close(ctx context.Context, sessionID string) error
	// SensorCreated tells the service that sensor sensorID now exists. Open sessions that were authoring a new
	// sensor with that id become sessions on it (their target id is set), so they use its env keys and saved script.
	SensorCreated(ctx context.Context, sensorID string) error
	// FlowCreated is SensorCreated for flow sessions authoring a new flow.
	FlowCreated(ctx context.Context, flowID string) error
}

// DraftConflictError is UpdateDraft's hash mismatch (409 conflict).
type DraftConflictError struct {
	Expected, Current string
}

func (e *DraftConflictError) Error() string {
	return "the draft changed since it was read (hash " + e.Current + ", expected " + e.Expected + ")"
}
