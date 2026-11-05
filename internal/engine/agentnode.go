package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"hal9001/internal/agent"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// runAgent executes an agent node (§7): it renders the prompt, resolves the
// working directory, and drives the agent through internal/agent. A node
// whose proposal was approved while it was parked is resumed in the same session.
func (e *Engine) runAgent(ctx context.Context, ne *nodeEnv) nodeOutcome {
	node, nr := ne.node, ne.nr
	rt, ok := e.runtimeConfig(ne.snap, node.Runtime)
	if !ok {
		return failure(errConfig, fmt.Sprintf("runtime %q is not configured", node.Runtime))
	}
	mode, style := agent.Mode(node.Mode), agent.ApproveStyle(node.Approve)
	if ne.fr.DryRun {
		// Dry-run: agents are forced read-only and can never act (§8).
		mode, style = agent.ModeReadonly, ""
	}
	prev, approval, err := e.parkedProposal(nr)
	if err != nil {
		return failure(errConfig, err.Error())
	}
	if prev != nil && (approval == nil || approval.Status != store.ApprovalApproved) {
		return failure(errConfig, "node has an unresolved proposal but no approved approval")
	}

	var prompt, cwd, wtKey string
	if prev != nil {
		prompt, cwd = strings.TrimSuffix(e.readContent(prev.Prompt), agent.ProposalSuffix), prev.Cwd
	} else {
		if prompt, err = e.renderPrompt(ne); err != nil {
			return failure(errTemplate, err.Error())
		}
		var spawnedWT bool
		if cwd, spawnedWT, err = e.resolveCwd(ctx, ne); err != nil {
			return failure(errConfig, "working directory: "+err.Error())
		}
		if spawnedWT {
			wtKey = nr.ID
		}
	}
	if prev != nil && prev.WorktreePath != "" {
		wtKey = nr.ID
	}

	expectNote := node.Mode == spec.ModeReadonly
	if p := ne.snap.prompts[node.Prompt]; p != nil && p.Output == spec.OutputNote {
		expectNote = true
	}
	req := agent.RunRequest{
		RunID:           e.st.NewID(),
		NodeRunID:       nr.ID,
		Runtime:         node.Runtime,
		Config:          rt,
		Mode:            mode,
		Approve:         style,
		Prompt:          prompt,
		Model:           node.Model,
		Cwd:             cwd,
		MCPServers:      convertMCP(node.MCPServers),
		Timeout:         node.Timeout.Std(),
		ApprovalTimeout: ne.snap.cfg.ApprovalTimeoutFor(node),
		ExpectNote:      expectNote,
	}
	if node.Allow != nil {
		req.Allow = agent.AllowRules{Tools: node.Allow.Tools, Commands: node.Allow.Commands}
	}
	rec := &runRecorder{e: e, red: ne.snap.red, model: node.Model, worktree: e.worktreePath(wtKey)}
	ex := &agent.Executor{
		Recorder:      rec,
		Broker:        e.broker,
		Clock:         e.clock,
		Log:           e.log,
		Environ:       e.agentEnviron(ne),
		ClientVersion: "hal9001",
	}

	var res agent.RunRecord
	if prev != nil {
		e.markPhaseOneDone(prev)
		res, err = ex.Resume(ctx, agent.ResumeRequest{
			RunRequest: req, PrevRunID: prev.ID, SessionID: prev.SessionID,
			Proposal: e.readContent(approval.Proposal), Comment: approval.Comment,
		})
	} else {
		res, err = ex.Run(ctx, req)
	}
	if err != nil {
		return failure(errConfig, err.Error())
	}
	if wtKey != "" {
		if rerr := e.wt.Release(e.dbCtx, wtKey, res.Status); rerr != nil {
			e.log.Warn("engine: release worktree", "key", wtKey, "err", rerr)
		}
	}
	return e.agentOutcome(ne, res, expectNote)
}

