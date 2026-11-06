package e2e

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// The structs below decode only the fields the tests assert on; the full shapes
// are checked against api/openapi.yaml on every response by Daemon.Do.

type TextBlob struct {
	Text      string `json:"text"`
	Size      int    `json:"size"`
	Truncated bool   `json:"truncated"`
	Blob      string `json:"blob"`
}

type SensorRun struct {
	ID        string   `json:"id"`
	SensorID  string   `json:"sensor_id"`
	Trigger   string   `json:"trigger"`
	Status    string   `json:"status"`
	Baseline  bool     `json:"baseline"`
	ExitCode  *int     `json:"exit_code"`
	ItemCount *int     `json:"item_count"`
	NewCount  *int     `json:"new_count"`
	Stdout    TextBlob `json:"stdout"`
	Stderr    TextBlob `json:"stderr"`
	Error     string   `json:"error"`
}

type Event struct {
	ID       string          `json:"id"`
	SensorID string          `json:"sensor_id"`
	DedupKey string          `json:"dedup_key"`
	Payload  json.RawMessage `json:"payload"`
	Injected bool            `json:"injected"`
}

type FlowRun struct {
	ID       string `json:"id"`
	FlowID   string `json:"flow_id"`
	FlowHash string `json:"flow_hash"`
	EventID  string `json:"event_id"`
	Kind     string `json:"kind"`
	ReplayOf string `json:"replay_of"`
	DryRun   bool   `json:"dry_run"`
	Status   string `json:"status"`
	Error    string `json:"error"`

	CancelledBy string `json:"cancelled_by"`
}

type NodeRun struct {
	ID              string   `json:"id"`
	FlowRunID       string   `json:"flow_run_id"`
	NodeID          string   `json:"node_id"`
	NodeType        string   `json:"node_type"`
	ParentNodeRunID string   `json:"parent_node_run_id"`
	Status          string   `json:"status"`
	Decision        string   `json:"decision"`
	Reason          string   `json:"reason"`
	Error           string   `json:"error"`
	Ports           []string `json:"ports"`
	EmittedCount    int      `json:"emitted_count"`
	Argv            []string `json:"argv"`
	EnvKeys         []string `json:"env_keys"`
	Stdin           TextBlob `json:"stdin"`
	Stdout          TextBlob `json:"stdout"`
	Stderr          TextBlob `json:"stderr"`
	ExitCode        *int     `json:"exit_code"`
	DurationMS      *int     `json:"duration_ms"`
	StartedAt       string   `json:"started_at"`
}

type NodeRunDetail struct {
	NodeRun
	AgentRuns []AgentRun `json:"agent_runs"`
}

type FlowRunDetail struct {
	FlowRun  FlowRun   `json:"flow_run"`
	NodeRuns []NodeRun `json:"node_runs"`
}

