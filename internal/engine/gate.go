package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"hal9001/internal/agent"
	"hal9001/internal/notify"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/transform"
)

// The approval gate: a node with requires_approval raises a request approval (store.RequestApproval.Gate) once it is
// claimed and its input is ready, and parks as awaiting_approval, holding no concurrency slot. Approving requeues the
// node run; execNode then sees the approved gate and runs the node normally. Denying (or the node's
// approval_timeout passing) ends the node run without running the node, on port denied with the input and the
// decision; with no edge on denied the branch stops. The decision is applied by applyGateDecision, from the same
// places as an approval node's (DecideApproval, expireRequests, finishNode, reconcileRequests).

const (
	gateInputLimit  = 2000 // runes of the input shown in the approval
	gateScriptLimit = 2000 // runes of a script or instructions shown in the approval
)

// gate decides whether a claimed node may run now. It returns held=true with the outcome to record when the node
// must not run: it is parked awaiting the user's decision, was denied, or the gate itself failed. held=false means
// the gate is passed (approved earlier, or simulated approved in a test run).
func (e *Engine) gate(ne *nodeEnv) (out nodeOutcome, held bool) {
	if ne.fr.Kind == store.KindTest {
		decision := SimulateApproved
		if v, ok := e.simulations.Load(ne.fr.ID); ok {
			decision = v.(string)
		}
		if e.assistTestOf(ne.fr.ID) == nil {
			e.simulations.Delete(ne.fr.ID) // a node test runs one node; TestFlow deletes its own
		}
		if decision == SimulateApproved {
			return nodeOutcome{}, false
		}
		now := time.Now().UTC()
		return e.gateDenied(ne, "denied", "", "simulated", "", &now, "simulated approval: denied"), true
	}
	ap, err := e.gateApproval(ne.nr.ID)
	if err != nil {
		return failure(errConfig, "load approval: "+err.Error()), true
	}
	switch {
	case ap == nil:
	case ap.Status == store.ApprovalApproved:
		return nodeOutcome{}, false
	case ap.Status == store.ApprovalPending:
		return nodeOutcome{status: store.NodeAwaitingApproval, reason: "awaiting approval " + ap.ID}, true
	default: // decided while the node was running: apply it as applyGateDecision would
		return e.gateDecidedOutcome(ne, ap), true
	}

	req, err := e.gateRequest(ne)
	if err != nil {
		return failure(errTemplate, err.Error()), true
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return failure(errConfig, err.Error()), true
	}
	ap = &store.Approval{NodeRunID: ne.nr.ID, Type: store.ApprovalRequest, Request: string(raw)}
	if t := ne.node.ApprovalTimeout; t > 0 {
		exp := time.Now().UTC().Add(t.Std())
		ap.ExpiresAt = &exp
	}
	if err := e.st.CreateApproval(e.dbCtx, ap); err != nil {
		return failure(errConfig, "create approval: "+err.Error()), true
	}
	e.notify(notify.Request(ne.fr.FlowID, ap.ID, req, e.url(ApprovalPath(ap.ID))))
	return nodeOutcome{status: store.NodeAwaitingApproval, reason: "awaiting approval " + ap.ID}, true
}

// gateApproval is the approval a node run raised for its gate, or nil.
func (e *Engine) gateApproval(nodeRunID string) (*store.Approval, error) {
	apps, err := e.st.ListApprovals(e.dbCtx, store.ApprovalFilter{NodeRunID: nodeRunID, Type: store.ApprovalRequest, ListOpts: store.ListOpts{Limit: 1}})
	if err != nil || len(apps.Items) == 0 {
		return nil, err
	}
	return &apps.Items[0], nil
}

// gateRequest renders what the user is asked: the node's resolved configuration and its input.
func (e *Engine) gateRequest(ne *nodeEnv) (store.RequestApproval, error) {
	n, red := ne.node, ne.snap.red
	req := store.RequestApproval{
		Title:    fmt.Sprintf("%s: run %s?", ne.fr.FlowID, n.ID),
		Priority: store.PriorityNormal, DryRun: ne.fr.DryRun, Gate: true, Fields: []store.RequestField{},
	}
	add := func(key, value string) {
		if value != "" {
			req.Fields = append(req.Fields, store.RequestField{Key: key, Value: red.String(value)})
		}
	}
	add("flow", ne.fr.FlowID)
	add("node", fmt.Sprintf("%s (%s)", n.ID, n.Type))

	var blocks []string // markdown sections after the fields
	block := func(title, lang, text string) {
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, "**"+title+"**\n\n"+fence(lang, red.String(truncate(strings.TrimRight(text, "\n"), gateScriptLimit))))
		}
	}
	switch n.Type {
	case spec.NodeCommand:
		add("lang", n.Lang)
		if n.Lang == spec.LangExec {
			block("Command", "", strings.Join(n.Command, " "))
		} else {
			block("Script", n.Lang, n.Script)
		}
		if n.Cwd != nil {
			cwd, err := ne.render("cwd", n.Cwd.Path)
			if err != nil {
				return req, err
			}
			add("cwd", cwd)
		}
		if n.SideEffect {
			add("side effect", "yes")
		}
	case spec.NodeTransform:
		prog, err := transform.Compile(n.Steps)
		if err != nil {
			return req, err
		}
		add("steps", fmt.Sprint(len(n.Steps)))
		block("jq program", "jq", prog)
	case spec.NodeAgent:
		add("runtime", n.Runtime)
		add("mode", n.Mode)
		add("approve", n.Approve)
		add("model", n.Model)
		add("sandbox", n.Sandbox)
		add("prompt file", n.Prompt)
		prompt, err := e.renderPrompt(ne)
		if err != nil {
			return req, err
		}
		block("Instructions", "", prompt)
	case spec.NodeInbox:
		title, err := ne.render("title", n.Title)
		if err != nil {
			return req, err
		}
		summary, err := ne.render("summary_md", n.SummaryMD)
		if err != nil {
			return req, err
		}
		add("title", title)
		add("priority", n.Priority)
		block("Summary", "", summary)
	}
	if len(ne.input) > 0 {
		block("Input", "json", gateInputText(ne.input))
	}
	body := strings.Join(blocks, "\n\n")
	if ne.fr.DryRun {
		body = strings.TrimRight(dryRunNotice+"\n\n"+body, "\n")
	}
	req.BodyMD = body
	return req, nil
}