// parkedProposal finds the propose-phase run of a node that is being resumed,
// and its approval. prev is nil for a fresh node run.
func (e *Engine) parkedProposal(nr *store.NodeRun) (*store.AgentRun, *store.Approval, error) {
	runs, err := e.st.ListAgentRuns(e.dbCtx, store.AgentRunFilter{NodeRunID: nr.ID, ListOpts: store.ListOpts{Oldest: true, Limit: 50}})
	if err != nil {
		return nil, nil, err
	}
	if len(runs.Items) == 0 {
		return nil, nil, nil
	}
	last := runs.Items[len(runs.Items)-1]
	if last.Phase != store.PhasePropose || last.Status != store.AgentAwaitingApproval {
		return nil, nil, nil
	}
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{AgentRunID: last.ID, Type: store.ApprovalProposal, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil || len(apps.Items) == 0 {
		return &last, nil, err
	}
	return &last, &apps.Items[0], nil
}

// markPhaseOneDone closes the propose-phase run once its approved plan is being executed.
func (e *Engine) markPhaseOneDone(prev *store.AgentRun) {
	prev.Status = store.AgentSucceeded
	if err := e.st.UpdateAgentRun(e.dbCtx, prev); err != nil {
		e.log.Warn("engine: close propose-phase run", "agent_run", prev.ID, "err", err)
	}
}

func (e *Engine) worktreePath(key string) string {
	if key == "" {
		return ""
	}
	p, err := e.wt.Path(key)
	if err != nil {
		return ""
	}
	return p
}

// renderPrompt renders the prompt file with the node's template data and
// appends the node's extra_prompt.
func (e *Engine) renderPrompt(ne *nodeEnv) (string, error) {
	node := ne.node
	p := ne.snap.prompts[node.Prompt]
	if p == nil {
		return "", fmt.Errorf("prompt %q does not exist", node.Prompt)
	}
	body, err := ne.render("prompt "+node.Prompt, p.Body)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(node.ExtraPrompt) != "" {
		extra, err := ne.render("extra_prompt", node.ExtraPrompt)
		if err != nil {
			return "", err
		}
		body = strings.TrimRight(body, "\n") + "\n\n" + extra
	}
	return body, nil
}

// resolveCwd renders the node's cwd templates and prepares the directory.
// worktree reports that a git worktree was created for this node run.
func (e *Engine) resolveCwd(ctx context.Context, ne *nodeEnv) (dir string, worktree bool, err error) {
	var cs agent.CwdSpec
	if c := ne.node.Cwd; c != nil {
		cs.Mode = c.Mode
		switch c.Mode {
		case spec.CwdDir:
			if cs.Path, err = ne.render("cwd.path", c.Path); err != nil {
				return "", false, err
			}
		case spec.CwdWorktree:
			if cs.Repo, err = ne.render("cwd.repo", c.Repo); err != nil {
				return "", false, err
			}
			if cs.Ref, err = ne.render("cwd.ref", c.Ref); err != nil {
				return "", false, err
			}
			if cs.Repo, err = agent.ExpandHome(cs.Repo); err != nil {
				return "", false, err
			}
		}
	}
	def := filepath.Join(e.data, "state", ne.fr.FlowID, ne.node.ID)
	dir, err = e.wt.ResolveCwd(ctx, cs, ne.nr.ID, def)
	return dir, cs.Mode == spec.CwdWorktree && err == nil, err
}

// agentEnviron is the base environment of the agent process: the node
// environment (PATH, env file values, HAL_* ids) without a state dir or event file.
func (e *Engine) agentEnviron(ne *nodeEnv) []string {
	env, cleanup, err := runner.BuildEnv(runner.EnvOptions{
		Path:    e.opt.BasePATH,
		EnvFile: ne.snap.env,
		RunID:   ne.fr.ID,
		EventID: ne.fr.EventID,
		Flow:    ne.fr.FlowID,
		Node:    ne.node.ID,
		DryRun:  ne.fr.DryRun,
	})
	if err != nil {
		e.log.Warn("engine: build agent environment", "err", err)
		env = runner.DefaultBaseEnv(e.opt.BasePATH)
	}
	defer cleanup()
	out := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		out = append(out, k+"="+env[k])
	}
	return out
}

func convertMCP(in []spec.MCPServer) []agent.MCPServer {
	out := make([]agent.MCPServer, len(in))
	for i, s := range in {
		out[i] = agent.MCPServer{Name: s.Name, Type: s.Type, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Headers: s.Headers}
	}
	return out
}

