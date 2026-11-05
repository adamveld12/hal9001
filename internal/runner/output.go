package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Decision is the recorded outcome of a node run.
type Decision string

const (
	DecisionEmitted Decision = "emitted"
	DecisionDropped Decision = "dropped"
	DecisionError   Decision = "error"
)

const (
	// DefaultPort is the port used when an output has no _port.
	DefaultPort = "out"
	// ErrorPort is implicit on every node (REQUIREMENTS §6).
	ErrorPort = "error"

	keyPort   = "_port"
	keyReason = "_reason"
)

// Output is one payload routed to a port, with reserved keys already stripped.
type Output struct {
	Port    string          `json:"port"`
	Reason  string          `json:"reason,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// Outcome is the parsed result of a node run.
type Outcome struct {
	Decision Decision
	// Outputs is set only for DecisionEmitted.
	Outputs []Output
	// Error describes the failure for DecisionError.
	Error string
}

// ParseNodeOutput applies the §4 result table to a node's stdout and exit code.
// Outputs may target any of declaredPorts plus the implicit "error" port; an empty
// declaredPorts means the node declares only the default port "out". An undeclared
// _port fails the whole node rather than emitting a partial fan-out.
func ParseNodeOutput(stdout []byte, exit int, declaredPorts []string) Outcome {
	if exit != 0 {
		return Outcome{Decision: DecisionError, Error: fmt.Sprintf("exit status %d", exit)}
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return Outcome{Decision: DecisionDropped}
	}
	values, err := splitJSONValues(stdout)
	if err != nil {
		return Outcome{Decision: DecisionError, Error: "stdout is not valid JSON or JSONL: " + err.Error()}
	}
	allowed := declaredPorts
	if len(allowed) == 0 {
		allowed = []string{DefaultPort}
	}
	outs := make([]Output, 0, len(values))
	for i, v := range values {
		out, err := buildOutput(v)
		if err != nil {
			return Outcome{Decision: DecisionError, Error: fmt.Sprintf("output %d: %v", i+1, err)}
		}
		if out.Port != ErrorPort && !slices.Contains(allowed, out.Port) {
			return Outcome{Decision: DecisionError, Error: fmt.Sprintf(
				"output %d: _port %q is not declared on this node (declared: %s)",
				i+1, out.Port, strings.Join(allowed, ", "))}
		}
		outs = append(outs, out)
	}
	return Outcome{Decision: DecisionEmitted, Outputs: outs}
}

// Outcome classifies a finished run: timeouts, kills and truncated stdout are errors
// before the stdout/exit table is consulted.
func (r Result) Outcome(declaredPorts []string) Outcome {
	switch {
	case r.TimedOut:
		return Outcome{Decision: DecisionError, Error: "timeout"}
	case r.Killed:
		return Outcome{Decision: DecisionError, Error: "killed"}
	case r.StdoutTruncated:
		return Outcome{Decision: DecisionError, Error: fmt.Sprintf(
			"stdout exceeded the output cap (%d bytes written, %d kept)", r.StdoutBytes, len(r.Stdout))}
	}
	return ParseNodeOutput(r.Stdout, r.ExitCode, declaredPorts)
}

// ParseSensorItems parses sensor stdout: a single JSON array yields its elements;
// anything else is treated as JSONL, one item per JSON value. Empty output is zero items.
func ParseSensorItems(stdout []byte) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, nil
	}
	values, err := splitJSONValues(stdout)
	if err != nil {
		return nil, fmt.Errorf("sensor stdout is not a JSON array or JSONL: %w", err)
	}
	if len(values) == 1 && values[0][0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(values[0], &items); err != nil {
			return nil, fmt.Errorf("sensor stdout array: %w", err)
		}
		for i := range items {
			items[i] = compact(items[i])
		}
		return items, nil
	}
	return values, nil
}

// splitJSONValues splits stdout into its top-level JSON values (JSONL, concatenated or
// pretty-printed), returning each compacted.
func splitJSONValues(data []byte) ([]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var values []json.RawMessage
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return values, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w (near %q)", err, snippet(data, dec.InputOffset()))
		}
		values = append(values, compact(raw))
	}
}

func compact(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

// snippet returns up to 40 bytes of data around the decoder offset for error messages.
func snippet(data []byte, off int64) string {
	start := min(max(int(off)-20, 0), len(data))
	end := min(start+40, len(data))
	return strings.ToValidUTF8(string(data[start:end]), "?")
}

// buildOutput extracts and strips _port/_reason from an object value.
func buildOutput(v json.RawMessage) (Output, error) {
	out := Output{Port: DefaultPort, Payload: v}
	if v[0] != '{' {
		return out, nil
	}
	fields, err := orderedFields(v)
	if err != nil {
		return Output{}, err
	}
	var kept []field
	reserved := false
	for _, f := range fields {
		if f.key != keyPort && f.key != keyReason {
			kept = append(kept, f)
			continue
		}
		reserved = true
		var s string
		if err := json.Unmarshal(f.value, &s); err != nil {
			return Output{}, fmt.Errorf("%s must be a string, got %s", f.key, f.value)
		}
		if f.key == keyPort {
			if s == "" {
				return Output{}, fmt.Errorf("%s must not be empty", keyPort)
			}
			out.Port = s
		} else {
			out.Reason = s
		}
	}
	if reserved {
		out.Payload = encodeFields(kept)
	}
	return out, nil
}

type field struct {
	key   string
	value json.RawMessage
}

// orderedFields reads a JSON object's members in source order (a map would reorder them).
func orderedFields(obj json.RawMessage) ([]field, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if _, err := dec.Token(); err != nil { // '{'
		return nil, err
	}
	var fields []field
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		fields = append(fields, field{key: key, value: val})
	}
	return fields, nil
}

func encodeFields(fields []field) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(f.value)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}
