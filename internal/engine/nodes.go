package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/core"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/transform"
)

// Error classes recorded in NodeRun.Error (api/openapi.yaml NodeRun.error).
const (
	errTimeout        = "timeout"
	errExitNonzero    = "exit_nonzero"
	errUndeclaredPort = "undeclared_port"
	errInvalidOutput  = "invalid_output"
	errKilled         = "killed"
	errInterrupted    = "interrupted"
	errTemplate       = "template"
	errConfig         = "config"
	errStart          = "start_failed"
)

// nodeEnv is everything a node run needs, resolved once.
type nodeEnv struct {
	e     *Engine
	snap  *snapshot
	fr    *store.FlowRun
	flow  *spec.Flow
	node  *spec.Node
	nr    *store.NodeRun
	input []byte // the node's input payload (JSON text)
	event []byte // the original event payload; the input when the run has no event

	inputV, eventV any
}

func (n *nodeEnv) data() map[string]any {
	return spec.NewData(n.eventV, n.inputV,
		spec.FlowInfo{ID: n.fr.FlowID, Hash: n.fr.FlowHash, DryRun: n.fr.DryRun},
		spec.RunInfo{ID: n.fr.ID, EventID: n.fr.EventID, Kind: string(n.fr.Kind)})
}

func (n *nodeEnv) render(name, tmpl string) (string, error) {
	return spec.RenderNamed(name, tmpl, n.data())
}

// nodeOutcome is what executing a node produced, before it is recorded.
type nodeOutcome struct {
	status   store.NodeRunStatus
	decision store.NodeDecision
	reason   string
	class    string // error class for NodeRun.Error
	outputs  []runner.Output
	exit     *int

	lang, script    string
	argv, envKeys   []string
	stdout, stderr  string
	routableFailure bool // a failed outcome that the error port may catch
}

func failure(class, reason string) nodeOutcome {
	return nodeOutcome{status: store.NodeFailed, decision: store.DecisionError, class: class, reason: reason, routableFailure: true}
}

// execNode runs one claimed node run to completion and routes its outputs.
func (e *Engine) execNode(parent context.Context, nr *store.NodeRun) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	fr, err := e.st.GetFlowRun(e.dbCtx, nr.FlowRunID)
	if err != nil {
		e.log.Error("engine: load flow run", "node_run", nr.ID, "err", err)
		e.failNodeRun(nr, err.Error())
		return
	}
	// Test runs (assist flow tests) are not live work: the kill switch does not cancel them.
	if fr.Kind == store.KindTest {
		defer e.trackExempt(nr.ID, cancel)()
	} else {
		defer e.track(nr.ID, cancel)()
	}
	if fr.Status.Terminal() {
		e.skipNode(nr, "flow run "+string(fr.Status))
		return
	}
	ne, err := e.newNodeEnv(fr, nr)
	if err != nil {
		e.failNodeRun(nr, err.Error())
		e.reconcileFlowRun(fr.ID)
		return
	}
	var out nodeOutcome
	switch {
	case ctx.Err() != nil:
		out = cancelledOutcome(ctx) // cancelled between claim and start
	case ne.node.Type == spec.NodeCommand:
		out = e.runCommand(ctx, ne)
	case ne.node.Type == spec.NodeTransform:
		out = e.runTransform(ctx, ne)
	case ne.node.Type == spec.NodeInbox:
		out = e.runInbox(ne)
	case ne.node.Type == spec.NodeAgent && e.assistTestOf(fr.ID) != nil:
		out = nodeOutcome{status: store.NodeSkipped, decision: store.DecisionSkipped, reason: assistTestAgentSkip}
	case ne.node.Type == spec.NodeAgent:
		out = e.runAgent(ctx, ne)
	default:
		out = failure(errConfig, fmt.Sprintf("unknown node type %q", ne.node.Type))
	}
	e.finishNode(ne, out)
}

func (e *Engine) failNodeRun(nr *store.NodeRun, reason string) {
	now := time.Now().UTC()
	nr.Status, nr.Decision, nr.Error, nr.Reason, nr.EndedAt = store.NodeFailed, store.DecisionError, errConfig, reason, &now
	if err := e.st.UpdateNodeRun(e.dbCtx, nr); err != nil {
		e.log.Error("engine: fail node run", "node_run", nr.ID, "err", err)
	}
}

