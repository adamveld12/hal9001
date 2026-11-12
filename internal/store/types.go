package store

import (
	"encoding/json"
	"errors"
	"time"
)

// Errors returned by repository methods. Use errors.Is.
var (
	// ErrNotFound: no row with the given key.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict: the row exists but is not in the state the operation requires
	// (e.g. deciding an approval that is no longer pending).
	ErrConflict = errors.New("store: conflict")
	// ErrInvalid: the caller supplied a value outside an enum or a required field is empty.
	ErrInvalid = errors.New("store: invalid argument")
)

// Enums. The strings are the exact values stored and serialised.

type (
	FlowRunStatus    string
	FlowRunKind      string
	NodeRunStatus    string
	NodeDecision     string
	NodeType         string
	AgentStatus      string
	AgentPhase       string
	ResumedVia       string
	AgentMode        string
	ApproveStyle     string
	Policy           string
	ApprovalType     string
	ApprovalState    string
	DescriptionBy    string
	ApprovalOrigin   string
	ConversationKind string
	PermDecision     string
	PermDecidedBy    string
	Priority         string
	SensorStatus     string
	SensorOrigin     string
	SourceType       string
	AuthService      string
)

const (
	FlowRunQueued           FlowRunStatus = "queued"
	FlowRunRunning          FlowRunStatus = "running"
	FlowRunAwaitingApproval FlowRunStatus = "awaiting_approval"
	FlowRunSucceeded        FlowRunStatus = "succeeded"
	FlowRunFailed           FlowRunStatus = "failed"
	FlowRunKilled           FlowRunStatus = "killed"
	FlowRunInterrupted      FlowRunStatus = "interrupted"
	FlowRunCancelled        FlowRunStatus = "cancelled" // removed from the queue

	KindLive   FlowRunKind = "live"
	KindReplay FlowRunKind = "replay"
	KindTest   FlowRunKind = "test"
	// KindContinuation is a run started by an approved emit of a chat that continues an agent run (see Continuation).
	KindContinuation FlowRunKind = "continuation"

	NodeQueued           NodeRunStatus = "queued"
	NodeRunning          NodeRunStatus = "running"
	NodeAwaitingApproval NodeRunStatus = "awaiting_approval"
	NodeDone             NodeRunStatus = "done"
	NodeFailed           NodeRunStatus = "failed"
	NodeKilled           NodeRunStatus = "killed"
	NodeInterrupted      NodeRunStatus = "interrupted"
	NodeSkipped          NodeRunStatus = "skipped"
	NodeCancelled        NodeRunStatus = "cancelled" // removed from the queue

	DecisionEmitted NodeDecision = "emitted"
	DecisionDropped NodeDecision = "dropped"
	DecisionError   NodeDecision = "error"
	DecisionSkipped NodeDecision = "skipped"

	NodeCommand  NodeType = "command"
	NodeAgent    NodeType = "agent"
	NodeInbox    NodeType = "inbox"
	NodeApproval NodeType = "approval"

	AgentQueued           AgentStatus = "queued"
	AgentRunning          AgentStatus = "running"
	AgentAwaitingApproval AgentStatus = "awaiting_approval"
	AgentSucceeded        AgentStatus = "succeeded"
	AgentFailed           AgentStatus = "failed"
	AgentDenied           AgentStatus = "denied"
	AgentKilled           AgentStatus = "killed"
	AgentInterrupted      AgentStatus = "interrupted"
	AgentTimeout          AgentStatus = "timeout"

	PhasePropose AgentPhase = "propose"
	PhaseExecute AgentPhase = "execute"

	ResumedLoad       ResumedVia = "load"
	ResumedNewSession ResumedVia = "new_session"

	ModeAuto     AgentMode = "auto"
	ModeApprove  AgentMode = "approve"
	ModeReadonly AgentMode = "readonly"

	StyleProposal ApproveStyle = "proposal"
	StylePerTool  ApproveStyle = "per_tool"

	PolicyReadonly Policy = "readonly"
	PolicyGated    Policy = "gated"
	PolicyAuto     Policy = "auto"

	ApprovalProposal ApprovalType = "proposal"
	ApprovalToolCall ApprovalType = "tool_call"
	ApprovalRequest  ApprovalType = "request"

	DescByHal   DescriptionBy = "hal"
	DescByAgent DescriptionBy = "agent"

	OriginFlow          ApprovalOrigin = "flow"
	OriginChat          ApprovalOrigin = "chat"
	OriginContinuedChat ApprovalOrigin = "continued_chat"
	OriginMCP           ApprovalOrigin = "mcp"

	ConvChat ConversationKind = "chat"
	ConvFlow ConversationKind = "flow"

	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalDenied   ApprovalState = "denied"
	ApprovalExpired  ApprovalState = "expired"

	PermAllow     PermDecision = "allow"
	PermReject    PermDecision = "reject"
	PermCancelled PermDecision = "cancelled"
	PermExpired   PermDecision = "expired"

	DecidedByPolicy  PermDecidedBy = "policy"
	DecidedByUser    PermDecidedBy = "user"
	DecidedByTimeout PermDecidedBy = "timeout"

	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"

	SensorRunning     SensorStatus = "running"
	SensorSucceeded   SensorStatus = "succeeded"
	SensorFailed      SensorStatus = "failed"
	SensorInterrupted SensorStatus = "interrupted"

	OriginSchedule SensorOrigin = "schedule"
	OriginManual   SensorOrigin = "manual"
	OriginInject   SensorOrigin = "inject"

	SourceAgentRun SourceType = "agent_run"
	SourceNodeRun  SourceType = "node_run"
	// SourceMCP is a note an external MCP client posted (send_note, docs/mcp.md); it belongs to no run.
	SourceMCP SourceType = "mcp"

	ServiceUI      AuthService = "ui"
	ServiceAPI     AuthService = "api"
	ServiceApprove AuthService = "approve"
	ServiceAdmin   AuthService = "admin"
)

