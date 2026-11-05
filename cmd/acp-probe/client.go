package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/coder/acp-go-sdk"
)

// Permission policies for session/request_permission.
const (
	policyAllowAll     = "allow-all"
	policyRejectAll    = "reject-all"
	policyLogAndReject = "log-and-reject"
	policyCancel       = "cancel"
)

func validPolicy(p string) bool {
	switch p {
	case policyAllowAll, policyRejectAll, policyLogAndReject, policyCancel:
		return true
	}
	return false
}

type toolInfo struct {
	ID       string
	Kind     string
	Title    string
	Status   string
	RawInput string
	Meta     string
}

type permRecord struct {
	ToolCallID string
	Kind       string
	Title      string
	RawInput   string
	Options    []string
	Decision   string
}

// probeClient implements acp.Client. It advertises no fs/terminal capability, so
// the file and terminal methods are never legitimately called; they return
// method-not-found style errors if an agent calls them anyway.
//
// Display of agent traffic is driven from the wire log (see handleAgentMsg), not
// from SessionUpdate, because the wire log keeps fields the typed structs drop.
type probeClient struct {
	policy      string
	allowAlways bool
	out         io.Writer

	mu       sync.Mutex
	text     strings.Builder
	thoughts int
	updates  int
	tools    map[string]*toolInfo
	order    []string
	perms    []permRecord

	lastElicit   json.RawMessage // raw params of the newest elicitation/create
	elicitations []string
}

var _ acp.Client = (*probeClient)(nil)

func newProbeClient(out io.Writer, policy string, allowAlways bool) *probeClient {
	return &probeClient{policy: policy, allowAlways: allowAlways, out: out, tools: map[string]*toolInfo{}}
}

func (c *probeClient) printf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = fmt.Fprintf(c.out, format, args...)
}

// takeText returns and clears the agent message text accumulated since the last call.
func (c *probeClient) takeText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.text.String()
	c.text.Reset()
	return s
}

func (c *probeClient) resetCounters() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.text.Reset()
	c.thoughts = 0
	c.updates = 0
}

func (c *probeClient) counters() (updates, thoughts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updates, c.thoughts
}

// handleAgentMsg renders agent-originated JSON-RPC messages as they arrive.
func (c *probeClient) handleAgentMsg(m wireMsg) {
	switch m.Method {
	case "session/update":
		c.handleUpdate(m.Params)
	case "session/request_permission", "elicitation/create":
		if m.Method == "elicitation/create" {
			c.mu.Lock()
			c.lastElicit = m.Params
			c.mu.Unlock()
		}
		c.printf("\n=== %s (id %s)\n%s\n", m.Method, string(m.ID), prettyJSON(m.Params))
	}
}

