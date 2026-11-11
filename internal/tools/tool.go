package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"hal9001/internal/sandbox"
)

// Tool is one tool the agent can call.
type Tool struct {
	// Name is lowercase snake_case, unique within a session.
	Name        string
	Description string
	// InputSchema is a JSON Schema of type object describing the arguments.
	InputSchema map[string]any
	// Approval reports whether a call with these arguments needs a user's approval first, and why. A nil Approval
	// means the tool never needs one. It is called before Run and must not have side effects.
	Approval func(args json.RawMessage) (needs bool, reason string)
	// Policy, when set, is used instead of Approval and can also deny a call outright. Same rules as Approval.
	Policy func(args json.RawMessage) (Verdict, string)
	// Run executes the tool. A returned error is reported to the agent as a tool error.
	Run func(ctx context.Context, c Call) (Result, error)
	// OnBlocked, when set, is told about every call that was not run: refused by policy, denied or left unanswered by
	// a user, or held back because the run is read-only.
	OnBlocked func(Blocked)
}

// Blocked describes a call that was not run.
type Blocked struct {
	Tool string
	Args json.RawMessage
	// By is why: BlockedPolicy, BlockedUser, BlockedExpired, BlockedReadOnly or BlockedError.
	By string
	// Reason is the policy's or approval's reason, or the error.
	Reason string
	// Comment is the approver's comment.
	Comment string
}

// Blocked.By values.
const (
	BlockedPolicy   = "policy"    // denied by the tool's policy
	BlockedUser     = "user"      // denied by the user (Comment may say why)
	BlockedExpired  = "expired"   // nobody decided before the approval timeout
	BlockedReadOnly = "read_only" // needs approval, but the run is read-only
	BlockedError    = "error"     // the approval wait failed (the run was cancelled)
)

// Verdict is the outcome of a tool's Policy for one call.
type Verdict int

const (
	// VerdictAllow runs the call without asking.
	VerdictAllow Verdict = iota
	// VerdictApprove holds the call for a user's approval.
	VerdictApprove
	// VerdictDeny refuses the call: it is recorded as denied by policy and the agent gets a tool error.
	VerdictDeny
)

// Call is one invocation of a tool.
type Call struct {
	Name string
	Args json.RawMessage
	// Info is the state of the run the call belongs to, as of the call.
	Info RunInfo
	// Now is the session clock's current time.
	Now time.Time
	// Sandbox is the run's sandbox session when the node has one (an extension supplied it), else nil.
	Sandbox sandbox.Session
	// Approval says how the call was allowed: "auto: <reason>" or "approved by user".
	Approval string
	// RecordID is the id of the trace record (permission request) the host made for the call; empty when none was made.
	RecordID string
}

// Result is what a tool returns to the agent.
type Result struct {
	Text string
}

// RunInfo is the state of the run a call belongs to. For an assist session it describes the current turn.
type RunInfo struct {
	AgentRunID      string
	NodeRunID       string
	FlowRunID       string
	FlowID          string
	NodeID          string
	AssistSessionID string
	// RunKind is the flow run kind (event, manual, replay, ...); empty for assist.
	RunKind string
	DryRun  bool
	// SensorID, EventID and PolledAt describe the triggering event.
	SensorID string
	EventID  string
	PolledAt time.Time
	// Input is the node's input payload and Event the original event payload (JSON); nil when the run has none.
	Input json.RawMessage
	Event json.RawMessage
}

// ToolInfo is the listing of one tool.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Registry is a set of tools by name. It is not safe for concurrent mutation.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds tools. It fails on an invalid or duplicate name, a missing Run, or a schema that is not an object.
func (r *Registry) Register(ts ...Tool) error {
	for _, t := range ts {
		switch {
		case !nameRE.MatchString(t.Name):
			return fmt.Errorf("tools: invalid tool name %q", t.Name)
		case t.Run == nil:
			return fmt.Errorf("tools: tool %q has no Run", t.Name)
		case t.InputSchema == nil || t.InputSchema["type"] != "object":
			return fmt.Errorf("tools: tool %q: InputSchema must be a JSON Schema of type object", t.Name)
		}
		if _, dup := r.tools[t.Name]; dup {
			return fmt.Errorf("tools: tool %q is already registered", t.Name)
		}
		r.tools[t.Name] = t
	}
	return nil
}

// Get returns the named tool.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the tool names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// List returns the tool listings sorted by name.
func (r *Registry) List() []ToolInfo {
	out := make([]ToolInfo, 0, len(r.tools))
	for _, n := range r.Names() {
		t := r.tools[n]
		out = append(out, ToolInfo{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// Clone returns an independent copy.
func (r *Registry) Clone() *Registry {
	c := NewRegistry()
	for n, t := range r.tools {
		c.tools[n] = t
	}
	return c
}

// ErrUnknownTool is returned by Session.Call for a name that is not in the session's tool set.
var ErrUnknownTool = errors.New("unknown tool")
