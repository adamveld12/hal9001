package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"hal9001/internal/spec"
	"hal9001/internal/store"
)

const (
	// previewLimit is the TextBlob.text bound in list responses, event
	// detail and stream events; fullLimit applies to detail responses.
	previewLimit = 4 << 10
	fullLimit    = 64 << 10

	redacted = "«redacted»"

	// unknownRef stands in for a slug that cannot be resolved because the run
	// it belonged to was pruned.
	unknownRef = "unknown"
)

// cutUTF8 returns at most n bytes of s without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func utc(t time.Time) time.Time { return t.UTC() }

func utcPtr(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// ---- text blobs ----

type textBlobView struct {
	Text      string  `json:"text"`
	Size      int64   `json:"size"`
	Truncated bool    `json:"truncated"`
	Blob      *string `json:"blob"`
}

func contentSize(c store.Content) int64 {
	return max(c.Size, int64(len(c.Text)))
}

// blobFull renders captured text for a detail response: up to 64 KiB inline.
// Content that was spilled to a blob is read back for its first 64 KiB, and
// `blob` names it for GET /api/blobs/{hash}.
func (s *Server) blobFull(c store.Content) textBlobView {
	if c.Ref == "" {
		return textBlobView{Text: c.Text, Size: contentSize(c)}
	}
	v := textBlobView{Size: c.Size, Truncated: true, Blob: &c.Ref}
	if data, err := s.opts.Store.ReadContent(c); err == nil {
		v.Text = cutUTF8(string(data), fullLimit)
		v.Truncated = int64(len(v.Text)) < c.Size
	}
	return v
}

// blobPreview renders captured text for list responses, event detail and
// stream events: up to 4 KiB. Spilled content is not read; its text is empty
// and `blob` names it.
func blobPreview(c store.Content) textBlobView {
	if c.Ref != "" {
		return textBlobView{Size: c.Size, Truncated: true, Blob: &c.Ref}
	}
	text := cutUTF8(c.Text, previewLimit)
	return textBlobView{Text: text, Size: contentSize(c), Truncated: len(text) < len(c.Text)}
}

func (s *Server) blob(c store.Content, preview bool) textBlobView {
	if preview {
		return blobPreview(c)
	}
	return s.blobFull(c)
}

// contentJSON returns the content as a JSON value; text that is not valid JSON
// becomes a JSON string.
func (s *Server) contentJSON(c store.Content) json.RawMessage {
	data, err := s.opts.Store.ReadContent(c)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(data) {
		return json.RawMessage(bytes.TrimSpace(data))
	}
	b, _ := json.Marshal(string(data))
	return b
}

func (s *Server) contentString(c store.Content) string {
	data, err := s.opts.Store.ReadContent(c)
	if err != nil {
		return ""
	}
	return string(data)
}

// ---- lineage ----

// resolver looks up the runs that records point to, caching within one
// request so a page of approvals does not re-read the same flow run.
type resolver struct {
	s         *Server
	ctx       context.Context
	flowRuns  map[string]*store.FlowRun
	nodeRuns  map[string]*store.NodeRun
	agentRuns map[string]*store.AgentRun
	flows     map[string]*spec.Flow
	permIDs   map[string]map[string]string // agent run -> permission request -> approval
}

func (s *Server) resolver(ctx context.Context) *resolver {
	return &resolver{
		s: s, ctx: ctx,
		flowRuns:  map[string]*store.FlowRun{},
		nodeRuns:  map[string]*store.NodeRun{},
		agentRuns: map[string]*store.AgentRun{},
		flows:     map[string]*spec.Flow{},
		permIDs:   map[string]map[string]string{},
	}
}

// lookup returns the cached record, reading it on first use. A record that no
// longer exists yields nil without an error.
func lookup[T any](cache map[string]*T, id string, get func() (*T, error)) (*T, error) {
	if id == "" {
		return nil, nil
	}
	if v, ok := cache[id]; ok {
		return v, nil
	}
	v, err := get()
	if errors.Is(err, store.ErrNotFound) {
		cache[id] = nil
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cache[id] = v
	return v, nil
}

func (rv *resolver) flowRun(id string) (*store.FlowRun, error) {
	return lookup(rv.flowRuns, id, func() (*store.FlowRun, error) { return rv.s.opts.Store.GetFlowRun(rv.ctx, id) })
}

func (rv *resolver) nodeRun(id string) (*store.NodeRun, error) {
	return lookup(rv.nodeRuns, id, func() (*store.NodeRun, error) { return rv.s.opts.Store.GetNodeRun(rv.ctx, id) })
}

func (rv *resolver) agentRun(id string) (*store.AgentRun, error) {
	return lookup(rv.agentRuns, id, func() (*store.AgentRun, error) { return rv.s.opts.Store.GetAgentRun(rv.ctx, id) })
}

// flowVersion returns the parsed flow snapshot a flow run used, or nil.
func (rv *resolver) flowVersion(flowID, hash string) *spec.Flow {
	key := flowID + "@" + hash
	if f, ok := rv.flows[key]; ok {
		return f
	}
	var f *spec.Flow
	if v, err := rv.s.opts.Store.GetFlowVersion(rv.ctx, flowID, hash); err == nil {
		f, _ = spec.ParseFlow([]byte(v.YAML))
	}
	rv.flows[key] = f
	return f
}

// lineage identifies where a node run, agent run, approval or inbox item came from.
type lineage struct {
	FlowRunID string
	NodeRunID string
	FlowID    string
	NodeID    string
	EventID   string
}

func (rv *resolver) lineageOfNodeRun(nodeRunID, flowRunID string) (lineage, error) {
	l := lineage{FlowRunID: flowRunID, NodeRunID: nodeRunID, FlowID: unknownRef, NodeID: unknownRef}
	nr, err := rv.nodeRun(nodeRunID)
	if err != nil {
		return l, err
	}
	if nr != nil {
		l.NodeID = nr.NodeID
		if l.FlowRunID == "" {
			l.FlowRunID = nr.FlowRunID
		}
	}
	fr, err := rv.flowRun(l.FlowRunID)
	if err != nil {
		return l, err
	}
	if fr != nil {
		l.FlowID, l.EventID = fr.FlowID, fr.EventID
	}
	return l, nil
}

func (rv *resolver) lineageOfAgentRun(agentRunID string) (lineage, *store.AgentRun, error) {
	ar, err := rv.agentRun(agentRunID)
	if err != nil || ar == nil {
		return lineage{FlowID: unknownRef, NodeID: unknownRef}, ar, err
	}
	l, err := rv.lineageOfNodeRun(ar.NodeRunID, "")
	return l, ar, err
}

// approvalForPermission returns the approval raised by a permission request.
func (rv *resolver) approvalForPermission(agentRunID, permID string) (string, error) {
	m, ok := rv.permIDs[agentRunID]
	if !ok {
		m = map[string]string{}
		page, err := rv.s.opts.Store.ListApprovals(rv.ctx, store.ApprovalFilter{
			AgentRunID: agentRunID, Slim: true, ListOpts: store.ListOpts{Limit: 500, Oldest: true},
		})
		if err != nil {
			return "", err
		}
		for _, a := range page.Items {
			if a.PermissionRequestID != "" {
				m[a.PermissionRequestID] = a.ID
			}
		}
		rv.permIDs[agentRunID] = m
	}
	return m[permID], nil
}

// ---- sensor runs and events ----

type sensorRunView struct {
	ID         string       `json:"id"`
	SensorID   string       `json:"sensor_id"`
	Trigger    string       `json:"trigger"`
	Status     string       `json:"status"`
	Baseline   bool         `json:"baseline"`
	StartedAt  time.Time    `json:"started_at"`
	EndedAt    *time.Time   `json:"ended_at"`
	DurationMS *int64       `json:"duration_ms"`
	ExitCode   *int         `json:"exit_code"`
	ItemCount  *int         `json:"item_count"`
	NewCount   *int         `json:"new_count"`
	Stdout     textBlobView `json:"stdout"`
	Stderr     textBlobView `json:"stderr"`
	Error      *string      `json:"error"`
}

// sensorRunStatus maps the store's four statuses onto the contract's six: a
// failed run whose error class is timeout or killed reports that class.
func sensorRunStatus(r store.SensorRun) string {
	if r.Status == store.SensorFailed && (r.Error == "timeout" || r.Error == "killed") {
		return r.Error
	}
	return string(r.Status)
}

func (s *Server) sensorRunView(r store.SensorRun, preview bool) sensorRunView {
	trigger := "scheduled"
	if r.Origin != store.OriginSchedule {
		trigger = "manual"
	}
	v := sensorRunView{
		ID: r.ID, SensorID: r.SensorID, Trigger: trigger, Status: sensorRunStatus(r), Baseline: r.Baseline,
		StartedAt: utc(r.StartedAt), EndedAt: utcPtr(r.EndedAt), DurationMS: r.DurationMS, ExitCode: r.ExitCode,
		Stdout: s.blob(r.Stdout, preview), Stderr: s.blob(r.Stderr, preview), Error: nonEmpty(r.Error),
	}
	if r.Status != store.SensorRunning {
		v.ItemCount, v.NewCount = &r.ItemCount, &r.NewCount
	}
	return v
}

type eventView struct {
	ID          string          `json:"id"`
	SensorID    string          `json:"sensor_id"`
	SensorRunID *string         `json:"sensor_run_id"`
	DedupKey    string          `json:"dedup_key"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"created_at"`
	Injected    bool            `json:"injected"`
}

func (s *Server) eventView(ev *store.Event) eventView {
	return eventView{
		ID: ev.ID, SensorID: ev.SensorID, SensorRunID: nonEmpty(ev.SensorRunID), DedupKey: ev.DedupKey,
		Payload: s.contentJSON(ev.Payload), CreatedAt: utc(ev.CreatedAt), Injected: ev.SensorRunID == "",
	}
}

type flowRunBriefView struct {
	ID        string     `json:"id"`
	FlowID    string     `json:"flow_id"`
	FlowHash  string     `json:"flow_hash"`
	Kind      string     `json:"kind"`
	DryRun    bool       `json:"dry_run"`
	Status    string     `json:"status"`
	ReplayOf  *string    `json:"replay_of"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

func flowRunBrief(fr store.FlowRun) flowRunBriefView {
	return flowRunBriefView{
		ID: fr.ID, FlowID: fr.FlowID, FlowHash: fr.FlowHash, Kind: string(fr.Kind), DryRun: fr.DryRun,
		Status: string(fr.Status), ReplayOf: nonEmpty(fr.ReplayOf), CreatedAt: utc(fr.CreatedAt), EndedAt: utcPtr(fr.EndedAt),
	}
}

type eventSummaryView struct {
	ID             string             `json:"id"`
	SensorID       string             `json:"sensor_id"`
	SensorRunID    *string            `json:"sensor_run_id"`
	DedupKey       string             `json:"dedup_key"`
	PayloadPreview string             `json:"payload_preview"`
	CreatedAt      time.Time          `json:"created_at"`
	Injected       bool               `json:"injected"`
	FlowRuns       []flowRunBriefView `json:"flow_runs"`
}

const payloadPreviewRunes = 512

// payloadPreview is the payload as compact JSON cut to 512 characters.
func (s *Server) payloadPreview(c store.Content) string {
	data, err := s.opts.Store.ReadContent(c)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, data) != nil {
		buf.Reset()
		buf.Write(data)
	}
	text := buf.String()
	if utf8.RuneCountInString(text) <= payloadPreviewRunes {
		return text
	}
	i := 0
	for range payloadPreviewRunes {
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return text[:i]
}

// eventSummary builds the timeline entry for an event with the given runs.
func (s *Server) eventSummary(ev store.Event, runs []store.FlowRun) eventSummaryView {
	v := eventSummaryView{
		ID: ev.ID, SensorID: ev.SensorID, SensorRunID: nonEmpty(ev.SensorRunID), DedupKey: ev.DedupKey,
		PayloadPreview: s.payloadPreview(ev.Payload), CreatedAt: utc(ev.CreatedAt), Injected: ev.SensorRunID == "",
		FlowRuns: make([]flowRunBriefView, 0, len(runs)),
	}
	for _, fr := range runs {
		v.FlowRuns = append(v.FlowRuns, flowRunBrief(fr))
	}
	return v
}

// ---- flow runs and node runs ----

type flowRunView struct {
	ID        string     `json:"id"`
	FlowID    string     `json:"flow_id"`
	FlowHash  string     `json:"flow_hash"`
	EventID   *string    `json:"event_id"`
	Kind      string     `json:"kind"`
	ReplayOf  *string    `json:"replay_of"`
	DryRun    bool       `json:"dry_run"`
	Status    string     `json:"status"`
	Error     *string    `json:"error"`
	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

func flowRunFull(fr store.FlowRun) flowRunView {
	return flowRunView{
		ID: fr.ID, FlowID: fr.FlowID, FlowHash: fr.FlowHash, EventID: nonEmpty(fr.EventID), Kind: string(fr.Kind),
		ReplayOf: nonEmpty(fr.ReplayOf), DryRun: fr.DryRun, Status: string(fr.Status), Error: nonEmpty(fr.Error),
		CreatedAt: utc(fr.CreatedAt), StartedAt: utcPtr(fr.StartedAt), EndedAt: utcPtr(fr.EndedAt),
	}
}

type nodeRunView struct {
	ID              string       `json:"id"`
	FlowRunID       string       `json:"flow_run_id"`
	NodeID          string       `json:"node_id"`
	NodeType        string       `json:"node_type"`
	ParentNodeRunID *string      `json:"parent_node_run_id"`
	Status          string       `json:"status"`
	Decision        *string      `json:"decision"`
	Reason          *string      `json:"reason"`
	Error           *string      `json:"error"`
	Ports           []string     `json:"ports"`
	EmittedCount    int          `json:"emitted_count"`
	Node            spec.Node    `json:"node"`
	Argv            []string     `json:"argv"`
	EnvKeys         []string     `json:"env_keys"`
	Stdin           textBlobView `json:"stdin"`
	Stdout          textBlobView `json:"stdout"`
	Stderr          textBlobView `json:"stderr"`
	ExitCode        *int         `json:"exit_code"`
	DurationMS      *int64       `json:"duration_ms"`
	CreatedAt       time.Time    `json:"created_at"`
	StartedAt       *time.Time   `json:"started_at"`
	EndedAt         *time.Time   `json:"ended_at"`
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// nodeSnapshot returns the node definition a node run executed: the node of
// that id in the flow version snapshot of its flow run. When the snapshot is
// gone it falls back to what the node run itself recorded.
func (rv *resolver) nodeSnapshot(nr store.NodeRun) spec.Node {
	if fr, _ := rv.flowRun(nr.FlowRunID); fr != nil {
		if f := rv.flowVersion(fr.FlowID, fr.FlowHash); f != nil {
			if n := f.NodeByID(nr.NodeID); n != nil {
				return *n
			}
		}
	}
	return spec.Node{ID: nr.NodeID, Type: string(nr.NodeType), Lang: nr.Lang, Script: nr.Script}
}

func (rv *resolver) nodeRunView(nr store.NodeRun, preview bool) nodeRunView {
	v := nodeRunView{
		ID: nr.ID, FlowRunID: nr.FlowRunID, NodeID: nr.NodeID, NodeType: string(nr.NodeType),
		ParentNodeRunID: nonEmpty(nr.ParentNodeRunID), Status: string(nr.Status), Reason: nonEmpty(nr.Reason),
		Error: nonEmpty(nr.Error), Ports: nonNil(nr.Ports), EmittedCount: nr.OutputCount, Node: rv.nodeSnapshot(nr),
		EnvKeys: nonNil(nr.EnvKeys), Stdin: rv.s.blob(nr.Stdin, preview), Stdout: rv.s.blob(nr.Stdout, preview),
		Stderr: rv.s.blob(nr.Stderr, preview), ExitCode: nr.ExitCode, DurationMS: nr.DurationMS,
		CreatedAt: utc(nr.QueuedAt), StartedAt: utcPtr(nr.StartedAt), EndedAt: utcPtr(nr.EndedAt),
	}
	if len(nr.Argv) > 0 {
		v.Argv = nr.Argv
	}
	if nr.Decision != "" {
		d := string(nr.Decision)
		v.Decision = &d
	}
	return v
}

// ---- agent runs, events, permission requests, approvals ----

type noteView struct {
	Title          string     `json:"title"`
	Priority       string     `json:"priority"`
	SummaryMD      string     `json:"summary_md"`
	SuggestedReply string     `json:"suggested_reply,omitempty"`
	Links          []linkView `json:"links,omitempty"`
}

type linkView struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type agentRunView struct {
	ID            string          `json:"id"`
	NodeRunID     string          `json:"node_run_id"`
	FlowRunID     string          `json:"flow_run_id"`
	FlowID        string          `json:"flow_id"`
	NodeID        string          `json:"node_id"`
	Runtime       string          `json:"runtime"`
	AgentInfo     json.RawMessage `json:"agent_info"`
	Capabilities  json.RawMessage `json:"capabilities"`
	Mode          string          `json:"mode"`
	ApproveStyle  *string         `json:"approve_style"`
	Policy        string          `json:"policy"`
	Phase         string          `json:"phase"`
	SessionID     *string         `json:"session_id"`
	ResumedVia    *string         `json:"resumed_via"`
	ResumeFall    *string         `json:"resume_fallback"`
	Cwd           *string         `json:"cwd"`
	RenderedPromp string          `json:"rendered_prompt"`
	Argv          []string        `json:"argv"`
	Status        string          `json:"status"`
	StopReason    *string         `json:"stop_reason"`
	FinalMessage  *string         `json:"final_message"`
	Result        *noteView       `json:"result"`
	ParseError    bool            `json:"parse_error"`
	Error         *string         `json:"error"`
	RawBlob       *string         `json:"raw_blob"`
	RawSize       int64           `json:"raw_size"`
	CreatedAt     time.Time       `json:"created_at"`
	StartedAt     *time.Time      `json:"started_at"`
	EndedAt       *time.Time      `json:"ended_at"`
}

// rawOrNull returns raw as a JSON value, or null when it is empty.
func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null")
	}
	return raw
}

// parseAgentResult reads store.AgentRun.Result. The engine stores a JSON
// object {"final_message", "note", "parse_error", "resume_fallback"}; a bare
// structured note and plain text are accepted too.
func parseAgentResult(text string) (final *string, note *noteView, parseErr bool, resumeFallback *string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil, false, nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &obj) != nil {
		return &text, nil, false, nil
	}
	_, hasFinal := obj["final_message"]
	_, hasNote := obj["note"]
	_, hasParse := obj["parse_error"]
	_, hasFallback := obj["resume_fallback"]
	switch {
	case hasFinal || hasNote || hasParse || hasFallback:
		var env struct {
			FinalMessage   *string   `json:"final_message"`
			Note           *noteView `json:"note"`
			ParseError     bool      `json:"parse_error"`
			ResumeFallback *string   `json:"resume_fallback"`
		}
		if json.Unmarshal([]byte(text), &env) != nil {
			return &text, nil, false, nil
		}
		return env.FinalMessage, env.Note, env.ParseError, env.ResumeFallback
	case obj["title"] != nil && obj["summary_md"] != nil:
		var n noteView
		if json.Unmarshal([]byte(text), &n) == nil {
			return nil, &n, false, nil
		}
	}
	return &text, nil, false, nil
}

func (rv *resolver) agentRunView(ar store.AgentRun, preview bool) (agentRunView, error) {
	l, _, err := rv.lineageOfAgentRun(ar.ID)
	if err != nil {
		return agentRunView{}, err
	}
	prompt := rv.s.contentString(ar.Prompt)
	final, note, parseErr, resumeFallback := parseAgentResult(rv.s.contentString(ar.Result))
	if preview {
		prompt = cutUTF8(prompt, previewLimit)
		if final != nil {
			f := cutUTF8(*final, previewLimit)
			final = &f
		}
	}
	argv := ar.Argv
	if argv == nil {
		argv = []string{}
	}
	v := agentRunView{
		ID: ar.ID, NodeRunID: ar.NodeRunID, FlowRunID: l.FlowRunID, FlowID: l.FlowID, NodeID: l.NodeID, Runtime: ar.Runtime,
		AgentInfo: rawOrNull(ar.AgentInfo), Capabilities: rawOrNull(ar.Capabilities), Mode: string(ar.Mode),
		Policy: string(ar.Policy), Phase: string(ar.Phase), SessionID: nonEmpty(ar.SessionID), Cwd: nonEmpty(ar.Cwd),
		RenderedPromp: prompt, Argv: argv, Status: string(ar.Status), StopReason: nonEmpty(ar.StopReason),
		FinalMessage: final, Result: note, ParseError: parseErr, ResumeFall: resumeFallback, Error: nonEmpty(ar.Error), RawSize: ar.RawJSONRPC.Size,
		CreatedAt: utc(ar.CreatedAt), StartedAt: utcPtr(ar.StartedAt), EndedAt: utcPtr(ar.EndedAt),
	}
	if ar.ApproveStyle != "" {
		a := string(ar.ApproveStyle)
		v.ApproveStyle = &a
	}
	if ar.ResumedVia != "" {
		r := string(ar.ResumedVia)
		v.ResumedVia = &r
	}
	if ar.RawJSONRPC.Ref != "" {
		v.RawBlob = &ar.RawJSONRPC.Ref
	}
	return v, nil
}

type agentEventView struct {
	AgentRunID string          `json:"agent_run_id"`
	Seq        int64           `json:"seq"`
	TS         time.Time       `json:"ts"`
	Kind       string          `json:"kind"`
	ToolCallID *string         `json:"tool_call_id"`
	Payload    json.RawMessage `json:"payload"`
}

// agentEventView renders a transcript entry. With preview, a payload over
// 4 KiB is replaced by {"truncated": true, "size": N}; fetch it from
// GET /api/agent-runs/{id}/events.
func (s *Server) agentEventView(ev store.AgentEvent, preview bool) agentEventView {
	v := agentEventView{AgentRunID: ev.AgentRunID, Seq: ev.Seq, TS: utc(ev.TS), Kind: ev.Kind, ToolCallID: nonEmpty(ev.ToolCallID)}
	if preview && (ev.Payload.Ref != "" || contentSize(ev.Payload) > previewLimit) {
		v.Payload = json.RawMessage(`{"truncated":true,"size":` + strconv.FormatInt(contentSize(ev.Payload), 10) + `}`)
	} else {
		v.Payload = s.contentJSON(ev.Payload)
	}
	return v
}

type permOptionView struct {
	OptionID string `json:"option_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type permissionRequestView struct {
	ID          string           `json:"id"`
	AgentRunID  string           `json:"agent_run_id"`
	ToolCallID  string           `json:"tool_call_id"`
	ToolKind    string           `json:"tool_kind"`
	Title       string           `json:"title"`
	RawInput    json.RawMessage  `json:"raw_input"`
	Options     []permOptionView `json:"options"`
	Decision    *string          `json:"decision"`
	DecidedBy   *string          `json:"decided_by"`
	MatchedRule *string          `json:"matched_rule"`
	Reason      *string          `json:"reason"`
	ApprovalID  *string          `json:"approval_id"`
	RequestedAt time.Time        `json:"requested_at"`
	DecidedAt   *time.Time       `json:"decided_at"`
}

// permOptions reads the options the agent offered; both ACP's camelCase
// (optionId) and snake_case keys are accepted.
func permOptions(raw json.RawMessage) []permOptionView {
	out := []permOptionView{}
	var in []struct {
		OptionID  string `json:"optionId"`
		OptionID2 string `json:"option_id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return out
	}
	for _, o := range in {
		id := o.OptionID
		if id == "" {
			id = o.OptionID2
		}
		out = append(out, permOptionView{OptionID: id, Name: o.Name, Kind: o.Kind})
	}
	return out
}

func (rv *resolver) permissionRequestView(p store.PermissionRequest) (permissionRequestView, error) {
	approvalID, err := rv.approvalForPermission(p.AgentRunID, p.ID)
	if err != nil {
		return permissionRequestView{}, err
	}
	v := permissionRequestView{
		ID: p.ID, AgentRunID: p.AgentRunID, ToolCallID: p.ToolCallID, ToolKind: p.ToolKind, Title: p.Title,
		RawInput: rv.s.contentJSON(p.RawInput), Options: permOptions(p.Options), MatchedRule: nonEmpty(p.MatchedRule),
		Reason: nonEmpty(p.Reason), ApprovalID: nonEmpty(approvalID), RequestedAt: utc(p.RequestedAt), DecidedAt: utcPtr(p.DecidedAt),
	}
	if p.Decision != "" {
		d := string(p.Decision)
		v.Decision = &d
	}
	if p.DecidedBy != "" {
		d := string(p.DecidedBy)
		v.DecidedBy = &d
	}
	return v, nil
}

type approvalView struct {
	ID                  string                 `json:"id"`
	AgentRunID          string                 `json:"agent_run_id"`
	NodeRunID           string                 `json:"node_run_id"`
	FlowRunID           string                 `json:"flow_run_id"`
	FlowID              string                 `json:"flow_id"`
	NodeID              string                 `json:"node_id"`
	EventID             *string                `json:"event_id"`
	Runtime             string                 `json:"runtime"`
	Type                string                 `json:"type"`
	PermissionRequestID *string                `json:"permission_request_id"`
	PermissionRequest   *permissionRequestView `json:"permission_request"`
	ProposalMD          *string                `json:"proposal_md"`
	Status              string                 `json:"status"`
	Comment             *string                `json:"comment"`
	CancelRun           *bool                  `json:"cancel_run"`
	DecidedBy           *string                `json:"decided_by"`
	DecidedAt           *time.Time             `json:"decided_at"`
	CreatedAt           time.Time              `json:"created_at"`
	ExpiresAt           *time.Time             `json:"expires_at"`
}

func (rv *resolver) approvalView(a store.Approval) (approvalView, error) {
	l, ar, err := rv.lineageOfAgentRun(a.AgentRunID)
	if err != nil {
		return approvalView{}, err
	}
	v := approvalView{
		ID: a.ID, AgentRunID: a.AgentRunID, NodeRunID: l.NodeRunID, FlowRunID: l.FlowRunID, FlowID: l.FlowID, NodeID: l.NodeID,
		EventID: nonEmpty(l.EventID), Type: string(a.Type), PermissionRequestID: nonEmpty(a.PermissionRequestID),
		Status: string(a.Status), Comment: nonEmpty(a.Comment), DecidedBy: nonEmpty(a.DecidedBy),
		DecidedAt: utcPtr(a.DecidedAt), CreatedAt: utc(a.CreatedAt), ExpiresAt: utcPtr(a.ExpiresAt), Runtime: unknownRef,
	}
	if ar != nil {
		v.Runtime = ar.Runtime
	}
	if a.Type == store.ApprovalProposal {
		text := rv.s.contentString(a.Proposal)
		v.ProposalMD = &text
	}
	if a.PermissionRequestID != "" {
		p, err := rv.s.opts.Store.GetPermissionRequest(rv.ctx, a.PermissionRequestID)
		switch {
		case err == nil:
			pv, err := rv.permissionRequestView(*p)
			if err != nil {
				return approvalView{}, err
			}
			v.PermissionRequest = &pv
			if a.Type == store.ApprovalToolCall && a.Status == store.ApprovalDenied {
				cancelled := p.Decision == store.PermCancelled
				v.CancelRun = &cancelled
			}
		case !errors.Is(err, store.ErrNotFound):
			return approvalView{}, err
		}
	}
	return v, nil
}

// ---- inbox ----

type inboxItemView struct {
	ID             string     `json:"id"`
	Source         string     `json:"source"`
	SourceID       string     `json:"source_id"`
	FlowRunID      string     `json:"flow_run_id"`
	NodeRunID      string     `json:"node_run_id"`
	AgentRunID     *string    `json:"agent_run_id"`
	FlowID         string     `json:"flow_id"`
	NodeID         string     `json:"node_id"`
	EventID        *string    `json:"event_id"`
	Title          string     `json:"title"`
	Priority       string     `json:"priority"`
	SummaryMD      string     `json:"summary_md"`
	SuggestedReply *string    `json:"suggested_reply"`
	Links          []linkView `json:"links"`
	Read           bool       `json:"read"`
	Starred        bool       `json:"starred"`
	Archived       bool       `json:"archived"`
	DryRun         bool       `json:"dry_run"`
	ParseError     bool       `json:"parse_error"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (rv *resolver) inboxView(it store.InboxItem) (inboxItemView, error) {
	l, err := rv.lineageOfNodeRun(it.NodeRunID, it.FlowRunID)
	if err != nil {
		return inboxItemView{}, err
	}
	sourceID := it.NodeRunID
	if it.SourceType == store.SourceAgentRun {
		sourceID = it.AgentRunID
	}
	links := make([]linkView, 0, len(it.Links))
	for _, k := range it.Links {
		links = append(links, linkView{Label: k.Label, URL: k.URL})
	}
	return inboxItemView{
		ID: it.ID, Source: string(it.SourceType), SourceID: sourceID, FlowRunID: l.FlowRunID, NodeRunID: it.NodeRunID,
		AgentRunID: nonEmpty(it.AgentRunID), FlowID: l.FlowID, NodeID: l.NodeID, EventID: nonEmpty(l.EventID), Title: it.Title,
		Priority: string(it.Priority), SummaryMD: it.SummaryMD, SuggestedReply: nonEmpty(it.SuggestedReply), Links: links,
		Read: it.Read, Starred: it.Starred, Archived: it.Archived, DryRun: it.DryRun, ParseError: it.ParseError,
		CreatedAt: utc(it.CreatedAt),
	}, nil
}

// ---- kill switch, status, auth events ----

type killSwitchView struct {
	Paused      bool       `json:"paused"`
	KillRunning bool       `json:"kill_running"`
	Since       *time.Time `json:"since"`
	By          *string    `json:"by"`
	Reason      *string    `json:"reason"`
}

// killSwitchReasonKey is the settings key under which the API keeps the
// reason given when pausing; core.Engine.Pause has no reason parameter.
const killSwitchReasonKey = "kill_switch_reason"

func (s *Server) killSwitchView(ctx context.Context) (killSwitchView, error) {
	ks, err := s.opts.Store.GetKillSwitch(ctx)
	if err != nil {
		return killSwitchView{}, err
	}
	v := killSwitchView{Paused: ks.Paused}
	if !ks.Paused {
		return v, nil
	}
	v.KillRunning = ks.KillRunning
	v.Since = utcPtr(&ks.Since)
	v.By = nonEmpty(ks.By)
	if raw, err := s.opts.Store.GetSetting(ctx, killSwitchReasonKey); err == nil {
		var reason string
		if json.Unmarshal(raw, &reason) == nil {
			v.Reason = nonEmpty(reason)
		}
	}
	return v, nil
}

type statusView struct {
	KillSwitch       killSwitchView `json:"kill_switch"`
	RunningFlowRuns  int            `json:"running_flow_runs"`
	QueuedFlowRuns   int            `json:"queued_flow_runs"`
	RunningAgentRuns int            `json:"running_agent_runs"`
	QueuedAgentRuns  int            `json:"queued_agent_runs"`
	PendingApprovals int            `json:"pending_approvals"`
	UnreadInbox      int            `json:"unread_inbox"`
}

func (s *Server) statusView(ctx context.Context) (statusView, error) {
	c, err := s.opts.Store.Counts(ctx)
	if err != nil {
		return statusView{}, err
	}
	ks, err := s.killSwitchView(ctx)
	if err != nil {
		return statusView{}, err
	}
	return statusView{
		KillSwitch: ks, RunningFlowRuns: c.FlowRunsRunning, QueuedFlowRuns: c.FlowRunsQueued,
		RunningAgentRuns: c.AgentRunsRunning, QueuedAgentRuns: c.AgentRunsQueued,
		PendingApprovals: c.PendingApprovals, UnreadInbox: c.UnreadInbox,
	}, nil
}

type moduleTraceView struct {
	Module  string `json:"module"`
	Control string `json:"control,omitempty"`
	Result  string `json:"result"`
	Reason  string `json:"reason,omitempty"`
}

type authEventView struct {
	ID         string            `json:"id"`
	TS         time.Time         `json:"ts"`
	Action     string            `json:"action"`
	Service    string            `json:"service,omitempty"`
	Endpoint   string            `json:"endpoint"`
	Modules    []moduleTraceView `json:"modules,omitempty"`
	Outcome    string            `json:"outcome"`
	Reason     string            `json:"reason,omitempty"`
	Principal  string            `json:"principal,omitempty"`
	RemoteAddr string            `json:"remote_addr"`
	Host       string            `json:"host"`
	SessionID  string            `json:"session_id,omitempty"`
}

func authEvent(e store.AuthEvent) authEventView {
	v := authEventView{
		ID: e.ID, TS: utc(e.TS), Action: e.Action, Service: string(e.Service), Endpoint: e.Endpoint, Outcome: e.Outcome,
		Reason: e.Reason, Principal: e.Principal, RemoteAddr: e.RemoteAddr, Host: e.Host, SessionID: e.SessionID,
	}
	for _, m := range e.Results {
		v.Modules = append(v.Modules, moduleTraceView{Module: m.Module, Control: m.Control, Result: m.Result, Reason: m.Reason})
	}
	return v
}
