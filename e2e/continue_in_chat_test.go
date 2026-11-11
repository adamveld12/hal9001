package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/agent/fakeagent"
)

// "Continue in chat": a finished agent run of a flow node is forked into an assistant chat. The scripted fake ACP agent
// plays both the flow run and the chat; the call log of the fake agent shows how the chat's first turn got the run's
// context (session/load, or a compacted transcript in the prompt).

type convJSON struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	Running    bool    `json:"running"`
	AgentRunID *string `json:"agent_run_id"`
	ForkedFrom *struct {
		AgentRunID string  `json:"agent_run_id"`
		FlowID     *string `json:"flow_id"`
		NodeID     *string `json:"node_id"`
	} `json:"forked_from"`
	ForkContext *string    `json:"fork_context"`
	ForkCount   int        `json:"fork_count"`
	Forks       []convJSON `json:"forks"`
}

type convMsgJSON struct {
	ID         string  `json:"id"`
	Role       string  `json:"role"`
	Kind       string  `json:"kind"`
	Text       *string `json:"text"`
	ApprovalID *string `json:"approval_id"`
	ToolCall   *struct {
		ToolCallID string `json:"tool_call_id"`
		Title      string `json:"title"`
		Status     string `json:"status"`
	} `json:"tool_call"`
}

func (m convMsgJSON) text() string {
	if m.Text == nil {
		return ""
	}
	return *m.Text
}

func (d *Daemon) conversation(id string) convJSON {
	d.t.Helper()
	var c convJSON
	d.Get("/api/conversations/" + id).Want(200).JSON(&c)
	return c
}

func (d *Daemon) conversationMessages(id string) []convMsgJSON {
	d.t.Helper()
	var out struct {
		Items []convMsgJSON `json:"items"`
	}
	d.Get("/api/conversations/" + id + "/messages").Want(200).JSON(&out)
	return out.Items
}

func (d *Daemon) conversationIDs(query string) []string {
	d.t.Helper()
	var out struct {
		Items []convJSON `json:"items"`
	}
	d.Get("/api/conversations" + query).Want(200).JSON(&out)
	ids := make([]string, len(out.Items))
	for i, c := range out.Items {
		ids[i] = c.ID
	}
	return ids
}

// firstOfKind returns the first message of the kind, or the zero value.
func firstOfKind(msgs []convMsgJSON, kind string) convMsgJSON {
	for _, m := range msgs {
		if m.Kind == kind {
			return m
		}
	}
	return convMsgJSON{}
}

// continueFlow is flow `f`: a readonly agent node `work` (extra: more node YAML, indented four spaces) whose output
// goes to an inbox node.
func continueFlow(extra string) string {
	return `id: f
enabled: true
subscribe: [src]
nodes:
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    prompt: p
` + extra + `  - {id: done, type: inbox, title: "done {{.input.input.id}}"}
edges:
  - {from: work, to: done}
`
}

// continueDaemon runs the flow `f` (one agent node `work`, readonly, node text "Triage <id>") once and returns the daemon
// and the agent run. The script answers the flow run, and two chat messages ("why exactly", "and the tests"); the chat
// turns come first so that a transcript in the prompt cannot match the flow's turn.
func continueDaemon(t *testing.T, load bool, extraFlow string) (*Daemon, AgentRun) {
	t.Helper()
	return continueDaemonWith(t, load, extraFlow, nil)
}

// continueDaemonWith is continueDaemon with extra setup before the daemon starts.
func continueDaemonWith(t *testing.T, load bool, extraFlow string, setup func(d *Daemon)) (*Daemon, AgentRun) {
	t.Helper()
	d := newAssistDaemon(t, func(d *Daemon) {
		if setup != nil {
			setup(d)
		}
		d.IdleSensor("src")
		d.Prompt("p", "none", "Triage {{.input.id}}")
		d.Runtime("fake", script{LoadSession: load, Turns: []turn{
			{When: when("why exactly\\?$"), Steps: []step{say("It is the golangci-lint step.")}},
			{When: when("and the tests\\?$"), Steps: []step{say("The tests pass.")}},
			{When: when("run it$"), Steps: []step{
				mcp("c1", "shell", map[string]any{"command": "echo from-the-chat"}),
				mcp("c2", "shout", map[string]any{"word": "hey"}),
				say("Ran both."),
			}},
			{When: when("Triage"), Steps: []step{say("The build is red because of lint.")}},
		}})
		d.Write("tools/shout.yaml", echoTool)
		d.Flow("f", continueFlow(extraFlow))
	})
	run := d.RunOf("src", map[string]any{"id": "7"})
	runs := d.AgentRuns(d.Node(run, "work").ID)
	if len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].SessionID == "" {
		t.Fatalf("agent runs: %+v", runs)
	}
	return d, runs[0]
}

