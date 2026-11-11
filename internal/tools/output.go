package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"hal9001/internal/outschema"
	"hal9001/internal/spec"
)

// OutputToolName is the tool of an agent node with output: note: it returns the inbox note as arguments.
const OutputToolName = "submit_output"

// Emission is one valid call of an output tool: the port it leads to ("" for submit_output) and its arguments.
type Emission struct {
	Port   string
	Output json.RawMessage
}

// outTool is one tool of a sink with the schema its calls are validated against.
type outTool struct {
	name, port, description string
	schema                  *outschema.Schema
}

// OutputSink collects the results an agent run returns through its output tools: submit_output (output: note) or one
// emit_<port> tool per declared port of the node that has an outgoing edge. A run's RunSpec.Output makes the session
// expose the tools; the engine reads the emissions from the sink when the run ends. Calls are validated against the
// tool's schema; an invalid call is a tool error and records nothing. With one result (the default) the last valid call
// replaces any earlier one; with many every valid call is kept.
type OutputSink struct {
	tools    []outTool
	required bool
	many     bool
	// followUp is the one reminder sent when a turn ended without a valid call.
	followUp string
	// resumeNote is appended to the "Approved. Execute the plan." prompt of an approved proposal.
	resumeNote string
	preamble   string

	mu        sync.Mutex
	emissions []Emission
	lastErr   string
}

// NewNoteSink returns the sink of an agent node with output: note: the submit_output tool with the note schema, preferred
// but not required (the final message is still parsed when it is not called).
func NewNoteSink() *OutputSink {
	return &OutputSink{
		tools: []outTool{{
			name: OutputToolName, schema: outschema.Note(),
			description: "Return this run's final result as structured arguments (preferred over writing it as JSON in your final message). " +
				"Call it exactly once, when you are done. " +
				"A call that does not match the input schema is refused with the path and reason of each mismatch; fix the arguments and call again. " +
				"The last valid call counts.",
		}},
		preamble: "result: call submit_output once with your final result (preferred); ending your final message with the JSON object is still accepted.",
	}
}

// NewPortSink returns the sink of an agent node with declared ports: one emit_<port> tool per entry of emits (the
// ports that have an outgoing edge). A run that ends without a valid call is reminded once and then fails. It reports
// an error for a schema that does not compile (validation catches that earlier).
func NewPortSink(emits []spec.EmitTool, many bool) (*OutputSink, error) {
	s := &OutputSink{required: true, many: many}
	names := make([]string, 0, len(emits))
	for _, t := range emits {
		sc, err := outschema.Compile(t.Schema)
		if err != nil {
			return nil, fmt.Errorf("port %s: schema: %w", t.Port, err)
		}
		s.tools = append(s.tools, outTool{name: t.Name, port: t.Port, schema: sc, description: t.ToolDescription()})
		names = append(names, t.Name)
	}
	s.followUp = "You must finish by calling one of: " + strings.Join(names, ", ") + "."
	s.resumeNote = spec.OutputsSection(emits, many)
	s.preamble = "result: finish by calling " + oneOrMore(many) + " of " + strings.Join(names, ", ") + "; text in your final message is not used as the result."
	return s, nil
}

func oneOrMore(many bool) string {
	if many {
		return "one or more"
	}
	return "one"
}

// Required reports that the run fails without a valid call.
func (o *OutputSink) Required() bool { return o.required }

// Many reports that every valid call is a separate emission.
func (o *OutputSink) Many() bool { return o.many }

// FollowUpPrompt is the one reminder sent when a turn ended without a valid call.
func (o *OutputSink) FollowUpPrompt() string { return o.followUp }

// ResumeNote is appended to the prompt that executes an approved plan, so the agent knows how to return its result.
func (o *OutputSink) ResumeNote() string { return o.resumeNote }

// PreambleLine is the line of the hal context block that tells the agent how the result is returned.
func (o *OutputSink) PreambleLine() string { return o.preamble }

// Emissions returns the valid calls in call order (with one result: at most the last one).
func (o *OutputSink) Emissions() []Emission {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Emission(nil), o.emissions...)
}

// Done reports that at least one valid call was made.
func (o *OutputSink) Done() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.emissions) > 0
}

// Value is the first valid submission (compact JSON), or nil. It is the result of submit_output.
func (o *OutputSink) Value() json.RawMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.emissions) == 0 {
		return nil
	}
	return o.emissions[0].Output
}

// LastError is the reason the latest invalid call was refused; empty when no call was refused since the last valid one.
func (o *OutputSink) LastError() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastErr
}

// ToolNames lists the names of the sink's tools.
func (o *OutputSink) ToolNames() []string {
	names := make([]string, len(o.tools))
	for i, t := range o.tools {
		names[i] = t.name
	}
	return names
}

func (o *OutputSink) registered() []Tool {
	out := make([]Tool, len(o.tools))
	for i := range o.tools {
		t := &o.tools[i]
		out[i] = Tool{
			Name: t.name, Description: t.description, InputSchema: t.schema.Doc(),
			Run: func(_ context.Context, c Call) (Result, error) { return o.run(t, c) },
		}
	}
	return out
}

func (o *OutputSink) run(t *outTool, c Call) (Result, error) {
	if err := t.schema.Validate(c.Args); err != nil {
		o.mu.Lock()
		o.lastErr = err.Error()
		o.mu.Unlock()
		return Result{}, fmt.Errorf("%s: the arguments do not match the input schema (fix them and call %s again):\n%w", t.name, t.name, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, c.Args); err != nil {
		return Result{}, fmt.Errorf("%s: arguments: %w", t.name, err)
	}
	e := Emission{Port: t.port, Output: append(json.RawMessage(nil), compact.Bytes()...)}
	o.mu.Lock()
	if o.many {
		o.emissions = append(o.emissions, e)
	} else {
		o.emissions = []Emission{e}
	}
	o.lastErr = ""
	o.mu.Unlock()
	if t.port == "" {
		return Result{Text: "Output accepted. Finish now; do not repeat the result in your final message."}, nil
	}
	return Result{Text: "ok"}, nil
}