// Node returns the single node run of nodeID, failing the test otherwise.
func (d *Daemon) Node(det FlowRunDetail, nodeID string) NodeRun {
	d.t.Helper()
	var found []NodeRun
	for _, n := range det.NodeRuns {
		if n.NodeID == nodeID {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		d.t.Fatalf("flow run %s has %d node runs of %q, want 1\n%s", det.FlowRun.ID, len(found), nodeID, describeRun(det))
	}
	return found[0]
}

func describeRun(det FlowRunDetail) string {
	var b strings.Builder
	fr := det.FlowRun
	fmt.Fprintf(&b, "flow run %s flow=%s kind=%s status=%s dry=%v error=%q\n", fr.ID, fr.FlowID, fr.Kind, fr.Status, fr.DryRun, fr.Error)
	for _, n := range det.NodeRuns {
		fmt.Fprintf(&b, "  node %s status=%s decision=%s error=%q reason=%q ports=%v\n", n.NodeID, n.Status, n.Decision, n.Error, n.Reason, n.Ports)
	}
	return b.String()
}

type InjectResult struct {
	Event        Event     `json:"event"`
	Deduplicated bool      `json:"deduplicated"`
	DedupKey     string    `json:"dedup_key"`
	FlowRuns     []FlowRun `json:"flow_runs"`
}

type AgentRun struct {
	ID             string          `json:"id"`
	NodeRunID      string          `json:"node_run_id"`
	FlowRunID      string          `json:"flow_run_id"`
	Runtime        string          `json:"runtime"`
	Mode           string          `json:"mode"`
	ApproveStyle   string          `json:"approve_style"`
	Policy         string          `json:"policy"`
	Phase          string          `json:"phase"`
	SessionID      string          `json:"session_id"`
	ResumedVia     string          `json:"resumed_via"`
	ResumeFallback string          `json:"resume_fallback"`
	Cwd            string          `json:"cwd"`
	RenderedPrompt string          `json:"rendered_prompt"`
	Argv           []string        `json:"argv"`
	Status         string          `json:"status"`
	StopReason     string          `json:"stop_reason"`
	FinalMessage   string          `json:"final_message"`
	Result         json.RawMessage `json:"result"`
	ParseError     bool            `json:"parse_error"`
	Error          string          `json:"error"`
	AgentInfo      *struct {
		Name string `json:"name"`
	} `json:"agent_info"`
}

type AgentRunDetail struct {
	AgentRun
	PhaseRunIDs []string   `json:"phase_run_ids"`
	Approvals   []Approval `json:"approvals"`
}

type AgentEvent struct {
	Seq        int             `json:"seq"`
	Kind       string          `json:"kind"`
	ToolCallID string          `json:"tool_call_id"`
	Payload    json.RawMessage `json:"payload"`
}

type PermissionRequest struct {
	ID          string          `json:"id"`
	ToolCallID  string          `json:"tool_call_id"`
	ToolKind    string          `json:"tool_kind"`
	Title       string          `json:"title"`
	RawInput    json.RawMessage `json:"raw_input"`
	Decision    string          `json:"decision"`
	DecidedBy   string          `json:"decided_by"`
	MatchedRule string          `json:"matched_rule"`
	Reason      string          `json:"reason"`
	ApprovalID  string          `json:"approval_id"`
}

type Approval struct {
	ID                string             `json:"id"`
	AgentRunID        string             `json:"agent_run_id"`
	NodeRunID         string             `json:"node_run_id"`
	FlowRunID         string             `json:"flow_run_id"`
	FlowID            string             `json:"flow_id"`
	NodeID            string             `json:"node_id"`
	EventID           string             `json:"event_id"`
	Runtime           string             `json:"runtime"`
	Type              string             `json:"type"`
	PermissionRequest *PermissionRequest `json:"permission_request"`
	ProposalMD        string             `json:"proposal_md"`
	Status            string             `json:"status"`
	Comment           string             `json:"comment"`
	CancelRun         *bool              `json:"cancel_run"`
	DecidedBy         string             `json:"decided_by"`
	ExpiresAt         string             `json:"expires_at"`
}

type InboxItem struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	FlowRunID      string `json:"flow_run_id"`
	NodeRunID      string `json:"node_run_id"`
	AgentRunID     string `json:"agent_run_id"`
	FlowID         string `json:"flow_id"`
	NodeID         string `json:"node_id"`
	EventID        string `json:"event_id"`
	Title          string `json:"title"`
	Priority       string `json:"priority"`
	SummaryMD      string `json:"summary_md"`
	SuggestedReply string `json:"suggested_reply"`
	Links          []struct {
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"links"`
	Read       bool `json:"read"`
	Starred    bool `json:"starred"`
	Archived   bool `json:"archived"`
	DryRun     bool `json:"dry_run"`
	ParseError bool `json:"parse_error"`
}

type KillSwitch struct {
	Paused      bool   `json:"paused"`
	KillRunning bool   `json:"kill_running"`
	By          string `json:"by"`
	Reason      string `json:"reason"`
}

type Status struct {
	KillSwitch       KillSwitch `json:"kill_switch"`
	RunningFlowRuns  int        `json:"running_flow_runs"`
	QueuedFlowRuns   int        `json:"queued_flow_runs"`
	RunningAgentRuns int        `json:"running_agent_runs"`
	QueuedAgentRuns  int        `json:"queued_agent_runs"`
	PendingApprovals int        `json:"pending_approvals"`
	UnreadInbox      int        `json:"unread_inbox"`
}

type FlowDetail struct {
	ID          string `json:"id"`
	Hash        string `json:"hash"`
	YAML        string `json:"yaml"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"diagnostics"`
	Flow *struct {
		Enabled bool `json:"enabled"`
		Nodes   []struct {
			ID     string `json:"id"`
			Script string `json:"script"`
		} `json:"nodes"`
	} `json:"flow"`
}

// ---- helpers ----

// Inject posts an item for sensor and returns the result.
func (d *Daemon) Inject(sensor string, item any) InjectResult {
	d.t.Helper()
	var res InjectResult
	d.Post("/api/events?sensor_id="+url.QueryEscape(sensor), item).Want(200, 201).JSON(&res)
	return res
}

// InjectRun injects an item and returns the single flow run it created.
func (d *Daemon) InjectRun(sensor string, item any) string {
	d.t.Helper()
	res := d.Inject(sensor, item)
	if res.Deduplicated || len(res.FlowRuns) != 1 {
		d.t.Fatalf("inject created %d flow runs (deduplicated=%v), want exactly one", len(res.FlowRuns), res.Deduplicated)
	}
	return res.FlowRuns[0].ID
}

// GetRun fetches a flow run, long-polling up to wait.
func (d *Daemon) GetRun(id string, wait time.Duration) FlowRunDetail {
	d.t.Helper()
	path := "/api/flow-runs/" + id
	if wait > 0 {
		path += "?wait=" + wait.String()
	}
	var det FlowRunDetail
	d.Get(path).Want(200).JSON(&det)
	return det
}

// WaitRun long-polls (the `?wait=` contract) until the flow run has one of the
// wanted statuses. A different terminal status fails the test; a run parked in
// awaiting_approval is polled until it moves on.
func (d *Daemon) WaitRun(id string, want ...string) FlowRunDetail {
	d.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		det := d.GetRun(id, 10*time.Second)
		for _, w := range want {
			if det.FlowRun.Status == w {
				return det
			}
		}
		switch det.FlowRun.Status {
		case "succeeded", "failed", "killed", "interrupted":
			d.t.Fatalf("flow run %s ended %s, want %v\n%s", id, det.FlowRun.Status, want, describeRun(det))
		case "awaiting_approval":
			// ?wait= returns at once for a parked run; a decision that was just
			// posted may not have resumed it yet.
			time.Sleep(20 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("flow run %s still %s after 60s, want %v\n%s", id, det.FlowRun.Status, want, describeRun(det))
		}
	}
}

// RunOf injects item and waits for its single flow run to succeed.
func (d *Daemon) RunOf(sensor string, item any) FlowRunDetail {
	d.t.Helper()
	return d.WaitRun(d.InjectRun(sensor, item), "succeeded")
}

// Poll runs the sensor now and returns the finished sensor run.
func (d *Daemon) Poll(sensor string) SensorRun {
	d.t.Helper()
	var sr SensorRun
	d.Post("/api/sensors/"+sensor+"/poll", nil).Want(202).JSON(&sr)
	d.Get("/api/sensor-runs/" + sr.ID + "?wait=30s").Want(200).JSON(&sr)
	if sr.Status == "running" {
		d.t.Fatalf("sensor run %s still running after wait", sr.ID)
	}
	return sr
}

// Events lists the events of a sensor, newest first.
func (d *Daemon) Events(sensor string) []Event {
	d.t.Helper()
	var l struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	d.Get("/api/events?limit=200&sensor_id=" + sensor).Want(200).JSON(&l)
	out := make([]Event, 0, len(l.Items))
	for _, it := range l.Items {
		var ev struct {
			Event Event `json:"event"`
		}
		d.Get("/api/events/" + it.ID).Want(200).JSON(&ev)
		out = append(out, ev.Event)
	}
	return out
}

// Inbox lists every non-archived inbox item, newest first.
func (d *Daemon) Inbox() []InboxItem {
	d.t.Helper()
	var l struct {
		Items []InboxItem `json:"items"`
	}
	d.Get("/api/inbox?limit=200&sort=time&archived=any").Want(200).JSON(&l)
	return l.Items
}

// InboxByTitle indexes the inbox by title.
func (d *Daemon) InboxByTitle() map[string]InboxItem {
	d.t.Helper()
	m := map[string]InboxItem{}
	for _, it := range d.Inbox() {
		m[it.Title] = it
	}
	return m
}

// Approvals lists approvals with the given status filter ("" = all), oldest first.
func (d *Daemon) Approvals(status string) []Approval {
	d.t.Helper()
	path := "/api/approvals?limit=200"
	if status != "" {
		path += "&status=" + status
	}
	var l struct {
		Items []Approval `json:"items"`
	}
	d.Get(path).Want(200).JSON(&l)
	for i, j := 0, len(l.Items)-1; i < j; i, j = i+1, j-1 {
		l.Items[i], l.Items[j] = l.Items[j], l.Items[i]
	}
	return l.Items
}

// WaitApproval waits for a pending approval of typ ("proposal" | "tool_call").
func (d *Daemon) WaitApproval(typ string) Approval {
	d.t.Helper()
	var got Approval
	d.Eventually("a pending "+typ+" approval", func() bool {
		for _, a := range d.Approvals("pending") {
			if a.Type == typ {
				got = a
				return true
			}
		}
		return false
	})
	return got
}

// Decide approves or denies an approval and returns the decided approval.
func (d *Daemon) Decide(id string, approve bool, body map[string]any) Approval {
	d.t.Helper()
	verb := "deny"
	if approve {
		verb = "approve"
	}
	var ap Approval
	d.Post("/api/approvals/"+id+"/"+verb, body).Want(200).JSON(&ap)
	return ap
}

// NodeDetail fetches a node run with its agent runs.
func (d *Daemon) NodeDetail(id string) NodeRunDetail {
	d.t.Helper()
	var n NodeRunDetail
	d.Get("/api/node-runs/" + id).Want(200).JSON(&n)
	return n
}

// AgentRuns returns the agent runs behind a node run, in phase order.
func (d *Daemon) AgentRuns(nodeRunID string) []AgentRun {
	d.t.Helper()
	return d.NodeDetail(nodeRunID).AgentRuns
}

// AgentEvents returns the transcript of an agent run.
func (d *Daemon) AgentEvents(agentRunID string) []AgentEvent {
	d.t.Helper()
	var l struct {
		Items []AgentEvent `json:"items"`
	}
	d.Get("/api/agent-runs/" + agentRunID + "/events?limit=1000").Want(200).JSON(&l)
	return l.Items
}

// Permissions returns the permission requests of an agent run.
func (d *Daemon) Permissions(agentRunID string) []PermissionRequest {
	d.t.Helper()
	var l struct {
		Items []PermissionRequest `json:"items"`
	}
	d.Get("/api/agent-runs/" + agentRunID + "/permission-requests").Want(200).JSON(&l)
	return l.Items
}

// Transcript is the transcript's text (message chunks) of an agent run.
func (d *Daemon) Transcript(agentRunID string) string {
	d.t.Helper()
	var b strings.Builder
	for _, ev := range d.AgentEvents(agentRunID) {
		b.Write(ev.Payload)
		b.WriteByte('\n')
	}
	return b.String()
}

// GetStatus returns the header counters.
func (d *Daemon) GetStatus() Status {
	d.t.Helper()
	var s Status
	d.Get("/api/status").Want(200).JSON(&s)
	return s
}

// GetFlow fetches a flow definition.
func (d *Daemon) GetFlow(id string) FlowDetail {
	d.t.Helper()
	var f FlowDetail
	d.Get("/api/flows/" + id).Want(200).JSON(&f)
	return f
}