func (d *Daemon) fork(runID string) convJSON {
	d.t.Helper()
	var c convJSON
	r := d.Post("/api/conversations/"+runID+"/fork", nil).Want(201)
	r.JSON(&c)
	if loc := r.Header.Get("Location"); loc != "/api/conversations/"+c.ID {
		d.t.Errorf("Location = %q", loc)
	}
	return c
}

// agentPrompts returns the text of the session/prompt calls the fake agent received, in order.
func (d *Daemon) agentPrompts() []string {
	d.t.Helper()
	var out []string
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/prompt" {
			s, _ := c["text"].(string)
			out = append(out, s)
		}
	}
	return out
}

// With a runtime that supports session/load the chat loads the run's own session in the run's working directory, and
// the first prompt carries no transcript.
func TestContinueInChatResumesTheRunsSession(t *testing.T) {
	d, orig := continueDaemon(t, true, "")
	eventsBefore := len(d.AgentEvents(orig.ID))

	fork := d.fork(orig.ID)
	if fork.Source != "chat" || fork.Kind != "general" || fork.Status != "idle" || fork.Running || fork.AgentRunID != nil || fork.ForkContext != nil ||
		fork.ForkedFrom == nil || fork.ForkedFrom.AgentRunID != orig.ID || *fork.ForkedFrom.FlowID != "f" || *fork.ForkedFrom.NodeID != "work" || fork.Title != "Continued: f / work" {
		t.Fatalf("fork = %+v", fork)
	}
	if msgs := d.conversationMessages(fork.ID); len(msgs) != 1 || msgs[0].Kind != "divider" || msgs[0].text() != "Continued from run "+orig.ID {
		t.Fatalf("a new fork has only its divider: %+v", msgs)
	}
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" {
			t.Fatalf("creating the fork started the agent: %v", c)
		}
	}

	d.assistTurn(fork.ID, "why exactly?")
	msgs := d.conversationMessages(fork.ID)
	var texts []string
	for _, m := range msgs {
		if m.Kind == "" {
			texts = append(texts, m.Role+": "+m.text())
		}
	}
	if got := strings.Join(texts, " | "); got != "user: why exactly? | assistant: It is the golangci-lint step." {
		t.Errorf("chat messages = %q", got)
	}
	if div := firstOfKind(msgs, "divider"); div.text() != "Continued from run "+orig.ID+"; session resumed" {
		t.Errorf("divider = %q", div.text())
	}
	got := d.conversation(fork.ID)
	if got.ForkContext == nil || *got.ForkContext != "resumed" || got.Title != "why exactly?" || got.Status != "idle" {
		t.Errorf("fork after its first turn = %+v", got)
	}

	// The agent loaded the run's session, in the run's working directory.
	var loaded bool
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" && c["session_id"] == orig.SessionID && c["cwd"] == orig.Cwd {
			loaded = true
		}
	}
	if !loaded {
		t.Errorf("no session/load of %s in %s: %v", orig.SessionID, orig.Cwd, d.AgentCalls("fake"))
	}
	prompts := d.agentPrompts()
	last := prompts[len(prompts)-1]
	if strings.Contains(last, "Transcript of run") || !strings.HasSuffix(last, "why exactly?") {
		t.Errorf("the resumed chat's prompt carries a transcript or lacks the message:\n%s", last)
	}
	// The instructions of a forked chat: the run, the node's emit target and the rule for it.
	runs := d.assistRuns(fork.ID)
	if len(runs) != 1 {
		t.Fatalf("turns = %+v", runs)
	}
	for _, want := range []string{"[hal9001 assistant]", "This chat is a continuation of agent run " + orig.ID + " of flow `f`, node `work`", "- emit_out:", "Leads to: inbox note", "explicitly asks you to send or emit the result to the flow"} {
		if !strings.Contains(runs[0].RenderedPrompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, runs[0].RenderedPrompt)
		}
	}

	// The original run is untouched, and the lists link the two.
	if after := d.AgentRuns(d.Node(d.GetRun(orig.FlowRunID, 0), "work").ID); len(after) != 1 || after[0].Status != "succeeded" || after[0].SessionID != orig.SessionID {
		t.Errorf("original agent runs after the fork: %+v", after)
	}
	if n := len(d.AgentEvents(orig.ID)); n != eventsBefore {
		t.Errorf("the original run has %d events, had %d", n, eventsBefore)
	}
	if ids := d.conversationIDs("?forked_from=" + orig.ID); len(ids) != 1 || ids[0] != fork.ID {
		t.Errorf("forked_from list = %v, want [%s]", ids, fork.ID)
	}
	if ids := d.conversationIDs("?source=chat&flow=f"); len(ids) != 1 || ids[0] != fork.ID {
		t.Errorf("chats of the flow = %v", ids)
	}
	if ids := d.conversationIDs("?source=flow"); len(ids) != 1 || ids[0] != orig.ID {
		t.Errorf("flow conversations = %v, want only %s (a chat turn is not one)", ids, orig.ID)
	}
	orig2 := d.conversation(orig.ID)
	if orig2.Source != "flow" || orig2.ForkCount != 1 || len(orig2.Forks) != 1 || orig2.Forks[0].ID != fork.ID || orig2.Status != "succeeded" {
		t.Errorf("original conversation = %+v", orig2)
	}
	flowMsgs := d.conversationMessages(orig.ID)
	if len(flowMsgs) < 3 || flowMsgs[0].Role != "user" || !strings.Contains(flowMsgs[0].text(), "Triage 7") || flowMsgs[len(flowMsgs)-1].text() != "Run finished." {
		t.Errorf("transcript of the flow run: %+v", flowMsgs)
	}

	// The CLI mirrors the API.
	d.sameAsAPI("conversations list", d.cliJSON("", "conversations", "list", "--forked-from", orig.ID), "/api/conversations?forked_from="+orig.ID)
	d.sameAsAPI("conversation", d.cliJSON("", "conversations", "get", orig.ID), "/api/conversations/"+orig.ID)
	d.sameAsAPI("conversation messages", d.cliJSON("", "conversations", "messages", fork.ID), "/api/conversations/"+fork.ID+"/messages")
	d.sameAsAPI("flow conversations", d.cliJSON("", "conversations", "list", "--source", "flow", "--q", "triage"), "/api/conversations?source=flow&q=triage")
	var cliFork map[string]any
	if out, errOut, code := d.CLI("", "conversations", "fork", orig.ID); code != 0 || json.Unmarshal([]byte(out), &cliFork) != nil || cliFork["source"] != "chat" {
		t.Errorf("conversations fork: exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	// A second message goes on in the same session.
	d.assistTurn(fork.ID, "and the tests?")
	if m := d.conversationMessages(fork.ID); m[len(m)-1].text() != "The tests pass." {
		t.Errorf("second turn: %+v", m)
	}
}

// Without session/load the first prompt carries a compacted transcript of the run (and only the first).
func TestContinueInChatReplaysTheTranscriptWhenTheRuntimeCannotLoad(t *testing.T) {
	d, orig := continueDaemon(t, false, "")
	fork := d.fork(orig.ID)
	d.assistTurn(fork.ID, "why exactly?")

	msgs := d.conversationMessages(fork.ID)
	if div := firstOfKind(msgs, "divider"); div.text() != "Continued from run "+orig.ID+"; earlier context replayed" {
		t.Errorf("divider = %q", div.text())
	}
	if got := d.conversation(fork.ID); got.ForkContext == nil || *got.ForkContext != "replayed" {
		t.Errorf("fork_context = %v", got.ForkContext)
	}
	for _, c := range d.AgentCalls("fake") {
		if c["method"] == "session/load" {
			t.Errorf("session/load for a runtime without loadSession: %v", c)
		}
	}
	prompts := d.agentPrompts()
	first := prompts[len(prompts)-1]
	for _, want := range []string{"Transcript of run " + orig.ID, "[user]", "Triage 7", "[assistant]", "The build is red because of lint.", "The user now says:\n\nwhy exactly?"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first prompt lacks %q:\n%s", want, first)
		}
	}

	d.assistTurn(fork.ID, "and the tests?")
	prompts = d.agentPrompts()
	if second := prompts[len(prompts)-1]; strings.Contains(second, "Transcript of run") {
		t.Errorf("the second prompt replays the transcript again:\n%s", second)
	}
}