func (c *probeClient) handleUpdate(params json.RawMessage) {
	var p struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	var u struct {
		Kind       string          `json:"sessionUpdate"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		ToolKind   string          `json:"kind"`
		Title      string          `json:"title"`
		Status     string          `json:"status"`
		RawInput   json.RawMessage `json:"rawInput"`
		Meta       json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(p.Update, &u) != nil {
		return
	}
	c.mu.Lock()
	c.updates++
	c.mu.Unlock()
	switch u.Kind {
	case "agent_message_chunk":
		var cb struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(u.Content, &cb)
		c.mu.Lock()
		c.text.WriteString(cb.Text)
		c.mu.Unlock()
	case "agent_thought_chunk":
		c.mu.Lock()
		c.thoughts++
		c.mu.Unlock()
	case "user_message_chunk":
		// Replayed history during session/load; counted in updates only.
	case "tool_call", "tool_call_update":
		c.mu.Lock()
		t, ok := c.tools[u.ToolCallID]
		if !ok {
			t = &toolInfo{ID: u.ToolCallID}
			c.tools[u.ToolCallID] = t
			c.order = append(c.order, u.ToolCallID)
		}
		if u.ToolKind != "" {
			t.Kind = u.ToolKind
		}
		if u.Title != "" {
			t.Title = u.Title
		}
		if u.Status != "" {
			t.Status = u.Status
		}
		if len(u.RawInput) > 0 && string(u.RawInput) != "null" {
			t.RawInput = compactJSON(u.RawInput)
		}
		if len(u.Meta) > 0 && string(u.Meta) != "null" {
			t.Meta = compactJSON(u.Meta)
		}
		line := fmt.Sprintf("[%s] id=%s kind=%s status=%s title=%q rawInput=%s\n",
			u.Kind, u.ToolCallID, u.ToolKind, u.Status, u.Title, trunc(compactJSON(u.RawInput), 400))
		_, _ = io.WriteString(c.out, line)
		c.mu.Unlock()
	default:
		c.printf("[%s] %s\n", u.Kind, trunc(compactJSON(p.Update), 300))
	}
}

func (c *probeClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func (c *probeClient) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	rec := permRecord{ToolCallID: string(p.ToolCall.ToolCallId)}
	if p.ToolCall.Kind != nil {
		rec.Kind = string(*p.ToolCall.Kind)
	}
	if p.ToolCall.Title != nil {
		rec.Title = *p.ToolCall.Title
	}
	if p.ToolCall.RawInput != nil {
		if b, err := json.Marshal(p.ToolCall.RawInput); err == nil {
			rec.RawInput = string(b)
		}
	}
	for _, o := range p.Options {
		rec.Options = append(rec.Options, fmt.Sprintf("%s:%s", o.OptionId, o.Kind))
	}

	var resp acp.RequestPermissionResponse
	switch c.policy {
	case policyAllowAll:
		if id, ok := pickOption(p.Options, c.allowAlways, true); ok {
			resp = selected(id)
			rec.Decision = "allow:" + string(id)
		} else {
			resp = cancelled()
			rec.Decision = "cancelled (no allow option offered)"
		}
	case policyRejectAll, policyLogAndReject:
		if id, ok := pickOption(p.Options, false, false); ok {
			resp = selected(id)
			rec.Decision = "reject:" + string(id)
		} else {
			resp = cancelled()
			rec.Decision = "cancelled (no reject option offered)"
		}
	case policyCancel:
		resp = cancelled()
		rec.Decision = "cancelled"
	default:
		return acp.RequestPermissionResponse{}, errors.New("unknown policy " + c.policy)
	}
	c.mu.Lock()
	c.perms = append(c.perms, rec)
	c.mu.Unlock()
	c.printf(">>> permission decision (%s): tool=%s kind=%q title=%q -> %s\n", c.policy, rec.ToolCallID, rec.Kind, rec.Title, rec.Decision)
	return resp, nil
}

// pickOption chooses the permission option for a decision. For allow it prefers
// allow_once unless always is set; for reject it prefers reject_once.
func pickOption(opts []acp.PermissionOption, always, allow bool) (acp.PermissionOptionId, bool) {
	var first, second acp.PermissionOptionKind
	switch {
	case allow && always:
		first, second = acp.PermissionOptionKindAllowAlways, acp.PermissionOptionKindAllowOnce
	case allow:
		first, second = acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways
	default:
		first, second = acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways
	}
	for _, want := range []acp.PermissionOptionKind{first, second} {
		for _, o := range opts {
			if o.Kind == want {
				return o.OptionId, true
			}
		}
	}
	return "", false
}

func selected(id acp.PermissionOptionId) acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: id}}}
}

func cancelled() acp.RequestPermissionResponse {
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{Outcome: "cancelled"}}}
}

// UnstableCreateElicitation answers form elicitations (which omp uses for
// generic tool approvals when the client advertises elicitation.form). The
// probe maps the permission policy onto accept/decline/cancel; the request is
// printed in full by handleAgentMsg so the form schema is on record.
//
//nolint:unparam // the acp.ClientExperimental interface requires the error result
func (c *probeClient) UnstableCreateElicitation(context.Context, acp.UnstableCreateElicitationRequest) (acp.UnstableCreateElicitationResponse, error) {
	c.mu.Lock()
	raw := c.lastElicit
	c.mu.Unlock()
	var resp acp.UnstableCreateElicitationResponse
	var decision string
	switch c.policy {
	case policyAllowAll:
		resp = acp.UnstableCreateElicitationResponse{Accept: &acp.UnstableCreateElicitationAccept{Action: "accept", Content: approveForm(raw)}}
		decision = "accept"
	case policyCancel:
		resp = acp.UnstableCreateElicitationResponse{Cancel: &acp.UnstableCreateElicitationCancel{Action: "cancel"}}
		decision = "cancel"
	default:
		resp = acp.UnstableCreateElicitationResponse{Decline: &acp.UnstableCreateElicitationDecline{Action: "decline"}}
		decision = "decline"
	}
	c.mu.Lock()
	c.elicitations = append(c.elicitations, decision)
	c.mu.Unlock()
	c.printf(">>> elicitation decision (%s): %s\n", c.policy, decision)
	return resp, nil
}

func (c *probeClient) UnstableCompleteElicitation(context.Context, acp.UnstableCompleteElicitationNotification) error {
	return nil
}

func (c *probeClient) UnstableConnectMcp(context.Context, acp.UnstableConnectMcpRequest) (acp.UnstableConnectMcpResponse, error) {
	return acp.UnstableConnectMcpResponse{}, errNoCapability
}

func (c *probeClient) UnstableDisconnectMcp(context.Context, acp.UnstableDisconnectMcpRequest) (acp.UnstableDisconnectMcpResponse, error) {
	return acp.UnstableDisconnectMcpResponse{}, errNoCapability
}

var errNoCapability = errors.New("capability not advertised by the probe client")

func (c *probeClient) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	c.printf("!!! agent called fs/read_text_file although fs was not advertised\n")
	return acp.ReadTextFileResponse{}, errNoCapability
}

func (c *probeClient) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	c.printf("!!! agent called fs/write_text_file although fs was not advertised\n")
	return acp.WriteTextFileResponse{}, errNoCapability
}

func (c *probeClient) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	c.printf("!!! agent called terminal/create although terminal was not advertised\n")
	return acp.CreateTerminalResponse{}, errNoCapability
}

func (c *probeClient) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errNoCapability
}

func (c *probeClient) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errNoCapability
}

func (c *probeClient) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errNoCapability
}

func (c *probeClient) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errNoCapability
}

// approveForm fills an enum form by picking, for each property, the first enum
// value that starts with "Approve" (omp's wording), else the first value.
func approveForm(raw json.RawMessage) map[string]any {
	var p struct {
		Schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"requestedSchema"`
	}
	content := map[string]any{}
	if json.Unmarshal(raw, &p) != nil {
		return content
	}
	for k, prop := range p.Schema.Properties {
		if len(prop.Enum) == 0 {
			continue
		}
		choice := prop.Enum[0]
		for _, v := range prop.Enum {
			if strings.HasPrefix(strings.ToLower(v), "approve") {
				choice = v
				break
			}
		}
		content[k] = choice
	}
	return content
}