func (s FlowRunStatus) valid() bool {
	switch s {
	case FlowRunQueued, FlowRunRunning, FlowRunAwaitingApproval, FlowRunSucceeded, FlowRunFailed, FlowRunKilled, FlowRunInterrupted, FlowRunCancelled:
		return true
	}
	return false
}

// Terminal reports whether the status is final (no further transitions except pruning).
func (s FlowRunStatus) Terminal() bool {
	switch s {
	case FlowRunSucceeded, FlowRunFailed, FlowRunKilled, FlowRunInterrupted, FlowRunCancelled:
		return true
	}
	return false
}

func (k FlowRunKind) valid() bool {
	return k == KindLive || k == KindReplay || k == KindTest || k == KindContinuation
}

func (s NodeRunStatus) valid() bool {
	switch s {
	case NodeQueued, NodeRunning, NodeAwaitingApproval, NodeDone, NodeFailed, NodeKilled, NodeInterrupted, NodeSkipped, NodeCancelled:
		return true
	}
	return false
}

func (d NodeDecision) valid() bool {
	switch d {
	case "", DecisionEmitted, DecisionDropped, DecisionError, DecisionSkipped:
		return true
	}
	return false
}

func (t NodeType) valid() bool {
	return t == NodeCommand || t == NodeAgent || t == NodeInbox || t == NodeApproval
}

func (s AgentStatus) valid() bool {
	switch s {
	case AgentQueued, AgentRunning, AgentAwaitingApproval, AgentSucceeded, AgentFailed, AgentDenied, AgentKilled, AgentInterrupted, AgentTimeout:
		return true
	}
	return false
}

func (p Priority) valid() bool {
	return p == PriorityLow || p == PriorityNormal || p == PriorityHigh || p == PriorityUrgent
}