func (e *Engine) newNodeEnv(fr *store.FlowRun, nr *store.NodeRun) (*nodeEnv, error) {
	flow, err := e.flowFor(fr)
	if err != nil {
		return nil, err
	}
	node := flow.NodeByID(nr.NodeID)
	if node == nil {
		return nil, fmt.Errorf("flow %s has no node %q", fr.FlowID, nr.NodeID)
	}
	ne := &nodeEnv{e: e, snap: e.snapshot(), fr: fr, flow: flow, node: node, nr: nr}
	ne.input = []byte(strings.TrimSpace(e.readContent(nr.Stdin)))
	ne.event = ne.input
	if fr.EventID != "" {
		ev, err := e.st.GetEvent(e.dbCtx, fr.EventID)
		if err != nil {
			return nil, fmt.Errorf("load event %s: %w", fr.EventID, err)
		}
		if ne.event, err = e.eventPayload(ev); err != nil {
			return nil, fmt.Errorf("read event %s: %w", fr.EventID, err)
		}
	}
	ne.inputV = decodeOrString(ne.input)
	ne.eventV = decodeOrString(ne.event)
	return ne, nil
}

// decodeOrString decodes JSON text; text that is not JSON is passed as a string.
func decodeOrString(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	v, err := decodeJSON(b)
	if err != nil {
		return string(b)
	}
	return v
}

// runCommand executes a command node under the §4 contract.
func (e *Engine) runCommand(ctx context.Context, ne *nodeEnv) nodeOutcome {
	node := ne.node
	if ne.fr.DryRun && node.SideEffect {
		return nodeOutcome{status: store.NodeSkipped, decision: store.DecisionSkipped, reason: "skipped:dry_run"}
	}
	cwd := ""
	if node.Cwd != nil {
		var err error
		if cwd, err = ne.render("cwd", node.Cwd.Path); err != nil {
			return failure(errTemplate, err.Error())
		}
		if cwd, err = agent.ExpandHome(cwd); err != nil {
			return failure(errConfig, err.Error())
		}
	}
	env, cleanup, err := runner.BuildEnv(runner.EnvOptions{
		Path:     e.opt.BasePATH,
		EnvFile:  ne.snap.env,
		Event:    ne.event,
		RunID:    ne.fr.ID,
		EventID:  ne.fr.EventID,
		Flow:     ne.fr.FlowID,
		Node:     node.ID,
		StateDir: filepath.Join(e.data, "state", ne.fr.FlowID, node.ID),
		DryRun:   ne.fr.DryRun,
	})
	if err != nil {
		return failure(errStart, err.Error())
	}
	defer cleanup()
	red := ne.snap.red
	out := nodeOutcome{lang: node.Lang, script: red.String(node.Script), envKeys: sortedKeys(env)}
	if node.Lang == spec.LangExec {
		out.script = ""
	}
	res, runErr := runner.Run(ctx, runner.Spec{
		Lang:    runner.Lang(node.Lang),
		Script:  node.Script,
		Command: node.Command,
		Cwd:     cwd,
		Env:     env,
		BaseEnv: map[string]string{},
		Stdin:   ne.input,
		Timeout: node.Timeout.Std(),
	})
	if runErr != nil {
		if ctx.Err() != nil {
			return cancelledOutcome(ctx)
		}
		f := failure(errStart, red.String(runErr.Error()))
		f.lang, f.script, f.envKeys = out.lang, out.script, out.envKeys
		return f
	}
	res = red.Result(res)
	out.argv = res.Argv
	out.stdout, out.stderr = string(res.Stdout), string(res.Stderr)
	code := res.ExitCode
	out.exit = &code

	if res.Killed && !res.TimedOut {
		c := cancelledOutcome(ctx)
		c.lang, c.script, c.argv, c.envKeys, c.stdout, c.stderr, c.exit = out.lang, out.script, out.argv, out.envKeys, out.stdout, out.stderr, out.exit
		return c
	}
	oc := res.Outcome(node.Ports)
	switch oc.Decision {
	case runner.DecisionError:
		out.status, out.decision, out.routableFailure = store.NodeFailed, store.DecisionError, true
		out.class, out.reason = classifyRunnerError(oc.Error), oc.Error
		if out.class == errExitNonzero {
			if tail := lastLine(out.stderr); tail != "" {
				out.reason += ": " + truncate(tail, 200)
			}
		}
	case runner.DecisionDropped:
		out.status, out.decision, out.reason = store.NodeDone, store.DecisionDropped, "no output: the node filtered this item out"
	default:
		out.status, out.decision = store.NodeDone, store.DecisionEmitted
		out.outputs = oc.Outputs
		out.reason = joinReasons(oc.Outputs)
	}
	return out
}

