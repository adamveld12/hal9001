package e2e

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// Every approval hal raises, whatever raised it, is one RaiseApproval: it is in the Inbox listing with a title and a
// description, it was notified (title as subtitle, description as body), SSE carried approval.created, and an approval
// an agent run raised links to the conversation it was raised in.

// approvalStream follows GET /api/stream?types=approval.created and keeps the data lines it receives.
type approvalStream struct {
	mu   sync.Mutex
	data []string
}

func (d *Daemon) openApprovalStream() *approvalStream {
	d.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL()+"/api/stream?types=approval.created", nil)
	if err != nil {
		d.t.Fatal(err)
	}
	if d.Cookie != "" {
		req.Header.Set("Cookie", d.Cookie)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // closed by the reader goroutine
	if err != nil {
		cancel()
		d.t.Fatalf("GET /api/stream: %v", err)
	}
	s := &approvalStream{}
	ready := make(chan struct{})
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		var once sync.Once
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: stream.ready") {
				once.Do(func() { close(ready) })
			}
			if rest, ok := strings.CutPrefix(line, "data: "); ok && strings.Contains(rest, `"approval.created"`) {
				s.mu.Lock()
				s.data = append(s.data, rest)
				s.mu.Unlock()
			}
		}
	}()
	<-ready
	d.t.Cleanup(cancel)
	return s
}

func (s *approvalStream) carries(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.data {
		if strings.Contains(line, id) {
			return true
		}
	}
	return false
}

// raisedWant is what one creator's approval must look like.
type raisedWant struct {
	origin       string // flow | chat | mcp
	conversation string // "" (none), flow or chat
	descBy       string
	note         string // text the notification's body contains
}

// checkRaised asserts the common outcome of RaiseApproval for a pending approval.
func (d *Daemon) checkRaised(st *approvalStream, ap Approval, want raisedWant) {
	d.t.Helper()
	if ap.Title == "" || strings.TrimSpace(ap.Description) == "" {
		d.t.Errorf("approval %s has no title or description: %+v", ap.ID, ap)
	}
	if ap.DescriptionBy != want.descBy || ap.Origin.Kind != want.origin || ap.Origin.Label == "" {
		d.t.Errorf("approval %s description_by=%q origin=%+v, want %s/%s", ap.ID, ap.DescriptionBy, ap.Origin, want.descBy, want.origin)
	}
	listed := false
	for _, a := range d.Approvals("pending") {
		listed = listed || a.ID == ap.ID
	}
	if !listed {
		d.t.Errorf("approval %s is not in the Inbox listing", ap.ID)
	}
	switch {
	case want.conversation == "":
		if ap.Conversation != nil {
			d.t.Errorf("approval %s has a conversation: %+v", ap.ID, ap.Conversation)
		}
	case ap.Conversation == nil || ap.Conversation.Kind != want.conversation || ap.Conversation.Anchor == "" ||
		ap.Conversation.Path != "/chat/"+ap.Conversation.ID+"?message="+ap.Conversation.Anchor:
		d.t.Errorf("approval %s conversation = %+v, want a %s conversation with an anchor", ap.ID, ap.Conversation, want.conversation)
	default:
		var l struct {
			Items []Approval `json:"items"`
		}
		d.Get("/api/approvals?conversation_id=" + ap.Conversation.ID).Want(200).JSON(&l)
		if len(l.Items) == 0 || l.Items[0].ID != ap.ID {
			d.t.Errorf("approvals of conversation %s = %+v", ap.Conversation.ID, l.Items)
		}
	}
	d.Eventually("the notification of approval "+ap.ID, func() bool {
		for _, n := range d.Notifications() {
			if strings.HasSuffix(n["open"], "/inbox/approval/"+ap.ID) {
				return strings.Contains(n["subtitle"], ap.Title) && strings.Contains(n["message"], want.note)
			}
		}
		return false
	})
	d.Eventually("approval.created of "+ap.ID, func() bool { return st.carries(ap.ID) })
}

func TestRaisedProposalApproval(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "Review {{.input.id}}")
	d.Runtime("fake", proposalScript(true))
	d.Flow("f", approvalFlow("proposal", ""))
	d.Start()
	st := d.openApprovalStream()

	d.InjectRun("src", map[string]any{"id": "42"})
	ap := d.WaitApproval("proposal")
	if ap.Title != proposalPlan || ap.Description != proposalPlan {
		t.Errorf("a proposal is titled and described by its summary line: %q / %q", ap.Title, ap.Description)
	}
	d.checkRaised(st, ap, raisedWant{origin: "flow", conversation: "flow", descBy: "hal", note: proposalPlan})
}

func TestRaisedHubToolCallApproval(t *testing.T) {
	d, _ := halToolDaemon(t, "")
	st := d.openApprovalStream()

	d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("tool_call")
	if !strings.Contains(ap.Description, "o/r#1") || !strings.Contains(ap.Details.Command, "gh api -X POST") || ap.Priority != "high" {
		t.Errorf("hal describes the call from its facts: %+v", ap)
	}
	d.checkRaised(st, ap, raisedWant{origin: "flow", conversation: "flow", descBy: "hal", note: "o/r#1"})
}