func (s SensorStatus) valid() bool {
	return s == SensorRunning || s == SensorSucceeded || s == SensorFailed || s == SensorInterrupted
}

// Content is a possibly large text payload. Values up to SpillThreshold bytes
// are stored inline in the row; larger ones are written to the blob store and
// the row keeps only the ref.
//
// When reading: Text holds the inline value; for a spilled payload Text is
// empty and Ref is set (use Store.ReadContent). List and tree queries are
// "slim": they leave Text empty even for inline values and report only Size.
// Use Get* for full inline content.
//
// When writing: set Text to have the store spill automatically, or set Ref and
// Size to point at a blob you wrote yourself (Store.PutBlob). A Content read
// from the store can be written back unchanged; unhydrated content is
// preserved, never blanked.
type Content struct {
	Text string `json:"text,omitempty"`
	Ref  string `json:"ref,omitempty"`
	Size int64  `json:"size"`
}

// Inline returns a Content holding s.
func Inline(s string) Content { return Content{Text: s, Size: int64(len(s))} }

// Change describes one committed row change, delivered to the notifier.
type Change struct {
	Resource string `json:"resource"`
	ID       string `json:"id"`
	Op       string `json:"op"`
}

// Resource names used in Change.Resource.
const (
	ResSensorRun         = "sensor_run"
	ResSensorState       = "sensor_state"
	ResEvent             = "event"
	ResFlowVersion       = "flow_version"
	ResFlowRun           = "flow_run"
	ResNodeRun           = "node_run"
	ResAgentRun          = "agent_run"
	ResAgentEvent        = "agent_event" // id is "<agent_run_id>/<seq>"
	ResSandboxSession    = "sandbox_session"
	ResSandboxLog        = "sandbox_log" // id is the entry id
	ResPermissionRequest = "permission_request"
	ResApproval          = "approval"
	ResInboxItem         = "inbox_item"
	ResNotification      = "notification"
	ResAuthEvent         = "auth_event"
	ResAssistSession     = "assist_session"
	ResAssistMessage     = "assist_message"
	ResSession           = "session"
	ResWebAuthn          = "webauthn_credential"
	ResSetting           = "setting"
)

// Change ops.
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpDelete = "delete"
)

// Page is one page of a cursor-paginated list. Pass NextCursor as the next
// request's Cursor; it is empty on the last page. Cursors are record ULIDs.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// ListOpts are the pagination controls shared by every List method.
// Default order is newest first (id descending) and Cursor is the id of the
// last item of the previous page. With Oldest, order is ascending and Cursor
// excludes ids <= Cursor. Limit defaults to 50 and is capped at 500.
type ListOpts struct {
	Cursor string
	Limit  int
	Oldest bool
}

// TimeRange filters on the table's primary timestamp: Since inclusive, Until exclusive.
type TimeRange struct {
	Since time.Time
	Until time.Time
}

// Record structs. JSON field names follow REQUIREMENTS.md §9 column names.

// SensorRun is one poll of a sensor.
type SensorRun struct {
	ID         string       `json:"id"`
	SensorID   string       `json:"sensor_id"`
	Origin     SensorOrigin `json:"origin"`
	Status     SensorStatus `json:"status"`
	Baseline   bool         `json:"baseline"`
	StartedAt  time.Time    `json:"started_at"`
	EndedAt    *time.Time   `json:"ended_at,omitempty"`
	DurationMS *int64       `json:"duration_ms,omitempty"`
	ExitCode   *int         `json:"exit_code,omitempty"`
	Error      string       `json:"error,omitempty"`
	ItemCount  int          `json:"item_count"`
	NewCount   int          `json:"new_count"`
	Stdout     Content      `json:"stdout"`
	Stderr     Content      `json:"stderr"`
	// MCPCalls are the tool calls the script made with its run token (docs/mcp-servers.md), in order.
	MCPCalls []SensorMCPCall `json:"mcp_calls,omitempty"`
}