// nodeRunType is the type recorded on a node's runs. Transform nodes run as
// jq command nodes (same pool, same record), so they are stored as commands.
func nodeRunType(n *spec.Node) store.NodeType {
	if n.Type == spec.NodeTransform {
		return store.NodeCommand
	}
	return store.NodeType(n.Type)
}

// runTransform compiles a transform node's steps to jq and runs the program
// exactly like a jq command node, so timeouts, redaction and the NodeRun
// record (script snapshot = the compiled jq) are the same. A dropped item
// gets a plain-language reason naming the step and condition.
func (e *Engine) runTransform(ctx context.Context, ne *nodeEnv) nodeOutcome {
	prog, err := transform.Compile(ne.node.Steps)
	if err != nil {
		return failure(errConfig, err.Error())
	}
	cmd := *ne.node
	cmd.Type, cmd.Lang, cmd.Script, cmd.Steps = spec.NodeCommand, spec.LangJQ, prog, nil
	cmd.Ports = transform.Ports(ne.node.Steps)
	sub := *ne
	sub.node = &cmd
	out := e.runCommand(ctx, &sub)
	if out.decision == store.DecisionDropped {
		run := transform.NewRunner(e.opt.BasePATH, ne.node.Timeout.Std())
		if reason := transform.DropReason(ctx, ne.node.Steps, ne.input, run); reason != "" {
			out.reason = reason
		}
	}
	return out
}

// cancelledOutcome is the outcome of a node whose context was cancelled: killed
// by the kill switch, or interrupted by shutdown.
func cancelledOutcome(ctx context.Context) nodeOutcome {
	if errors.Is(context.Cause(ctx), agent.ErrKilled) {
		return nodeOutcome{status: store.NodeKilled, decision: store.DecisionError, class: errKilled, reason: "killed by the kill switch"}
	}
	return nodeOutcome{status: store.NodeInterrupted, decision: store.DecisionError, class: errInterrupted, reason: "daemon stopped"}
}

func classifyRunnerError(msg string) string {
	switch {
	case msg == "timeout":
		return errTimeout
	case msg == "killed":
		return errKilled
	case strings.HasPrefix(msg, "exit status"):
		return errExitNonzero
	case strings.Contains(msg, "is not declared on this node"):
		return errUndeclaredPort
	default:
		return errInvalidOutput
	}
}

