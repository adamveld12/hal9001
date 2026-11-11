package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"hal9001/internal/core"
	"hal9001/internal/outschema"
	"hal9001/internal/runner"
	"hal9001/internal/spec"
	"hal9001/internal/store"
)

// simulatedReasonPrefix starts the reason of a node run of an agent node that emitted through simulate_emit.
const simulatedReasonPrefix = "simulated: "

// simulatedStatus is the agent.status of the payload a simulated agent node emits.
const simulatedStatus = "simulated"

// simulatedEmit is the simulated emit registered for a node of a test run, or nil.
func (e *Engine) simulatedEmit(flowRunID, nodeID string) *core.SimulatedEmit {
	v, ok := e.simEmits.Load(flowRunID)
	if !ok {
		return nil
	}
	if em, ok := v.(map[string]core.SimulatedEmit)[nodeID]; ok {
		return &em
	}
	return nil
}

// emitToolFor finds the emit tool of the port of an agent node, or says why there is none.
func emitToolFor(flow *spec.Flow, node *spec.Node, port string) (spec.EmitTool, error) {
	if node.Type != spec.NodeAgent {
		return spec.EmitTool{}, fmt.Errorf("node %q is a %s node: only agent nodes can simulate an emit", node.ID, node.Type)
	}
	tools := flow.EmitTools(node)
	for _, t := range tools {
		if t.Port == port {
			return t, nil
		}
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Port
	}
	if len(names) == 0 {
		return spec.EmitTool{}, fmt.Errorf("node %q has no declared port with an outgoing edge to emit on", node.ID)
	}
	return spec.EmitTool{}, fmt.Errorf("%q is not a declared port with an outgoing edge of node %q (ports: %s)", port, node.ID, strings.Join(names, ", "))
}

// simulatedOutput is the output a simulated emit passes: the given one, validated, or a minimal one built from the schema.
func simulatedOutput(tool spec.EmitTool, em core.SimulatedEmit) (json.RawMessage, error) {
	schema, err := outschema.Compile(tool.Schema)
	if err != nil {
		return nil, fmt.Errorf("the schema of port %q: %w", tool.Port, err)
	}
	out := em.Output
	if len(bytes.TrimSpace(out)) == 0 {
		if out, err = outschema.Minimal(schema.Doc()); err != nil {
			return nil, fmt.Errorf("no output was given and none can be generated from the schema of port %q: %w", tool.Port, err)
		}
	}
	if err := schema.Validate(out); err != nil {
		return nil, fmt.Errorf("output does not match the schema of port %q:\n%w", tool.Port, err)
	}
	return out, nil
}

// validateSimulatedEmits checks every simulated emit against the flow (a defaulted one), so a bad request fails
// before the run starts. Errors wrap store.ErrInvalid (a request error), and name the node, port and failing paths.
func validateSimulatedEmits(flow *spec.Flow, emits map[string]core.SimulatedEmit) error {
	ids := make([]string, 0, len(emits))
	for id := range emits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		node := flow.NodeByID(id)
		if node == nil {
			return fmt.Errorf("%w: simulate_emit: the flow has no node %q", store.ErrInvalid, id)
		}
		tool, err := emitToolFor(flow, node, emits[id].Port)
		if err != nil {
			return fmt.Errorf("%w: simulate_emit: %w", store.ErrInvalid, err)
		}
		if _, err := simulatedOutput(tool, emits[id]); err != nil {
			return fmt.Errorf("%w: simulate_emit %s: %w", store.ErrInvalid, id, err)
		}
	}
	return nil
}

// runSimulatedAgent is the outcome of an agent node with a simulated emit: no agent runs, and the node emits the
// downstream payload of a real emission (status "simulated") on the port.
func (e *Engine) runSimulatedAgent(ne *nodeEnv) nodeOutcome {
	em := *e.simulatedEmit(ne.fr.ID, ne.node.ID)
	if e.assistTestOf(ne.fr.ID) == nil {
		e.simEmits.Delete(ne.fr.ID) // a node test runs one node; TestFlow deletes its own
	}
	tool, err := emitToolFor(ne.flow, ne.node, em.Port)
	if err != nil {
		return failure(errConfig, err.Error())
	}
	output, err := simulatedOutput(tool, em)
	if err != nil {
		return failure(errConfig, err.Error())
	}
	payload := e.emitPayload(ne, "", tool.Port, output, "", simulatedStatus)
	return nodeOutcome{
		status: store.NodeDone, decision: store.DecisionEmitted, reason: simulatedReasonPrefix + tool.Name,
		stdout:  string(payload),
		outputs: []runner.Output{{Port: tool.Port, Payload: payload}},
	}
}