// agentOutcome maps a finished agent run onto the node run: ports out and
// denied, the error port for failures, inbox items for notes (§7).
func (e *Engine) agentOutcome(ne *nodeEnv, res agent.RunRecord, expectNote bool) nodeOutcome {
	out := nodeOutcome{argv: res.Argv}
	switch res.Status {
	case agent.StatusAwaitingApproval:
		out.status, out.reason = store.NodeAwaitingApproval, "awaiting approval "+res.ApprovalID
	case agent.StatusSucceeded:
		out.status, out.decision = store.NodeDone, store.DecisionEmitted
		if err := e.noteToInbox(ne, res, expectNote); err != nil {
			e.log.Error("engine: create inbox item from agent note", "agent_run", res.ID, "err", err)
		}
		out.outputs = []runner.Output{{Port: spec.PortOut, Payload: e.agentPayload(ne, res)}}
		out.reason = "agent run " + res.ID
	case agent.StatusDenied:
		out.status, out.decision, out.class = store.NodeDone, store.DecisionEmitted, agent.ReasonDenied
		out.reason = firstNonEmpty(res.Error, "denied")
		out.outputs = []runner.Output{{Port: spec.PortDenied, Payload: e.agentPayload(ne, res)}}
	case agent.StatusKilled:
		out.status, out.decision, out.class, out.reason = store.NodeKilled, store.DecisionError, errKilled, "killed by the kill switch"
	case agent.StatusInterrupted:
		out.status, out.decision, out.class, out.reason = store.NodeInterrupted, store.DecisionError, errInterrupted, "daemon stopped"
	default: // failed, timeout
		class := res.FailureReason
		if res.Status == agent.StatusTimeout {
			class = agent.ReasonTimeout
		}
		out = failure(firstNonEmpty(class, "agent_error"), ne.snap.red.String(firstNonEmpty(res.Error, "agent run "+string(res.Status))))
		out.argv = res.Argv
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// agentPayload is what an agent node emits downstream: its input and a summary
// of the run, so a following node can use both ({{.input.input.x}} and
// {{.input.agent.final_message}}, {{.input.agent.note.title}}, ...).
func (e *Engine) agentPayload(ne *nodeEnv, res agent.RunRecord) json.RawMessage {
	return e.agentSummary(ne, map[string]any{
		"run_id": res.ID, "status": string(res.Status), "final_message": res.FinalMessage, "note": res.Note,
		"proposal": "", "comment": "",
	})
}

func (e *Engine) agentSummary(ne *nodeEnv, summary map[string]any) json.RawMessage {
	b, err := json.Marshal(map[string]any{"input": rawOrString(ne.input), "agent": summary})
	if err != nil {
		return json.RawMessage(`null`)
	}
	return ne.snap.red.Bytes(b)
}

// rawOrString embeds JSON as is, and anything else as a JSON string.
func rawOrString(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

// noteToInbox turns a successful run's structured note into an inbox item; an
// unparseable final message becomes a normal-priority item flagged parse_error (§7.4).
func (e *Engine) noteToInbox(ne *nodeEnv, res agent.RunRecord, expectNote bool) error {
	red := ne.snap.red
	it := &store.InboxItem{
		SourceType: store.SourceAgentRun, AgentRunID: res.ID, NodeRunID: ne.nr.ID, FlowRunID: ne.fr.ID, DryRun: ne.fr.DryRun,
	}
	switch {
	case res.Note != nil:
		n := res.Note
		it.Title, it.Priority = red.String(n.Title), store.Priority(n.Priority)
		it.SummaryMD, it.SuggestedReply = red.String(n.SummaryMD), red.String(n.SuggestedReply)
		for _, l := range n.Links {
			it.Links = append(it.Links, store.InboxLink{Label: red.String(l.Label), URL: red.String(l.URL)})
		}
	case expectNote:
		it.Title = fmt.Sprintf("Unstructured agent result: %s/%s", ne.fr.FlowID, ne.node.ID)
		it.Priority, it.ParseError = store.PriorityNormal, true
		it.SummaryMD = red.String(res.FinalMessage)
		if it.SummaryMD == "" {
			it.SummaryMD = "The agent produced no final message."
		}
	default:
		return nil
	}
	return e.addInbox(ne.snap, it)
}

// deniedPayload is the payload of a denied proposal: the node's input with the proposal and comment.
func (e *Engine) deniedPayload(ne *nodeEnv, run *store.AgentRun, ap *store.Approval) json.RawMessage {
	return e.agentSummary(ne, map[string]any{
		"run_id": run.ID, "status": "denied", "final_message": "", "note": nil,
		"proposal": e.readContent(ap.Proposal), "comment": ap.Comment,
	})
}

var errNoWaiter = errors.New("no agent process is waiting for this approval")