func joinReasons(outs []runner.Output) string {
	var rs []string
	for _, o := range outs {
		if o.Reason != "" && !slices.Contains(rs, o.Reason) {
			rs = append(rs, o.Reason)
		}
	}
	return strings.Join(rs, "; ")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// runInbox renders an inbox node's templates and creates the inbox item. The
// input passes through on port out.
func (e *Engine) runInbox(ne *nodeEnv) nodeOutcome {
	n := ne.node
	it := &store.InboxItem{
		SourceType: store.SourceNodeRun, NodeRunID: ne.nr.ID, FlowRunID: ne.fr.ID,
		Priority: store.Priority(n.Priority), DryRun: ne.fr.DryRun,
	}
	fields := []struct {
		name, tmpl string
		dst        *string
	}{
		{"title", n.Title, &it.Title},
		{"summary_md", n.SummaryMD, &it.SummaryMD},
		{"suggested_reply", n.SuggestedReply, &it.SuggestedReply},
	}
	for _, f := range fields {
		v, err := ne.render(f.name, f.tmpl)
		if err != nil {
			return failure(errTemplate, err.Error())
		}
		*f.dst = ne.snap.red.String(v)
	}
	for i, l := range n.Links {
		label, err := ne.render(fmt.Sprintf("links[%d].label", i), l.Label)
		if err != nil {
			return failure(errTemplate, err.Error())
		}
		url, err := ne.render(fmt.Sprintf("links[%d].url", i), l.URL)
		if err != nil {
			return failure(errTemplate, err.Error())
		}
		it.Links = append(it.Links, store.InboxLink{Label: ne.snap.red.String(label), URL: ne.snap.red.String(url)})
	}
	if strings.TrimSpace(it.Title) == "" {
		return failure(errTemplate, "title rendered empty")
	}
	at := e.assistTestOf(ne.fr.ID)
	if at != nil {
		at.recordInbox(ne.nr.ID, it)
	} else if err := e.addInbox(ne.snap, it); err != nil {
		return failure(errConfig, "create inbox item: "+err.Error())
	}
	payload := ne.input
	if len(payload) == 0 {
		payload = []byte("null")
	}
	return nodeOutcome{
		status: store.NodeDone, decision: store.DecisionEmitted, reason: inboxReason(at != nil, it),
		outputs: []runner.Output{{Port: spec.PortOut, Payload: payload}},
	}
}

// addInbox stores an inbox item and notifies for those at or above the
// configured priority threshold.
func (e *Engine) addInbox(snap *snapshot, it *store.InboxItem) error {
	if err := e.st.CreateInboxItem(e.dbCtx, it); err != nil {
		return err
	}
	if priorityRank(string(it.Priority)) >= priorityRank(snap.cfg.NotifyMinPriority) {
		e.notify(core.Notification{
			Kind:  "inbox",
			Title: it.Title,
			Body:  truncate(firstLine(it.SummaryMD), 200),
			URL:   e.url(InboxPath(it.ID)),
		})
	}
	return nil
}

func priorityRank(p string) int {
	return slices.Index(spec.Priorities, p)
}

// finishNode records a node run's outcome together with the node runs its
// outputs start (one transaction, store.FinishNodeRun) and then updates the
// flow run. Recording the two separately would let another node of the run
// finish in between, see every node finished, and end the flow run before
// the follow-ups are queued; they would then be skipped as belonging to a
// finished run.
func (e *Engine) finishNode(ne *nodeEnv, out nodeOutcome) {
	nr, fr := ne.nr, ne.fr
	at := e.assistTestOf(fr.ID)
	if out.routableFailure && out.status == store.NodeFailed && routes(fr, at) && hasEdge(ne.flow, nr.NodeID, spec.PortError) {
		payload, _ := json.Marshal(map[string]any{
			"input": json.RawMessage(nonEmptyJSON(ne.input)), "error": out.class, "reason": out.reason,
			"exit_code": out.exit, "stderr": truncate(out.stderr, 4000),
		})
		out.status = store.NodeDone
		out.outputs = []runner.Output{{Port: spec.PortError, Payload: payload}}
	}
	var next []*store.NodeRun
	if out.status == store.NodeDone && routes(fr, at) && len(out.outputs) > 0 {
		var err error
		if next, err = e.successors(nr, ne.flow, out.outputs); err != nil {
			e.log.Error("engine: route node outputs", "node_run", nr.ID, "err", err)
			out.status, out.decision, out.class, out.reason = store.NodeFailed, store.DecisionError, errConfig, "route outputs: "+err.Error()
		}
	}
	now := time.Now().UTC()
	nr.Status, nr.Decision, nr.Reason, nr.Error = out.status, out.decision, out.reason, out.class
	nr.Lang, nr.Script, nr.Argv, nr.EnvKeys, nr.ExitCode = out.lang, out.script, out.argv, out.envKeys, out.exit
	if out.stdout != "" || out.stderr != "" {
		nr.Stdout, nr.Stderr = store.Inline(out.stdout), store.Inline(out.stderr)
	}
	if at != nil {
		at.recordOutputs(nr.ID, out.outputs)
	}
	nr.OutputCount = len(out.outputs)
	nr.Ports = distinctPorts(out.outputs)
	if out.status != store.NodeAwaitingApproval {
		nr.EndedAt = &now
	}
	if nr.StartedAt != nil {
		d := now.Sub(*nr.StartedAt).Milliseconds()
		nr.DurationMS = &d
	}
	if err := e.st.FinishNodeRun(e.dbCtx, nr, next); err != nil {
		e.log.Error("engine: record node run", "node_run", nr.ID, "err", err)
		if len(next) > 0 {
			// Neither the node nor its follow-ups were stored.
			e.failNodeRun(nr, "route outputs: "+err.Error())
		}
	}
	e.reconcileFlowRun(fr.ID)
	if out.status == store.NodeAwaitingApproval {
		// An approval decided while the node was still running could not be
		// applied (the node was not parked yet); apply it now.
		e.applyProposalDecision(nr.ID)
	}
	e.wake()
}

func nonEmptyJSON(b []byte) []byte {
	if len(b) == 0 {
		return []byte("null")
	}
	return b
}

func distinctPorts(outs []runner.Output) []string {
	ports := []string{}
	for _, o := range outs {
		if !slices.Contains(ports, o.Port) {
			ports = append(ports, o.Port)
		}
	}
	return ports
}
