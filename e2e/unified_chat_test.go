package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Unified chat, full path: a flow run of a sandboxed agent node with ports is continued in a chat (a fork), and the chat
// uses the node's sandbox and tools, emits back into the flow through an approval, and meets the sandbox's keep_alive.
// The scripted fake ACP agent plays the flow run and every chat turn; the chat turns are listed first so that a transcript
// inside a replayed prompt cannot match the flow run's turn.

// ucOpts configures newUC.
type ucOpts struct {
	// load is whether the fake runtime supports session/load (a fork then resumes, else it replays).
	load bool
	// clock gives the daemon a manual clock (the keep_alive timer follows it).
	clock bool
	// keepAlive is the sandbox's keep_alive ("" leaves the default of 1m).
	keepAlive string
	// persistent makes the local sandbox lifecycle persistent (one workdir, the root, that is never removed).
	persistent bool
	// noSandbox removes the node's sandbox (and the flow run's sandbox calls).
	noSandbox bool
	// noPorts declares no ports on the node (its emit tool is emit_out).
	noPorts bool
}

// ucEnv is a daemon that ran flow `f` once.
type ucEnv struct {
	d    *Daemon
	root string // the local sandbox root
	run  FlowRunDetail
	orig AgentRun // the agent run of node `work`
}

const ucReportText = "flow report"

// ucFlow is flow f: pre -> work (sandbox local, tool shout, ports reviewed and needs_human, plus `ignored` without an
// edge) with reviewed -> tag -> shown and needs_human -> asked (an inbox node that asks first). Without ports the node's
// only edge leaves its out port, to `shown`.
func ucFlow(noPorts bool) string {
	if noPorts {
		return `id: f
enabled: true
subscribe: [src]
nodes:
  - id: pre
    type: transform
    steps:
      - {type: pick, fields: [id]}
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    sandbox: local
    tools: [shout]
    instructions: |
      Review {{.input.id}}
    output: none
  - {id: shown, type: inbox, title: "Out {{.input.agent.final_message}}"}
edges:
  - {from: pre, to: work}
  - {from: work, to: shown}
`
	}
	return `id: f
enabled: true
subscribe: [src]
nodes:
  - id: pre
    type: transform
    steps:
      - {type: pick, fields: [id]}
  - id: work
    type: agent
    runtime: fake
    mode: readonly
    sandbox: local
    tools: [shout]
    instructions: |
      Review {{.input.id}}
    ports:
      reviewed:
        description: Reviewed, nothing for a person to do.
        schema: ` + reviewedPortSchema + `
      needs_human:
        description: Needs a person to look.
        schema: ` + reviewedPortSchema + `
      ignored:
        description: Leads nowhere.
        schema: ` + reviewedPortSchema + `
  - id: tag
    type: transform
    steps:
      - {type: pick, fields: [input, agent]}
  - {id: shown, type: inbox, title: "Reviewed {{.input.agent.output.body}}", priority: "{{.input.agent.output.priority}}"}
  - {id: asked, type: inbox, title: "Needs {{.input.agent.output.body}}", requires_approval: true}
edges:
  - {from: pre, to: work}
  - {from: work, port: reviewed, to: tag}
  - {from: tag, to: shown}
  - {from: work, port: needs_human, to: asked}
`
}