// When session/load fails the turn falls back to a new session with the transcript, and the divider says why.
func TestContinueInChatFallsBackWhenSessionLoadFails(t *testing.T) {
	d, orig := continueDaemon(t, true, "")
	fork := d.fork(orig.ID)
	if err := os.Remove(filepath.Join(d.Path("fake/fake/sessions"), orig.SessionID+".jsonl")); err != nil {
		t.Fatal(err)
	}
	d.assistTurn(fork.ID, "why exactly?")

	div := firstOfKind(d.conversationMessages(fork.ID), "divider")
	if !strings.HasPrefix(div.text(), "Continued from run "+orig.ID+"; earlier context replayed (") || !strings.Contains(div.text(), "session/load failed") {
		t.Errorf("divider = %q", div.text())
	}
	if got := d.conversation(fork.ID); got.ForkContext == nil || *got.ForkContext != "replayed" {
		t.Errorf("fork_context = %v", got.ForkContext)
	}
	prompts := d.agentPrompts()
	if first := prompts[len(prompts)-1]; !strings.Contains(first, "Transcript of run "+orig.ID) || !strings.Contains(first, "The build is red because of lint.") {
		t.Errorf("the fallback prompt carries no transcript:\n%s", first)
	}
}

// The chat has the node's custom tools and sandbox tools next to the read tools.
func TestContinuedChatHasTheNodesToolsAndSandbox(t *testing.T) {
	d, orig := continueDaemonWith(t, true, "    sandbox: local\n    tools: [shout]\n", func(d *Daemon) {
		d.Write("sandboxes/local.yaml", "enabled: true\nroot: "+filepath.Join(t.TempDir(), "sbx")+"\nlifecycle: persistent\napproval: auto\n")
	})
	fork := d.fork(orig.ID)
	d.assistTurn(fork.ID, "run it")

	runs := d.assistRuns(fork.ID)
	if len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatalf("turns = %+v", runs)
	}
	for _, want := range []string{"- shout: Shout a word", "- shell:", "- fs_write:", "- fs_read:", "- flows_list:", "- agent_run_events:", "- navigate_ui:", "the sandbox `local`"} {
		if !strings.Contains(runs[0].RenderedPrompt, want) {
			t.Errorf("the chat's tools lack %q:\n%s", want, runs[0].RenderedPrompt)
		}
	}
	var tools []string
	for _, m := range d.conversationMessages(fork.ID) {
		if m.ToolCall != nil {
			tools = append(tools, m.ToolCall.Title+":"+m.ToolCall.Status)
		}
	}
	if len(tools) != 2 || !strings.HasSuffix(strings.Split(tools[0], ":")[0], "shell") || !strings.HasSuffix(strings.Split(tools[1], ":")[0], "shout") {
		t.Errorf("tool calls of the chat = %v", tools)
	}
	if tr := d.Transcript(runs[0].ID); !strings.Contains(tr, "mcp[c1] is_error=false") || !strings.Contains(tr, "from-the-chat") || !strings.Contains(tr, "mcp[c2] is_error=false") {
		t.Errorf("the tools did not run:\n%s", tr)
	}
}