// gateInputText is the node input for display: compact JSON cut to gateInputLimit runes, other text as it is.
func gateInputText(input []byte) string {
	var v any
	if err := json.Unmarshal(input, &v); err != nil {
		return truncate(string(input), gateInputLimit)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return truncate(string(input), gateInputLimit)
	}
	return truncate(string(pretty), gateInputLimit)
}

// fence wraps text in a code fence long enough not to be closed by the text itself.
func fence(lang, text string) string {
	ticks, run := 3, 0
	for _, r := range text {
		if r == '`' {
			run++
			ticks = max(ticks, run+1)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", ticks)
	return f + lang + "\n" + text + "\n" + f
}

// gateDenied is the outcome of a denied or timed-out gate: the node did not run, and its input leaves on port
// denied with the decision.
func (e *Engine) gateDenied(ne *nodeEnv, decision, comment, by, approvalID string, at *time.Time, reason string) nodeOutcome {
	info := approvalInfo{Decision: decision, Comment: comment, DecidedBy: by, ApprovalID: approvalID}
	if at != nil {
		s := at.UTC().Format(time.RFC3339)
		info.DecidedAt = &s
	}
	payload, err := json.Marshal(map[string]any{"input": rawOrString(ne.input), "approval": info})
	if err != nil {
		return failure(errConfig, err.Error())
	}
	class := agent.ReasonDenied
	if decision == "timeout" {
		class = errApprovalTimeout
	}
	return nodeOutcome{
		status: store.NodeDone, decision: store.DecisionEmitted, class: class, reason: reason,
		outputs: []runner.Output{{Port: spec.PortDenied, Payload: ne.snap.red.Bytes(payload)}},
	}
}

// gateDecidedOutcome is the denied outcome of a decided gate approval that was not approved.
func (e *Engine) gateDecidedOutcome(ne *nodeEnv, ap *store.Approval) nodeOutcome {
	if ap.Status == store.ApprovalDenied {
		return e.gateDenied(ne, "denied", ap.Comment, ap.DecidedBy, ap.ID, ap.DecidedAt, "denied"+commentSuffix(ap.Comment))
	}
	return e.gateDenied(ne, "timeout", ap.Comment, ap.DecidedBy, ap.ID, ap.DecidedAt, "approval timed out, treated as denied")
}

// applyGateDecision carries a decided gate approval into its parked node run: approved requeues the node run (it
// runs when claimed), denied or expired ends it on port denied. It does nothing while the gate is undecided or
// already passed. The caller holds the node run's lock and has checked that it is awaiting approval.
func (e *Engine) applyGateDecision(nr *store.NodeRun) {
	ap, err := e.gateApproval(nr.ID)
	if err != nil || ap == nil || ap.Status == store.ApprovalPending {
		return
	}
	fr, err := e.st.GetFlowRun(e.dbCtx, nr.FlowRunID)
	if err != nil {
		e.log.Error("engine: load flow run", "flow_run", nr.FlowRunID, "err", err)
		return
	}
	ne, err := e.newNodeEnv(fr, nr)
	if err != nil {
		e.failNodeRun(nr, err.Error())
		e.reconcileFlowRun(fr.ID)
		return
	}
	if !ne.node.RequiresApproval {
		return
	}
	if ne.node.Type == spec.NodeAgent {
		// An agent run means the gate was passed and the node is now parked on its own approval.
		runs, err := e.st.ListAgentRuns(e.dbCtx, store.AgentRunFilter{NodeRunID: nr.ID, ListOpts: store.ListOpts{Limit: 1}})
		if err != nil || len(runs.Items) > 0 {
			return
		}
	}
	if ap.Status == store.ApprovalApproved {
		nr.Status, nr.EndedAt, nr.Reason = store.NodeQueued, nil, "approved"+commentSuffix(ap.Comment)
		if err := e.st.UpdateNodeRun(e.dbCtx, nr); err != nil {
			e.log.Error("engine: requeue approved node run", "node_run", nr.ID, "err", err)
			return
		}
		e.reconcileFlowRun(nr.FlowRunID)
		e.wake()
		return
	}
	e.finishNode(ne, e.gateDecidedOutcome(ne, ap))
}