// ucScript plays the flow run (the last turn) and the chat turns, selected by the end of the user's message.
func ucScript(load bool) script {
	return script{LoadSession: load, Turns: []turn{
		{When: when("read the file$"), Steps: []step{
			toolCall("fs_read", "c1", `{"path":"report.txt"}`),
			toolCall("fs_list", "c2", `{"path":"."}`),
			toolCall("fs_search", "c3", `{"pattern":"flow report"}`),
			toolCall("shout", "c4", `{"word":"hey"}`),
			toolCall("flows_list", "c5", `{}`),
			say("Read it."),
		}},
		{When: when("print secret$"), Steps: []step{
			toolCall("shell", "c1", `{"command":"echo $CHAT_SECRET"}`),
			say("Printed."),
		}},
		{When: when("write notes$"), Steps: []step{
			toolCall("fs_write", "c1", `{"path":"notes.txt","content":"chat notes"}`),
			say("Wrote it."),
		}},
		{When: when("read notes$"), Steps: []step{
			toolCall("fs_read", "c1", `{"path":"notes.txt"}`),
			toolCall("fs_read", "c2", `{"path":"report.txt"}`),
			say("Tried both."),
		}},
		{When: when("run touch$"), Steps: []step{
			toolCall("shell", "c1", `{"command":"touch asked.txt"}`),
			say("Ran it."),
		}},
		{When: when("run touch denied$"), Steps: []step{
			toolCall("shell", "c1", `{"command":"touch denied.txt"}`),
			say("Went on."),
		}},
		{When: when("run lockdown$"), Steps: []step{
			toolCall("shell", "c1", `{"command":"rm -f report.txt"}`),
			toolCall("fs_write", "c2", `{"path":"../escape.txt","content":"x"}`),
			toolCall("fs_read", "c3", `{"path":"report.txt"}`),
			say("Tried."),
		}},
		{When: when("emit reviewed$"), Steps: []step{
			toolCall("emit_reviewed", "e1", `{"priority":"urgent","body":"chat says hi"}`),
			say("Asked to emit."),
		}},
		{When: when("emit invalid$"), Steps: []step{
			toolCall("emit_reviewed", "e1", `{"priority":"soon","body":"x"}`),
			say("It was refused."),
		}},
		{When: when("emit needs human$"), Steps: []step{
			toolCall("emit_needs_human", "e1", `{"priority":"normal","body":"please look"}`),
			say("Asked to emit."),
		}},
		{When: when("emit out$"), Steps: []step{
			toolCall("emit_out", "e1", `{"final_message":"chat result"}`),
			say("Asked to emit."),
		}},
		{When: when("Review"), Steps: []step{
			toolCall("fs_write", "w1", `{"path":"report.txt","content":"`+ucReportText+`"}`),
			toolCall("shell", "w2", `{"command":"echo flow-ran > ran.txt"}`),
			toolCall("emit_reviewed", "w3", `{"priority":"urgent","body":"Flow says fine"}`),
			say("done"),
		}},
	}}
}

// ucNoPortsScript is ucScript for the node without ports: the flow run just ends with a message.
func ucNoPortsScript(load bool) script {
	s := ucScript(load)
	s.Turns[len(s.Turns)-1] = turn{When: when("Review"), Steps: []step{
		toolCall("fs_write", "w1", `{"path":"report.txt","content":"`+ucReportText+`"}`),
		say("Reviewed fine"),
	}}
	return s
}

func newUC(t *testing.T, o ucOpts) *ucEnv { t.Helper(); return newUCWith(t, o, nil) }

// newUCWith is newUC with extra setup before the daemon starts.
func newUCWith(t *testing.T, o ucOpts, setup func(d *Daemon)) *ucEnv {
	t.Helper()
	var opts []Option
	if o.clock {
		opts = append(opts, withClock())
	}
	d := newDaemon(t, opts...)
	d.InstallCLI()
	if setup != nil {
		setup(d)
	}
	root := filepath.Join(t.TempDir(), "sbx")
	lifecycle := "per_run"
	if o.persistent {
		lifecycle = "persistent"
	}
	cfg := "enabled: true\nroot: " + root + "\nlifecycle: " + lifecycle + "\napproval: rules\napproval_rules:\n  shell:\n    - {match: '^rm ', action: deny}\n    - {match: '^(echo|cat|ls) ', action: auto}\n  fs_write:\n    - {path: '**', action: auto}\n"
	if o.keepAlive != "" {
		cfg += "keep_alive: " + o.keepAlive + "\n"
	}
	d.Write("sandboxes/local.yaml", cfg)
	d.Write("tools/shout.yaml", echoTool)
	d.IdleSensor("src")
	rt, flow := ucScript(o.load), ucFlow(o.noPorts)
	if o.noPorts {
		rt = ucNoPortsScript(o.load)
	}
	if o.noSandbox {
		flow = strings.Replace(flow, "    sandbox: local\n", "", 1)
		rt.Turns[len(rt.Turns)-1] = turn{When: when("Review"), Steps: []step{
			toolCall("emit_reviewed", "w3", `{"priority":"urgent","body":"Flow says fine"}`), say("done"),
		}}
	}
	d.Runtime("fake", rt)
	d.Flow("f", flow)
	d.Start()
	run := d.RunOf("src", map[string]any{"id": "7"})
	runs := d.AgentRuns(d.Node(run, "work").ID)
	if len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].SessionID == "" {
		t.Fatalf("agent runs: %+v", runs)
	}
	return &ucEnv{d: d, root: root, run: run, orig: runs[0]}
}