// SensorMCPCall is one `hal9001 mcp call` of a sensor run.
type SensorMCPCall struct {
	Server     string    `json:"server"`
	Tool       string    `json:"tool"`
	At         time.Time `json:"at"`
	DurationMS int64     `json:"duration_ms"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
}

// SensorState is per-sensor bookkeeping; LastSuccessAt feeds HAL_LAST_SUCCESS_AT.
type SensorState struct {
	SensorID            string     `json:"sensor_id"`
	LastRunID           string     `json:"last_run_id,omitempty"`
	LastRunAt           *time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	// PausedAt is set while the sensor is paused (never polled by the
	// scheduler); PausedBy is the principal that paused it.
	PausedAt *time.Time `json:"paused_at,omitempty"`
	PausedBy string     `json:"paused_by,omitempty"`
	// ResetAt is when the sensor's dedup state was last reset (ResetSensor);
	// ResetBy is the principal that did it.
	ResetAt   *time.Time `json:"reset_at,omitempty"`
	ResetBy   string     `json:"reset_by,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Event is one deduplicated sensor item. Immutable once created.
type Event struct {
	ID          string    `json:"id"`
	SensorID    string    `json:"sensor_id"`
	SensorRunID string    `json:"sensor_run_id,omitempty"`
	DedupKey    string    `json:"dedup_key"`
	Payload     Content   `json:"payload"`
	CreatedAt   time.Time `json:"created_at"`
}

// FlowVersion is a snapshot of a flow's YAML, keyed by content hash.
type FlowVersion struct {
	FlowID    string    `json:"flow_id"`
	Hash      string    `json:"hash"`
	YAML      string    `json:"yaml"`
	FirstSeen time.Time `json:"first_seen"`
}

// FlowRun is one execution of one flow for one event.
type FlowRun struct {
	ID        string        `json:"id"`
	FlowID    string        `json:"flow_id"`
	FlowHash  string        `json:"flow_hash"`
	EventID   string        `json:"event_id,omitempty"`
	Kind      FlowRunKind   `json:"kind"`
	ReplayOf  string        `json:"replay_of,omitempty"`
	DryRun    bool          `json:"dry_run"`
	Status    FlowRunStatus `json:"status"`
	Error     string        `json:"error,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	StartedAt *time.Time    `json:"started_at,omitempty"`
	EndedAt   *time.Time    `json:"ended_at,omitempty"`
	// CancelledBy is the principal who removed the run from the queue (status cancelled).
	CancelledBy string `json:"cancelled_by,omitempty"`
	// Continuation links a run of kind continuation to what it continues; nil for the other kinds.
	Continuation *Continuation `json:"continuation,omitempty"`

	// FlowYAML is write-only input. When set on CreateFlowRun, the flow version
	// (FlowID, FlowHash) is inserted in the same transaction if missing.
	FlowYAML string `json:"-"`
}

// Continuation says where a continuation run came from: the flow run whose agent node the user continued in chat
// (OfRunID), the conversation (the assist session id), the agent node and the port that were emitted, and the
// approval of the emit tool call that started the run. The original run is not changed; the link lives on the new run.
type Continuation struct {
	OfRunID        string `json:"of_run_id"`
	ConversationID string `json:"conversation_id"`
	NodeID         string `json:"node_id"`
	Port           string `json:"port"`
	ApprovalID     string `json:"approval_id"`
}

// NodeRun is one execution of one node within a flow run.
type NodeRun struct {
	ID              string        `json:"id"`
	FlowRunID       string        `json:"flow_run_id"`
	NodeID          string        `json:"node_id"`
	NodeType        NodeType      `json:"node_type"`
	ParentNodeRunID string        `json:"parent_node_run_id,omitempty"`
	Status          NodeRunStatus `json:"status"`
	Decision        NodeDecision  `json:"decision,omitempty"`
	Reason          string        `json:"reason,omitempty"`
	Ports           []string      `json:"ports"`
	OutputCount     int           `json:"output_count"`
	Lang            string        `json:"lang,omitempty"`
	Script          string        `json:"script,omitempty"`
	Argv            []string      `json:"argv"`
	EnvKeys         []string      `json:"env_keys"`
	Stdin           Content       `json:"stdin"`
	Stdout          Content       `json:"stdout"`
	Stderr          Content       `json:"stderr"`
	ExitCode        *int          `json:"exit_code,omitempty"`
	Error           string        `json:"error,omitempty"`
	QueuedAt        time.Time     `json:"queued_at"`
	StartedAt       *time.Time    `json:"started_at,omitempty"`
	EndedAt         *time.Time    `json:"ended_at,omitempty"`
	DurationMS      *int64        `json:"duration_ms,omitempty"`
}

// AgentRun is one agent session phase behind an agent node run.
type AgentRun struct {
	ID        string `json:"id"`
	NodeRunID string `json:"node_run_id,omitempty"`
	// AssistSessionID is set instead of NodeRunID when the run is one turn of an assist session.
	AssistSessionID string          `json:"assist_session_id,omitempty"`
	PrevAgentRunID  string          `json:"prev_agent_run_id,omitempty"`
	Runtime         string          `json:"runtime"`
	AgentInfo       json.RawMessage `json:"agent_info,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	Mode            AgentMode       `json:"mode"`
	ApproveStyle    ApproveStyle    `json:"approve_style,omitempty"`
	Policy          Policy          `json:"policy"`
	Phase           AgentPhase      `json:"phase"`
	Model           string          `json:"model,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	ResumedVia      ResumedVia      `json:"resumed_via,omitempty"`
	Cwd             string          `json:"cwd,omitempty"`
	WorktreePath    string          `json:"worktree_path,omitempty"`
	Prompt          Content         `json:"prompt"`
	Argv            []string        `json:"argv"`
	Status          AgentStatus     `json:"status"`
	StopReason      string          `json:"stop_reason,omitempty"`
	Error           string          `json:"error,omitempty"`
	Result          Content         `json:"result"`
	RawJSONRPC      Content         `json:"raw_jsonrpc"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	EndedAt         *time.Time      `json:"ended_at,omitempty"`
}

// AgentEvent is one session/update (or other transcript entry), ordered by Seq within a run.
type AgentEvent struct {
	AgentRunID string    `json:"agent_run_id"`
	Seq        int64     `json:"seq"`
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Payload    Content   `json:"payload"`
}

// PermissionRequest is one session/request_permission and how it was decided.
// Decision is empty while undecided.
type PermissionRequest struct {
	ID          string          `json:"id"`
	AgentRunID  string          `json:"agent_run_id"`
	ToolCallID  string          `json:"tool_call_id,omitempty"`
	ToolKind    string          `json:"tool_kind,omitempty"`
	Title       string          `json:"title,omitempty"`
	RawInput    Content         `json:"raw_input"`
	Options     json.RawMessage `json:"options,omitempty"`
	Decision    PermDecision    `json:"decision,omitempty"`
	DecidedBy   PermDecidedBy   `json:"decided_by,omitempty"`
	MatchedRule string          `json:"matched_rule,omitempty"`
	Reason      string          `json:"reason,omitempty"`
	RequestedAt time.Time       `json:"requested_at"`
	DecidedAt   *time.Time      `json:"decided_at,omitempty"`
}

// MaxApprovalDescription is the longest approval description, in characters.
const MaxApprovalDescription = 600

// Approval is a pending or decided approval-queue entry. Every approval has the same shape: what is asked (Title,
// Description), what will actually happen (Details), where it came from (Origin) and, when an agent run raised it, the
// conversation it belongs to. Request holds the request machinery only (RequestApproval).
type Approval struct {
	ID                  string         `json:"id"`
	AgentRunID          string         `json:"agent_run_id,omitempty"` // lineage: the flow agent run or chat turn run that raised it
	NodeRunID           string         `json:"node_run_id,omitempty"`  // set for a Request approval node or a node gate
	Request             string         `json:"request,omitempty"`      // JSON of a RequestApproval; request approvals only
	Type                ApprovalType   `json:"type"`
	PermissionRequestID string         `json:"permission_request_id,omitempty"`
	Title               string         `json:"title"`
	Description         string         `json:"description"`
	DescriptionBy       DescriptionBy  `json:"description_by"`
	Priority            Priority       `json:"priority"`
	Origin              ApprovalOrigin `json:"origin"`
	OriginLabel         string         `json:"origin_label,omitempty"`
	OriginClient        string         `json:"origin_client,omitempty"` // the MCP client of an mcp approval
	// ConversationID and ConversationKind link an approval raised by an agent run to its conversation: a chat
	// session (id = session id) or a flow agent run (id = agent run id). Both are empty without an agent run.
	ConversationID   string           `json:"conversation_id,omitempty"`
	ConversationKind ConversationKind `json:"conversation_kind,omitempty"`
	// Anchor is the message id of the point in the conversation where the approval was raised.
	Anchor string `json:"anchor,omitempty"`
	// HeldNote is set on an approval raised held (the conversation reached its steering cap): the note says why.
	HeldNote  string          `json:"held_note,omitempty"`
	Details   ApprovalDetails `json:"details"`
	Proposal  Content         `json:"proposal_md"`
	Status    ApprovalState   `json:"status"`
	Comment   string          `json:"comment,omitempty"`    // the approver's steering text
	DecidedBy string          `json:"decided_by,omitempty"` // principal, or "system" for expiry
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt *time.Time      `json:"expires_at,omitempty"`
	DecidedAt *time.Time      `json:"decided_at,omitempty"`
}

// Held reports whether the approval was raised held.
func (a *Approval) Held() bool { return a.HeldNote != "" }

// ApprovalDetails is what will actually happen, as hal knows it: shown next to the description.
type ApprovalDetails struct {
	BodyMD string         `json:"body_md"`
	Fields []RequestField `json:"fields"`
	// Command is the shell command or the tool-call explanation, extracted from nested arguments.
	Command string         `json:"command"`
	Tool    string         `json:"tool"`
	Diffs   []ApprovalDiff `json:"diffs"`
	DryRun  bool           `json:"dry_run"`
}

// ApprovalDiff is one change a tool call would make. Old is empty for a new file.
type ApprovalDiff struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// InboxLink is one entry of InboxItem.Links.
type InboxLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// InboxItem is a structured note for the user.
type InboxItem struct {
	ID             string      `json:"id"`
	SourceType     SourceType  `json:"source_type"`
	FlowRunID      string      `json:"flow_run_id,omitempty"`
	NodeRunID      string      `json:"node_run_id,omitempty"`
	AgentRunID     string      `json:"agent_run_id,omitempty"`
	Title          string      `json:"title"`
	Priority       Priority    `json:"priority"`
	SummaryMD      string      `json:"summary_md,omitempty"`
	SuggestedReply string      `json:"suggested_reply,omitempty"`
	Links          []InboxLink `json:"links"`
	ParseError     bool        `json:"parse_error"`
	Read           bool        `json:"read"`
	Starred        bool        `json:"starred"`
	Archived       bool        `json:"archived"`
	DryRun         bool        `json:"dry_run"`
	CreatedAt      time.Time   `json:"created_at"`
	// Client names the external MCP client of a SourceMCP item, ThreadKey is the grouping key it passed.
	Client    string `json:"client,omitempty"`
	ThreadKey string `json:"thread_key,omitempty"`
}

// ModuleResult is one auth module's outcome inside an AuthEvent.
type ModuleResult struct {
	Module  string `json:"module"`
	Control string `json:"control,omitempty"`
	Result  string `json:"result"`
	Reason  string `json:"reason,omitempty"`
}

// AuthEvent records one authentication decision.
type AuthEvent struct {
	ID         string         `json:"id"`
	TS         time.Time      `json:"ts"`
	Action     string         `json:"action,omitempty"` // e.g. authenticate, login, logout; free text owned by the auth layer
	Service    AuthService    `json:"service,omitempty"`
	Endpoint   string         `json:"endpoint,omitempty"`
	Results    []ModuleResult `json:"results"`
	Outcome    string         `json:"outcome"`
	Reason     string         `json:"reason,omitempty"`
	Principal  string         `json:"principal,omitempty"`
	RemoteAddr string         `json:"remote_addr,omitempty"`
	Host       string         `json:"host,omitempty"`
	SessionID  string         `json:"session_id,omitempty"` // hashed session id
}

// Session is a server-side login session. ID is the hex SHA-256 of the cookie
// secret, computed by the caller; the store never sees the raw secret.
type Session struct {
	ID         string        `json:"id"`
	Principal  string        `json:"principal"`
	Modules    []string      `json:"modules"`
	Services   []AuthService `json:"services"`
	Host       string        `json:"host,omitempty"`
	RemoteAddr string        `json:"remote_addr,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	LastSeenAt time.Time     `json:"last_seen_at"`
	ExpiresAt  time.Time     `json:"expires_at"`
	RevokedAt  *time.Time    `json:"revoked_at,omitempty"`
}

// WebAuthnCredential is a stored passkey. Data carries any extra serialised
// authenticator fields (AAGUID, transports, flags) the auth layer needs.
type WebAuthnCredential struct {
	ID         string          `json:"id"` // base64url credential id
	RPID       string          `json:"rp_id"`
	Principal  string          `json:"principal,omitempty"`
	PublicKey  []byte          `json:"public_key"`
	SignCount  uint32          `json:"sign_count"`
	Label      string          `json:"label,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	LastUsedAt *time.Time      `json:"last_used_at,omitempty"`
}

// KillSwitch is the persisted kill-switch state (settings key "kill_switch").
type KillSwitch struct {
	Paused      bool      `json:"paused"`
	KillRunning bool      `json:"kill_running"`
	Since       time.Time `json:"since"`
	By          string    `json:"by,omitempty"`
}

// RequestApproval is the request machinery of a request approval (Approval.Request, stored as JSON): what the approver
// may edit, the edits applied, what an expiry does and how the approval is tied to its raiser. What is asked and what
// will happen live in the Approval itself (Title, Description, Details).
type RequestApproval struct {
	// Editable are the input paths the approver may edit, with their values when the approval was raised.
	Editable []RequestEditable `json:"editable,omitempty"`
	// Edits is set when the approval was approved with edits.
	Edits map[string]RequestEdit `json:"edits,omitempty"`
	// OnTimeout is what an expiry does: denied, approved or error. Empty without a timeout.
	OnTimeout string `json:"on_timeout,omitempty"`
	// Principal is the authenticated caller of the MCP request that raised an mcp approval.
	Principal string `json:"principal,omitempty"`
	// Detached marks an approval that outlives the MCP call that raised it (request_approval): it is decided and
	// expires on its own, survives a daemon restart, and nobody has to be waiting on it.
	Detached bool `json:"detached,omitempty"`
	// Gate marks the approval a node with requires_approval raised before running: approving runs the node, denying
	// sends its input down port denied.
	Gate bool `json:"gate,omitempty"`
}

// RequestEditable is one input path of a request approval the approver may edit, with its current value.
type RequestEditable struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// RequestEdit is one applied edit.
type RequestEdit struct {
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// RequestField is one row of the key/value table of a request approval.
type RequestField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