func TestRaisedApprovalNode(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("ask", approvalNodeFlow(""))
	d.Start()
	st := d.openApprovalStream()

	d.InjectRun("src", map[string]any{"id": "1", "name": "api", "meta": map[string]any{"env": "prod"}})
	ap := d.WaitApproval("request")
	if ap.Description != "Asks whether to deploy api" || ap.Gate || ap.Priority != "high" {
		t.Errorf("approval node: %+v", ap)
	}
	d.checkRaised(st, ap, raisedWant{origin: "flow", descBy: "hal", note: "Asks whether to deploy api"})
}

func TestRaisedGateApproval(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Flow("g", gateFlowYAML("", "", true))
	d.Start()
	st := d.openApprovalStream()

	d.InjectRun("src", map[string]any{"id": "7", "name": "api"})
	ap := d.WaitApproval("request")
	if !ap.Gate || ap.Description != "Run node act (command) of flow g" || !strings.Contains(ap.Details.Command, "echo ran") {
		t.Errorf("gate: %+v", ap)
	}
	d.checkRaised(st, ap, raisedWant{origin: "flow", descBy: "hal", note: "Run node act"})
}

func TestRaisedMCPApprovals(t *testing.T) {
	d := newDaemon(t)
	d.Start()
	st := d.openApprovalStream()
	s := connectMCP(t, d, "e2e-client")

	// A gated call: hal writes the description.
	wait := callAsync(s, "flow_create", map[string]any{"id": "agent-made", "spec": mcpNewFlow})
	gated := d.WaitApproval("request")
	d.checkRaised(st, gated, raisedWant{origin: "mcp", descBy: "hal", note: gated.Description[:10]})
	d.Decide(gated.ID, false, map[string]any{"comment": "no"})
	if res, err := wait(); err != nil || !res.IsError {
		t.Fatalf("denied flow_create: %v %+v", err, res)
	}

	// request_approval: the agent's description, labelled as such; without one it is a tool error.
	res := callMCP(t, s, "request_approval", map[string]any{"title": "Send it?"})
	if !res.IsError || !strings.Contains(resultText(t, res), "description is required") {
		t.Fatalf("request_approval without a description: %+v", res)
	}
	res = callMCP(t, s, "request_approval", map[string]any{"title": "Send it?", "description": "Mail the weekly report to the team.", "wait_seconds": 0})
	if res.IsError {
		t.Fatalf("request_approval: %+v", res)
	}
	var asked Approval
	for _, a := range d.Approvals("pending") {
		if a.Title == "Send it?" {
			asked = a
		}
	}
	if asked.ID == "" || asked.Description != "Mail the weekly report to the team." {
		t.Fatalf("asked approval: %+v", asked)
	}
	d.checkRaised(st, asked, raisedWant{origin: "mcp", descBy: "agent", note: "Mail the weekly report"})
}

// An agent run's request_approval: it needs a description (a tool error otherwise, and the agent retries), the card
// says the agent wrote it, the denial's comment comes back as the steering text, and it links to the conversation.
func TestRaisedAgentAskedApproval(t *testing.T) {
	d := newDaemon(t)
	d.IdleSensor("src")
	d.Prompt("p", "none", "go {{.input.id}}")
	d.Runtime("fake", script{Turns: []turn{{When: inMode("default"), Steps: []step{
		{Op: fakeagent.OpMCPCall, ID: "a1", Tool: "request_approval", Args: map[string]any{"title": "Deploy now?"}},
		{Op: fakeagent.OpMCPCall, ID: "a2", Tool: "request_approval", Args: map[string]any{
			"title": "Deploy now?", "description": "Deploy build 42 to staging because the tests passed.", "wait_seconds": 60,
		}},
		say("finished"),
	}}}})
	d.Flow("f", approvalFlow("per_tool", ""))
	d.Start()
	st := d.openApprovalStream()

	runID := d.InjectRun("src", map[string]any{"id": "7"})
	ap := d.WaitApproval("request")
	if ap.Description != "Deploy build 42 to staging because the tests passed." || ap.AgentRunID == "" {
		t.Fatalf("agent-asked approval: %+v", ap)
	}
	d.checkRaised(st, ap, raisedWant{origin: "flow", conversation: "flow", descBy: "agent", note: "Deploy build 42"})
	if ap.Conversation.ID != ap.AgentRunID {
		t.Errorf("a flow conversation is the agent run: %+v", ap.Conversation)
	}

	d.Decide(ap.ID, false, map[string]any{"comment": "wait for the release freeze to end"})
	done := d.WaitRun(runID, "succeeded")
	tr := d.Transcript(d.AgentRuns(d.Node(done, "work").ID)[0].ID)
	if !strings.Contains(tr, "mcp[a1] is_error=true") || !strings.Contains(tr, "description is required") {
		t.Errorf("a request without a description is a tool error:\n%s", tr)
	}
	if !strings.Contains(tr, "mcp[a2] is_error=false") || !strings.Contains(tr, "wait for the release freeze to end") || !strings.Contains(tr, "denied") {
		t.Errorf("the denial and its steering comment did not reach the agent:\n%s", tr)
	}
	if got := d.Approvals("pending"); len(got) != 0 {
		t.Errorf("pending after the decision: %+v", got)
	}
}