func TestContinueInChatRefusesWhatIsNotAFinishedFlowRun(t *testing.T) {
	d := newAssistDaemon(t, func(d *Daemon) {
		d.IdleSensor("src")
		d.Prompt("p", "none", "Triage {{.input.id}}")
		d.Runtime("fake", script{LoadSession: true, Turns: []turn{
			{When: when("hold$"), Steps: []step{say("chat")}},
			{When: when("Triage"), Steps: []step{{Op: fakeagent.OpSleep, MS: 600000}}},
		}})
		d.Flow("f", continueFlow(""))
	})
	// An unknown run, and a turn of a chat, are not flow runs.
	d.Post("/api/conversations/01JC7Z3K9Q8V5T2M4N6P8R0S1T/fork", nil).Want(404)
	s := d.assistCreateGeneral()
	d.assistTurn(s.ID, "hold")
	turn := d.assistRuns(s.ID)[0]
	d.Post("/api/conversations/"+turn.ID+"/fork", nil).Want(404)
	d.Post("/api/conversations/"+s.ID+"/fork", nil).Want(404)

	// A run that is still running cannot be continued; it can be watched as a conversation.
	runID := d.InjectRun("src", map[string]any{"id": "1"})
	var agentRun string
	d.Eventually("the agent run to start", func() bool {
		var l struct {
			Items []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"items"`
		}
		d.Get("/api/agent-runs?flow_run_id=" + runID).Want(200).JSON(&l)
		if len(l.Items) == 1 && l.Items[0].Status == "running" {
			agentRun = l.Items[0].ID
		}
		return agentRun != ""
	})
	if p := d.Post("/api/conversations/"+agentRun+"/fork", nil).Want(409).Problem(); p != "invalid-state" {
		t.Errorf("problem = %q", p)
	}
	if c := d.conversation(agentRun); !c.Running || c.Status != "running" {
		t.Errorf("running run = %+v", c)
	}
}
