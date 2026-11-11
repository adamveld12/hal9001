package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"hal9001/internal/outschema"
)

// OutputToolName is the tool an agent node's run uses to return a structured result.
const OutputToolName = "submit_output"

// OutputSink collects the result an agent run submits through submit_output. A run's RunSpec.Output makes the
// session expose the tool; the engine reads the result from the sink when the run ends. Calls are validated against
// the schema; a valid call replaces any earlier one (the last valid call counts) and an invalid call leaves the
// earlier result in place.
type OutputSink struct {
	schema   *outschema.Schema
	required bool

	mu      sync.Mutex
	value   json.RawMessage
	lastErr string
}

// NewOutputSink returns a sink for schema. With required, the tool text tells the agent that the run fails without a
// valid call; otherwise the call is described as preferred over writing the result in the final message.
func NewOutputSink(schema *outschema.Schema, required bool) *OutputSink {
	return &OutputSink{schema: schema, required: required}
}

// Schema is the schema calls are validated against.
func (o *OutputSink) Schema() *outschema.Schema { return o.schema }

// Value is the latest valid submission (compact JSON), or nil when there is none.
func (o *OutputSink) Value() json.RawMessage {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.value
}

// LastError is the reason the latest invalid call was refused; empty when no call was refused since the last valid one.
func (o *OutputSink) LastError() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lastErr
}

// Required reports that the run fails without a valid call.
func (o *OutputSink) Required() bool { return o.required }

// FollowUpPrompt is the one reminder sent when a turn ended without a valid submission.
func (o *OutputSink) FollowUpPrompt() string {
	return "You must call " + OutputToolName + " with your final result: your turn ended without a valid call, and the result is only taken from that call. " +
		"Call " + OutputToolName + " now, once, with arguments matching this JSON Schema:\n" + o.schema.JSON() +
		"\nDo not answer in text."
}

// ResumeNote tells an agent whose approved plan is executed that the result is returned through the tool.
func (o *OutputSink) ResumeNote() string {
	return "When you are done, return your final result by calling the " + OutputToolName +
		" tool once, with arguments matching its input schema. Text in your final message is not used as the result."
}

func (o *OutputSink) description() string {
	if o.required {
		return "Return this run's final structured result. Call it exactly once, when you are done, with the result as the arguments: " +
			"the run fails without a valid call, and your final message text is not used as the result. " +
			"A call that does not match the input schema is refused with the path and reason of each mismatch; fix the arguments and call again. " +
			"The last valid call counts."
	}
	return "Return this run's final result as structured arguments (preferred over writing it as JSON in your final message). " +
		"Call it exactly once, when you are done. " +
		"A call that does not match the input schema is refused with the path and reason of each mismatch; fix the arguments and call again. " +
		"The last valid call counts."
}

func (o *OutputSink) tool() Tool {
	return Tool{
		Name:        OutputToolName,
		Description: o.description(),
		InputSchema: o.schema.Doc(),
		Run:         o.run,
	}
}

func (o *OutputSink) run(_ context.Context, c Call) (Result, error) {
	if err := o.schema.Validate(c.Args); err != nil {
		o.mu.Lock()
		o.lastErr = err.Error()
		o.mu.Unlock()
		return Result{}, fmt.Errorf("%s: the arguments do not match the input schema (fix them and call %s again):\n%w", OutputToolName, OutputToolName, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, c.Args); err != nil {
		return Result{}, fmt.Errorf("%s: arguments: %w", OutputToolName, err)
	}
	o.mu.Lock()
	o.value = append(json.RawMessage(nil), compact.Bytes()...)
	o.lastErr = ""
	o.mu.Unlock()
	return Result{Text: "Output accepted. Finish now; do not repeat the result in your final message."}, nil
}