// workspaces returns the files named name in the sandbox's per-run workspaces.
func (e *ucEnv) workspaces(name string) []string {
	m, _ := filepath.Glob(filepath.Join(e.root, "runs", "*", name))
	return m
}

func (d *Daemon) sandboxLogAt(path string) sandboxLog {
	d.t.Helper()
	var l sandboxLog
	d.Get(path).Want(200).JSON(&l)
	return l
}

// ucTurn sends text to the chat, waits for the turn and returns its agent run and the transcript (the agent's messages,
// which hold the result of every tool call as `mcp[<id>] is_error=<bool> <text>`).
func (e *ucEnv) ucTurn(chat, text string) (runJSON, string) {
	e.d.t.Helper()
	e.d.assistTurn(chat, text)
	runs := e.d.assistRuns(chat)
	run := runs[len(runs)-1]
	return run, e.d.Transcript(run.ID)
}

func wantContains(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, got)
		}
	}
}

// A flow run of a sandboxed node is continued within keep_alive: the sandbox is reattached with the run's files, its log
// moves to the conversation, and the divider says whether the runtime's own session was resumed or the run replayed.
func TestUnifiedChatReattachesTheSandboxOfTheRun(t *testing.T) {
	for _, load := range []bool{true, false} {
		t.Run(fmt.Sprintf("load_session=%v", load), func(t *testing.T) {
			e := newUC(t, ucOpts{load: load})
			d, orig := e.d, e.orig

			// Within keep_alive the run's sandbox is still there, with its files, and its log says so.
			if got := e.workspaces("report.txt"); len(got) != 1 {
				t.Fatalf("the run's workspace is gone before the fork: %v", got)
			}
			origLogPath := "/api/agent-runs/" + orig.ID + "/sandbox-log"
			before := d.sandboxLogAt(origLogPath)
			if len(before.Sessions) != 1 || before.Sessions[0].Status != "ready" || before.find("lifecycle", "Run ended; sandbox kept for 1m") == nil {
				t.Fatalf("log of the run before the fork: %+v %+v", before.Sessions, before.Entries)
			}

			fork := d.fork(orig.ID)

			// Creating the fork takes the sandbox over at once: the original log is handed over (and stays closed), the
			// conversation's log starts with the reattach entry. The workspace was not touched.
			after := d.sandboxLogAt(origLogPath)
			if len(after.Sessions) != 1 || after.Sessions[0].Status != "closed" || after.find("lifecycle", "Sandbox handed over to conversation "+fork.ID) == nil || after.find("lifecycle", "Teardown") != nil {
				t.Errorf("log of the run after the fork: %+v %+v", after.Sessions, after.Entries)
			}
			if len(after.Entries) != len(before.Entries)+1 {
				t.Errorf("the run's log changed beyond the hand-over entry: %d entries, was %d", len(after.Entries), len(before.Entries))
			}
			if byNode := d.sandboxLogAt("/api/node-runs/" + orig.NodeRunID + "/sandbox-log"); len(byNode.Sessions) != 1 || byNode.Sessions[0].ID != after.Sessions[0].ID {
				t.Errorf("the node run's log includes the conversation's session: %+v", byNode.Sessions)
			}
			convLog := d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
			if len(convLog.Sessions) != 1 || convLog.Sessions[0].Status != "ready" || convLog.Sessions[0].ID == after.Sessions[0].ID ||
				convLog.Sessions[0].Workdir != after.Sessions[0].Workdir || convLog.find("lifecycle", "Reattached sandbox of run "+orig.ID) == nil {
				t.Fatalf("conversation log after the fork: %+v %+v (run log %+v)", convLog.Sessions, convLog.Entries, after.Sessions)
			}
			if got := e.workspaces("report.txt"); len(got) != 1 {
				t.Errorf("workspaces after the fork: %v", got)
			}
			if flowConv := d.sandboxLogAt("/api/conversations/" + orig.ID + "/sandbox-log"); len(flowConv.Sessions) != 1 || flowConv.Sessions[0].ID != after.Sessions[0].ID {
				t.Errorf("the sandbox log of the flow conversation is not the run's: %+v", flowConv.Sessions)
			}

			// The first chat turn reads what the run wrote, through the tools of the node and the assistant.
			turn, tr := e.ucTurn(fork.ID, "read the file")
			wantContains(t, "transcript", tr,
				"mcp[c1] is_error=false "+ucReportText, "mcp[c2] is_error=false", "report.txt", "mcp[c3] is_error=false", "mcp[c4] is_error=false", `word\":\"hey`, "mcp[c5] is_error=false", `\"id\":\"f\"`)
			if strings.Contains(tr, "is_error=true") {
				t.Errorf("a tool call failed:\n%s", tr)
			}
			if n := len(d.Approvals("")); n != 0 {
				t.Errorf("the reads asked for approval: %d approvals", n)
			}

			// How the context was restored.
			wantDivider, wantCtx := "Continued from run "+orig.ID+"; earlier context replayed", "replayed"
			if load {
				wantDivider, wantCtx = "Continued from run "+orig.ID+"; session resumed", "resumed"
			}
			msgs := d.conversationMessages(fork.ID)
			if div := firstOfKind(msgs, "divider"); div.text() != wantDivider {
				t.Errorf("divider = %q, want %q", div.text(), wantDivider)
			}
			if c := d.conversation(fork.ID); c.ForkContext == nil || *c.ForkContext != wantCtx || c.Status != "idle" {
				t.Errorf("conversation = %+v", c)
			}
			var loaded bool
			for _, c := range d.AgentCalls("fake") {
				if c["method"] == "session/load" && c["session_id"] == orig.SessionID {
					loaded = true
				}
			}
			if loaded != load {
				t.Errorf("session/load of the run's session = %v, want %v", loaded, load)
			}
			if !strings.Contains(turn.RenderedPrompt, "This chat is a continuation of agent run "+orig.ID) {
				t.Errorf("the turn's prompt lacks the continuation section:\n%s", turn.RenderedPrompt)
			}

			// The conversation's log has the chat's file operations and the keep-alive hand-off; the run's log is as it was.
			convLog = d.sandboxLogAt("/api/conversations/" + fork.ID + "/sandbox-log")
			if len(convLog.Sessions) != 1 || convLog.find("fs", "read report.txt") == nil || convLog.find("lifecycle", "Reply finished; sandbox kept for 1m") == nil {
				t.Errorf("conversation log after the turn: %+v %+v", convLog.Sessions, convLog.Entries)
			}
			if again := d.sandboxLogAt(origLogPath); len(again.Entries) != len(after.Entries) {
				t.Errorf("the run's log grew while the chat used the sandbox: %d entries, was %d", len(again.Entries), len(after.Entries))
			}
			if got := e.workspaces("report.txt"); len(got) != 1 {
				t.Errorf("the chat used another workspace: %v", got)
			}

			// The lists link the fork.
			if ids := d.conversationIDs("?forked_from=" + orig.ID); len(ids) != 1 || ids[0] != fork.ID {
				t.Errorf("forked_from list = %v, want [%s]", ids, fork.ID)
			}
			if c := d.conversation(orig.ID); c.ForkCount != 1 || len(c.Forks) != 1 || c.Forks[0].ID != fork.ID {
				t.Errorf("original conversation = %+v", c)
			}
			// The run's own agent events are unchanged: the chat's turn is a turn of the chat.
			for _, ev := range d.AgentEvents(orig.ID) {
				if strings.Contains(string(ev.Payload), "read the file") {
					t.Errorf("a chat message reached the original run: %s", ev.Payload)
				}
			}
		})
	}
}

