package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"hal9001/internal/core"
	"hal9001/internal/outschema"
	"hal9001/internal/spec"
	"hal9001/internal/store"
	"hal9001/internal/tools"
)

// emitRule ends the description of every emit tool of a chat. The chat's system prompt says the same.
const emitRule = "Call this ONLY when the user explicitly asked you to send or emit the result to the flow. " +
	"It starts a new flow run after the user approves it; it does not change the original run."

// emitPayloadLimit is how much of the payload the approval text shows.
const emitPayloadLimit = 4000

// EmitTools implements assist.Host.EmitTools: the tools with which a chat that continues the agent run of fork emits
// its result back into the flow. There is one tool per entry of fork.Ports, named emit_<port> (emit_out for a node
// without declared ports). The tools are supplied through tools.RunSpec.ExtraTools.
//
// A call is validated against the port's schema as the original run used it; an invalid call is refused by policy
// (the agent can retry), a valid one waits for the user's approval as a tool_call approval whose reason shows the
// flow, node, port, the next nodes and the payload. Only an approved call runs: it starts a continuation run
// (StartContinuation) and reports its id. conversationID is the chat's assist session.
func (e *Engine) EmitTools(ctx context.Context, fork core.ForkNode, conversationID string) ([]tools.Tool, error) {
	if len(fork.Ports) == 0 {
		return nil, nil
	}
	orig, err := e.st.GetFlowRun(ctx, fork.FlowRunID)
	if err != nil {
		return nil, err
	}
	flow, err := e.flowFor(orig)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", core.ErrInvalidState, err)
	}
	node := flow.NodeByID(fork.NodeID)
	if node == nil || node.Type != spec.NodeAgent {
		return nil, fmt.Errorf("%w: agent node %q in flow %q", core.ErrUnknownRef, fork.NodeID, flow.ID)
	}
	declared := map[string]spec.EmitTool{}
	for _, t := range flow.EmitTools(node) {
		declared[t.Port] = t
	}
	out := make([]tools.Tool, 0, len(fork.Ports))
	for _, p := range fork.Ports {
		if err := checkEmitPort(flow, node, p.Name, orig.FlowHash); err != nil {
			return nil, err
		}
		et := emitSpec{port: p.Name, name: spec.EmitToolName(p.Name), description: strings.TrimSpace(p.Description)}
		if node.Ports.IsZero() {
			if et.description == "" {
				et.description = "Send the result of this node to the flow."
			}
			et.doc = outResultDoc(agentOutputFor(e.snapshot(), flow, node).mode == spec.OutputNote)
		} else {
			d, ok := declared[p.Name]
			if !ok {
				return nil, fmt.Errorf("%w: port %q of node %q", core.ErrUnknownRef, p.Name, node.ID)
			}
			// The flow's EmitTools fills a default description meant for a run that finishes through the port.
			et.description = firstNonEmpty(declDescription(node, p.Name), et.description, "Emit the result on port "+p.Name+" of this node.")
			et.doc = d.Schema
		}
		et.next = p.Next
		if len(et.next) == 0 {
			et.next = leadsToOf(flow, node.ID, p.Name)
		}
		t, err := e.emitTool(fork, conversationID, orig, et)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// emitSpec is what one emit tool is built from.
type emitSpec struct {
	port, name, description string
	next                    []string
	doc                     map[string]any
}

// outResultDoc is the input schema of emit_out, the tool of a node without declared ports: the agent's final message
// and, when the node's output is a note, the note.
func outResultDoc(withNote bool) map[string]any {
	props := map[string]any{
		"final_message": map[string]any{"type": "string", "pattern": `\S`, "description": "The result: the message the agent would end its run with."},
	}
	if withNote {
		props["note"] = outschema.NoteDoc()
	}
	return map[string]any{"type": "object", "required": []any{"final_message"}, "properties": props, "additionalProperties": false}
}

func declDescription(node *spec.Node, port string) string {
	for _, d := range node.Ports.Decls {
		if d.Name == port {
			return strings.TrimSpace(d.Description)
		}
	}
	return ""
}

// leadsToOf summarizes the nodes the edges from port of node lead to, in edge order without repeats.
func leadsToOf(flow *spec.Flow, node, port string) []string {
	var out []string
	for _, edge := range flow.Edges {
		if edge.From != node || edge.Port != port {
			continue
		}
		if to := flow.NodeByID(edge.To); to != nil {
			s := spec.LeadsTo(to)
			if !containsString(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (e *Engine) emitTool(fork core.ForkNode, conversationID string, orig *store.FlowRun, et emitSpec) (tools.Tool, error) {
	sc, err := outschema.Compile(et.doc)
	if err != nil {
		return tools.Tool{}, fmt.Errorf("port %s: schema: %w", et.port, err)
	}
	validate := func(args json.RawMessage) (json.RawMessage, error) {
		if err := sc.Validate(args); err != nil {
			return nil, fmt.Errorf("the arguments do not match the input schema (fix them and call %s again):\n%w", et.name, err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, args); err != nil {
			return nil, fmt.Errorf("arguments: %w", err)
		}
		return compact.Bytes(), nil
	}
	return tools.Tool{
		Name:        et.name,
		Description: strings.TrimSpace(et.description + " Leads to: " + strings.Join(et.next, "; ") + ". " + emitRule),
		InputSchema: sc.Doc(),
		Policy: func(args json.RawMessage) (tools.Verdict, string) {
			payload, err := validate(args)
			if err != nil {
				return tools.VerdictDeny, err.Error()
			}
			return tools.VerdictApprove, emitApprovalReason(fork, orig, et, payload)
		},
		Run: func(ctx context.Context, c tools.Call) (tools.Result, error) {
			if !strings.HasPrefix(c.Approval, "approved by user") {
				return tools.Result{}, fmt.Errorf("%s: refused: the call was not approved by the user", et.name)
			}
			payload, err := validate(c.Args)
			if err != nil {
				return tools.Result{}, fmt.Errorf("%s: %w", et.name, err)
			}
			id, err := e.StartContinuation(ctx, ContinuationRequest{
				OriginalAgentRunID: fork.AgentRunID, ConversationID: conversationID, Port: et.port, Payload: payload,
				ApprovalID: e.approvalOfCall(ctx, c),
			})
			if err != nil {
				return tools.Result{}, fmt.Errorf("%s: could not start the flow run: %w", et.name, err)
			}
			text := fmt.Sprintf(
				"ok; approved; started flow run %s (kind continuation) of flow %s from node %s on port %s. The original run %s is unchanged.",
				id, fork.FlowID, fork.NodeID, et.port, fork.FlowRunID)
			if e.isPaused() {
				text += " The kill switch is engaged, so the run waits in the queue until it is released."
			}
			return tools.Result{Text: text}, nil
		},
	}, nil
}

// approvalOfCall is the tool_call approval the user granted for c ("" when it cannot be found).
func (e *Engine) approvalOfCall(ctx context.Context, c tools.Call) string {
	if c.RecordID == "" || c.Info.AgentRunID == "" {
		return ""
	}
	page, err := e.st.ListApprovals(ctx, store.ApprovalFilter{
		AgentRunID: c.Info.AgentRunID, Type: store.ApprovalToolCall, Slim: true, ListOpts: store.ListOpts{Limit: 200},
	})
	if err != nil {
		e.log.Warn("engine: find the approval of an emit call", "agent_run", c.Info.AgentRunID, "err", err)
		return ""
	}
	for _, a := range page.Items {
		if a.PermissionRequestID == c.RecordID {
			return a.ID
		}
	}
	return ""
}

// emitApprovalReason is the text of the approval an emit call raises, shown on the chat's approval card and in the
// Inbox: what is emitted where, what runs next and the payload.
func emitApprovalReason(fork core.ForkNode, orig *store.FlowRun, et emitSpec, payload json.RawMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Emit a result to flow %s: node %s -> port %s.\n", fork.FlowID, fork.NodeID, et.port)
	if len(et.next) > 0 {
		fmt.Fprintf(&b, "Leads to: %s.\n", strings.Join(et.next, "; "))
	}
	b.WriteString("Payload:\n")
	b.WriteString(indentLines(clipPayload(payload), "  "))
	fmt.Fprintf(&b, "\nApproving starts a new flow run of %s (kind continuation) that runs the steps after this port. ", fork.FlowID)
	fmt.Fprintf(&b, "The original run %s stays unchanged.", fork.FlowRunID)
	if orig.DryRun {
		b.WriteString(" The original run was a dry run, so the new run is a dry run too.")
	}
	return b.String()
}

// clipPayload pretty-prints payload and cuts it to emitPayloadLimit runes.
func clipPayload(payload json.RawMessage) string {
	var buf bytes.Buffer
	text := string(payload)
	if json.Indent(&buf, payload, "", "  ") == nil {
		text = buf.String()
	}
	r := []rune(text)
	if len(r) <= emitPayloadLimit {
		return text
	}
	return string(r[:emitPayloadLimit]) + fmt.Sprintf("\n… (clipped: %d of %d characters shown)", emitPayloadLimit, len(r))
}

func indentLines(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
