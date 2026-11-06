package spec

import (
	"fmt"
	"strings"
)

// Severity of a Diagnostic. Errors make a file unusable by the engine;
// warnings and info are advisory.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Stable diagnostic codes.
const (
	CodeYAMLSyntax      = "yaml_syntax"
	CodeYAMLType        = "yaml_type"
	CodeUnknownField    = "unknown_field"
	CodeEmptyFile       = "empty_file"
	CodeReadError       = "read_error"
	CodeIgnoredFile     = "ignored_file"
	CodeMissingFile     = "missing_file"
	CodeRequired        = "required"
	CodeInvalidEnum     = "invalid_enum"
	CodeInvalidValue    = "invalid_value"
	CodeInvalidID       = "invalid_id"
	CodeIDMismatch      = "id_mismatch"
	CodeDuplicateID     = "duplicate_id"
	CodeDuplicateEdge   = "duplicate_edge"
	CodeFieldNotAllowed = "field_not_allowed"
	CodeUnknownNode     = "unknown_node"
	CodeUndeclaredPort  = "undeclared_port"
	CodeReservedPort    = "reserved_port"
	CodeCycle           = "cycle"
	CodeUnreachable     = "unreachable"
	CodeUnknownSensor   = "unknown_sensor"
	CodeUnknownPrompt   = "unknown_prompt"
	CodeUnknownRuntime  = "unknown_runtime"
	CodeUnknownSandbox  = "unknown_sandbox"
	CodeUnknownStack    = "unknown_stack"
	CodeUnknownModule   = "unknown_module"
	CodeInvalidRegex    = "invalid_regex"
	CodeInvalidTemplate = "invalid_template"
	CodeInvalidSchedule = "invalid_schedule"
	CodeNoSubscription  = "no_subscription"
	CodeEmptyFlow       = "empty_flow"
	CodeDuplicateKey    = "duplicate_key"
)

// Diagnostic is one finding about one file. File is relative to the home dir
// (for example "flows/triage.yaml"); Path locates the field inside the file
// (for example "nodes[2].ports"); NodeID names the flow node when the finding
// is about one; Line is the 1-based YAML line when known.
type Diagnostic struct {
	File     string   `json:"file,omitempty"`
	Path     string   `json:"path,omitempty"`
	NodeID   string   `json:"node_id,omitempty"`
	Line     int      `json:"line,omitempty"`
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

func (d Diagnostic) String() string {
	var b strings.Builder
	b.WriteString(d.File)
	if d.Line > 0 {
		fmt.Fprintf(&b, ":%d", d.Line)
	}
	if d.Path != "" {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(d.Path)
	}
	if b.Len() > 0 {
		b.WriteString(": ")
	}
	fmt.Fprintf(&b, "%s: %s", d.Severity, d.Message)
	return b.String()
}

// HasErrors reports whether any diagnostic has error severity.
func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// withFile returns ds with File set on every entry that has none.
func withFile(file string, ds []Diagnostic) []Diagnostic {
	for i := range ds {
		if ds[i].File == "" {
			ds[i].File = file
		}
	}
	return ds
}

// collector accumulates diagnostics during validation.
type collector struct {
	ds   []Diagnostic
	node string // NodeID stamped on new diagnostics
}

func (c *collector) add(sev Severity, code, path, format string, args ...any) {
	c.ds = append(c.ds, Diagnostic{
		Path: path, NodeID: c.node, Severity: sev, Code: code,
		Message: fmt.Sprintf(format, args...),
	})
}

func (c *collector) errorf(code, path, format string, args ...any) {
	c.add(SeverityError, code, path, format, args...)
}

func (c *collector) warnf(code, path, format string, args ...any) {
	c.add(SeverityWarning, code, path, format, args...)
}