// luToolList is the tool list the agent's hal MCP server offers (GET /api/mcp/tools, with the token of the session).
type luToolList struct {
	Tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"tools"`
}

var luToken = regexp.MustCompile(`"--token","(hal_mcp_[A-Za-z0-9_-]+)"`)

// halTools returns the tools of the newest agent session of the fake runtime, by name.
func (d *Daemon) halTools() map[string]string {
	d.t.Helper()
	var token string
	for _, c := range d.AgentCalls("fake") {
		if c["method"] != "session/new" && c["method"] != "session/load" {
			continue
		}
		raw, _ := json.Marshal(c["mcp_servers"])
		if m := luToken.FindSubmatch(raw); m != nil {
			token = string(m[1])
		}
	}
	if token == "" {
		d.t.Fatalf("no hal MCP token in the call log: %v", d.AgentCalls("fake"))
	}
	var l luToolList
	d.Get("/api/mcp/tools", withHeader("Authorization", "Bearer "+token), withoutCookie()).Want(200).JSON(&l)
	out := map[string]string{}
	for _, t := range l.Tools {
		out[t.Name] = t.Description
	}
	return out
}

// The chat's tools are the node's (custom tool, sandbox tools), the assistant's read tools and one emit tool per port
// that has an edge. The lockdown and approval rules hold: a policy deny and an escaping path fail, a shell command that
// no rule allows waits in the Inbox, linked from its chat message.
func TestUnifiedChatToolsAndApprovals(t *testing.T) {
	e := newUC(t, ucOpts{load: true})
	d, orig := e.d, e.orig
	fork := d.fork(orig.ID)
	run, tr := e.ucTurn(fork.ID, "read the file")
	if strings.Contains(tr, "is_error=true") {
		t.Fatalf("first turn:\n%s", tr)
	}

	// What the agent's MCP server offers.
	tools := d.halTools()
	for _, name := range []string{"shout", "fs_read", "fs_write", "fs_list", "fs_search", "shell", "flows_list", "agent_run_events", "conversation_messages", "navigate_ui", "emit_reviewed", "emit_needs_human"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("the chat lacks the tool %q: %v", name, keys(tools))
		}
	}
	for _, name := range []string{"emit_out", "emit_ignored", "submit_output", "draft_write"} {
		if _, ok := tools[name]; ok {
			t.Errorf("the chat has the tool %q", name)
		}
	}
	for _, name := range []string{"emit_reviewed", "emit_needs_human"} {
		wantContains(t, name+" description", tools[name], "Leads to:", "ONLY when the user explicitly asked you to send or emit the result to the flow", "starts a new flow run after the user approves")
	}
	if !strings.Contains(tools["emit_reviewed"], `inbox note "Reviewed ..."`) && !strings.Contains(tools["emit_reviewed"], "Reviewed") {
		t.Errorf("emit_reviewed does not say where it leads: %s", tools["emit_reviewed"])
	}
	wantContains(t, "rendered prompt", run.RenderedPrompt,
		"- shout: Shout a word", "- shell:", "- fs_write:", "- fs_read:", "- flows_list:", "- agent_run_events:", "- emit_reviewed:", "- emit_needs_human:", "the sandbox `local`",
		"explicitly asks you to send or emit the result to the flow", "never on your own")
	if strings.Contains(run.RenderedPrompt, "- emit_ignored") || strings.Contains(run.RenderedPrompt, "- emit_out:") {
		t.Errorf("the prompt lists a tool the chat does not have:\n%s", run.RenderedPrompt)
	}

	// Lockdown: a policy deny, a path that leaves the workdir, then a read that still works.
	_, tr = e.ucTurn(fork.ID, "run lockdown")
	wantContains(t, "lockdown transcript", tr, "mcp[c1] is_error=true", "denied by policy", "mcp[c2] is_error=true", "mcp[c3] is_error=false "+ucReportText)
	if len(e.workspaces("report.txt")) != 1 {
		t.Error("the denied rm ran")
	}
	for _, p := range []string{filepath.Join(e.root, "runs", "escape.txt"), filepath.Join(e.root, "escape.txt")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("a write left the workdir: %s", p)
		}
	}

	// A command no rule allows waits for the user; the chat message of the call links the approval.
	d.assistSend(fork.ID, "run touch", nil)
	ap := d.WaitApproval("tool_call")
	if ap.PermissionRequest == nil || ap.PermissionRequest.Title != "hal tool: shell" || !strings.Contains(string(ap.PermissionRequest.RawInput), "touch asked.txt") || ap.AgentRunID == "" {
		t.Fatalf("approval: %+v", ap)
	}
	if got := d.assistWait(fork.ID, "awaiting_approval"); got.Status != "awaiting_approval" {
		t.Errorf("status = %s", got.Status)
	}
	if len(e.workspaces("asked.txt")) != 0 {
		t.Error("the command ran before the approval")
	}
	if linked := ucApprovalOfShell(d.conversationMessages(fork.ID), "shell"); linked != ap.ID {
		t.Errorf("the chat message of the shell call links approval %q, want %q", linked, ap.ID)
	}
	if n := len(d.pendingApprovals()); n != 1 {
		t.Errorf("pending approvals = %d, want 1", n)
	}
	d.Decide(ap.ID, true, nil)
	d.assistWait(fork.ID, "idle")
	if len(e.workspaces("asked.txt")) != 1 {
		t.Error("the approved command did not run in the sandbox")
	}
	if linked := ucApprovalOfShell(d.conversationMessages(fork.ID), "shell"); linked != ap.ID {
		t.Errorf("after the decision the message links approval %q, want %q", linked, ap.ID)
	}

	// A denial is a tool error and the chat goes on.
	d.assistSend(fork.ID, "run touch denied", nil)
	ap2 := d.WaitApproval("tool_call")
	d.Decide(ap2.ID, false, map[string]any{"comment": "no thanks"})
	d.assistWait(fork.ID, "idle")
	runs := d.assistRuns(fork.ID)
	if tr := d.Transcript(runs[len(runs)-1].ID); !strings.Contains(tr, "mcp[c1] is_error=true") || !strings.Contains(tr, "Denied by the user: ") || !strings.Contains(tr, "no thanks") || !strings.Contains(tr, "Went on.") {
		t.Errorf("denied shell transcript:\n%s", tr)
	}
	if len(e.workspaces("denied.txt")) != 0 {
		t.Error("the denied command ran")
	}
	if got := d.conversationMessages(fork.ID); got[len(got)-1].text() != "Went on." {
		t.Errorf("the chat did not continue after the denial: %+v", got[len(got)-1])
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ucApprovalOfShell returns the approval id on the newest tool message whose title ends with tool.
func ucApprovalOfShell(msgs []convMsgJSON, tool string) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if m := msgs[i]; m.ToolCall != nil && strings.HasSuffix(m.ToolCall.Title, tool) && m.ApprovalID != nil {
			return *m.ApprovalID
		}
	}
	return ""
}

// wantUpstreamReused checks that the continuation run holds a copy of node's run of the original run (the same input,
// output and times, a new id) instead of a run of its own.
func (e *ucEnv) wantUpstreamReused(orig, cont FlowRunDetail, node string) {
	e.d.t.Helper()
	o, c := e.d.Node(orig, node), e.d.Node(cont, node)
	if o.ID == c.ID || c.Status != o.Status || c.StartedAt != o.StartedAt || c.Stdout.Text != o.Stdout.Text || c.Stdin.Text != o.Stdin.Text {
		e.d.t.Errorf("node %s of the continuation is not the original's result: original %+v, continuation %+v", node, o, c)
	}
}
