package spec

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"hal9001/internal/transform"
)

// Tool parameter types.
const (
	ParamString  = "string"
	ParamNumber  = "number"
	ParamInteger = "integer"
	ParamBoolean = "boolean"
	ParamArray   = "array"
	ParamObject  = "object"
)

// ParamTypes lists the legal values of a tool parameter's type.
var ParamTypes = []string{ParamString, ParamNumber, ParamInteger, ParamBoolean, ParamArray, ParamObject}

// Tool approval modes. ApproveWhen is the object form `{when: ...}`.
const (
	ToolApproveNever  = "never"
	ToolApproveAlways = "always"
	ToolApproveWhen   = "when"
)

// ToolLanguages lists the legal values of a tool's lang (no jq: tools take
// arguments, not a stream of items).
var ToolLanguages = []string{LangBash, LangPython, LangNode, LangExec}

// DefaultToolTimeout is the timeout of a tool that sets none.
const DefaultToolTimeout = DefaultCommandTimeout

var paramNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Tool is tools/<id>.yaml: a user-defined tool an agent can call. Arguments are
// described by Params (name to parameter) or, as an override, by a raw JSON
// Schema; at most one of the two is set.
type Tool struct {
	ID          string               `yaml:"id" json:"id"`
	Description string               `yaml:"description" json:"description"`
	Params      map[string]ToolParam `yaml:"params,omitempty" json:"params,omitempty"`
	// Schema is a raw JSON Schema (an object schema) replacing Params.
	Schema  map[string]any `yaml:"schema,omitempty" json:"schema,omitempty"`
	Lang    string         `yaml:"lang" json:"lang"`
	Script  string         `yaml:"script,omitempty" json:"script,omitempty"`
	Command []string       `yaml:"command,omitempty" json:"command,omitempty"`
	// Approve is never, always or {when: conditions over the arguments}. Unset
	// means always.
	Approve       ToolApprove `yaml:"approve,omitempty" json:"approve,omitempty"`
	Timeout       Duration    `yaml:"timeout,omitempty" json:"timeout"`
	RunsInSandbox bool        `yaml:"runs_in_sandbox,omitempty" json:"runs_in_sandbox,omitempty"`
}

// ToolParam is one named argument of a tool.
type ToolParam struct {
	Type        string `yaml:"type" json:"type"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Enum        []any  `yaml:"enum,omitempty" json:"enum,omitempty"`
	Default     any    `yaml:"default,omitempty" json:"default,omitempty"`
}

// ToolApprove is a tool's approval setting. On the wire it is the string
// "never" or "always", or the object {when: {match, conditions}}.
type ToolApprove struct {
	Mode string
	// When holds the conditions of mode "when": approval is needed when they
	// match the arguments.
	When *ToolWhen
}

// ToolWhen is a condition group over a tool's arguments, in the Transform
// condition model.
type ToolWhen struct {
	Match      string                `yaml:"match,omitempty" json:"match,omitempty"`
	Conditions []transform.Condition `yaml:"conditions" json:"conditions"`
}

type toolApproveObject struct {
	When *ToolWhen `yaml:"when" json:"when"`
}

// MarshalYAML implements yaml.Marshaler.
func (a ToolApprove) MarshalYAML() (any, error) {
	if a.Mode == ToolApproveWhen {
		return toolApproveObject{When: a.When}, nil
	}
	return a.Mode, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (a *ToolApprove) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		a.Mode, a.When = s, nil
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i].Value; k != "when" {
				return fmt.Errorf("line %d: approve: unknown field %q (an object form has only when)", n.Content[i].Line, k)
			}
		}
		var o toolApproveObject
		if err := n.Decode(&o); err != nil {
			return err
		}
		a.Mode, a.When = ToolApproveWhen, o.When
		if a.When == nil {
			a.When = &ToolWhen{}
		}
		return nil
	}
	return fmt.Errorf("line %d: approve must be never, always or {when: ...}", n.Line)
}

// MarshalJSON implements json.Marshaler.
func (a ToolApprove) MarshalJSON() ([]byte, error) {
	if a.Mode == ToolApproveWhen {
		return json.Marshal(toolApproveObject{When: a.When})
	}
	return json.Marshal(a.Mode)
}

// UnmarshalJSON implements json.Unmarshaler.
func (a *ToolApprove) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		a.Mode, a.When = s, nil
		return nil
	}
	var o toolApproveObject
	if err := json.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("approve must be never, always or {when: ...}")
	}
	a.Mode, a.When = ToolApproveWhen, o.When
	if a.When == nil {
		a.When = &ToolWhen{}
	}
	return nil
}

// IsZero lets omitempty drop an unset approval in YAML and JSON.
func (a ToolApprove) IsZero() bool { return a.Mode == "" && a.When == nil }

// ApplyDefaults fills unset fields in place. It is idempotent.
func (t *Tool) ApplyDefaults() {
	if t.Timeout == 0 {
		t.Timeout = DefaultToolTimeout
	}
	if t.Approve.Mode == "" {
		t.Approve.Mode = ToolApproveAlways
	}
}

// Clone returns a copy sharing no slices or maps with t.
func (t Tool) Clone() Tool {
	t.Command = slices.Clone(t.Command)
	if t.Params != nil {
		p := make(map[string]ToolParam, len(t.Params))
		for k, v := range t.Params {
			v.Enum = slices.Clone(v.Enum)
			p[k] = v
		}
		t.Params = p
	}
	if t.Schema != nil {
		var c map[string]any
		if b, err := json.Marshal(t.Schema); err == nil && json.Unmarshal(b, &c) == nil {
			t.Schema = c
		}
	}
	if t.Approve.When != nil {
		w := *t.Approve.When
		w.Conditions = slices.Clone(w.Conditions)
		t.Approve.When = &w
	}
	return t
}

// Minimal returns a copy with default values cleared; see Config.Minimal.
func (t Tool) Minimal() Tool {
	t = t.Clone()
	if t.Timeout == DefaultToolTimeout {
		t.Timeout = 0
	}
	if t.Approve.Mode == ToolApproveAlways {
		t.Approve = ToolApprove{}
	}
	return t
}

// ParamNames returns the parameter names, sorted.
func (t *Tool) ParamNames() []string {
	names := make([]string, 0, len(t.Params))
	for k := range t.Params {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// EffectiveSchema is the JSON Schema of the tool's arguments: Schema when set,
// otherwise an object schema built from Params.
func (t *Tool) EffectiveSchema() map[string]any {
	if t.Schema != nil {
		return t.Schema
	}
	props := map[string]any{}
	required := []string{}
	for _, name := range t.ParamNames() {
		p := t.Params[name]
		prop := map[string]any{"type": p.Type}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Default != nil {
			prop["default"] = p.Default
		}
		props[name] = prop
		if p.Required {
			required = append(required, name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// ParseTool parses a tool file.
func ParseTool(b []byte) (*Tool, []Diagnostic) {
	t, ds := parseYAMLFile[Tool](b)
	if t != nil {
		t.ApplyDefaults()
	}
	return t, ds
}

// CheckTool parses and validates tool bytes; see CheckSensor for name.
func CheckTool(name string, b []byte) (*Tool, []Diagnostic) {
	t, ds := ParseTool(b)
	if t != nil {
		ds = append(ds, ValidateTool(t)...)
		ds = append(ds, checkIDMatchesName(KindTool, name, t.ID)...)
	}
	return t, withFile(KindTool.Rel(name), ds)
}

// MarshalTool renders t as YAML with defaults omitted.
func MarshalTool(t Tool) ([]byte, error) { return encodeYAML(t.Minimal()) }

// WriteTool is WriteFile for tools/<id>.yaml.
func WriteTool(home, id string, data []byte, expectedHash string) (string, error) {
	return WriteFile(home, KindTool, id, data, expectedHash)
}

// Tool returns the tool file with the given id, or nil.
func (w *Workspace) Tool(id string) *File[Tool] { return find(w.Tools, id) }

// ToolEnvFor returns the per-tool env file of a tool, or nil.
func (w *Workspace) ToolEnvFor(id string) *File[Env] { return find(w.ToolEnv, id) }

// ValidateTool checks a Tool (not its id against a file name; see CheckTool).
func ValidateTool(t *Tool) []Diagnostic {
	g := t.Clone()
	g.ApplyDefaults()
	c := &collector{}
	c.id("id", "id", g.ID)
	if strings.TrimSpace(g.Description) == "" {
		c.errorf(CodeRequired, "description", "description is required: the agent reads it to decide when to call the tool")
	}
	c.enum("lang", g.Lang, ToolLanguages, true)
	if g.Lang == LangJQ {
		c.errorf(CodeInvalidEnum, "lang", "lang jq is not valid for tools (one of %s)", strings.Join(ToolLanguages, " | "))
	} else {
		c.runSpec("", g.Lang, g.Script, g.Command)
	}
	c.positiveDuration("timeout", g.Timeout, "timeout")

	if len(g.Params) > 0 && g.Schema != nil {
		c.errorf(CodeFieldNotAllowed, "schema", "schema replaces params: set one of them")
	}
	for _, name := range g.ParamNames() {
		validateToolParam(c, name, g.Params[name])
	}
	if g.Schema != nil {
		if typ, _ := g.Schema["type"].(string); typ != "object" {
			c.errorf(CodeInvalidValue, "schema.type", `the schema of a tool's arguments must have "type": "object"`)
		}
	}
	validateToolApprove(c, g.Approve)
	return c.ds
}

func validateToolParam(c *collector, name string, p ToolParam) {
	path := "params." + name
	if !paramNameRE.MatchString(name) {
		c.errorf(CodeInvalidID, path, "parameter name %q is invalid: use letters, digits and _, not starting with a digit (it becomes ARG_%s)", name, strings.ToUpper(name))
		return
	}
	c.enum(path+".type", p.Type, ParamTypes, true)
	if !slices.Contains(ParamTypes, p.Type) {
		return
	}
	for i, v := range p.Enum {
		if !valueHasType(v, p.Type) {
			c.errorf(CodeInvalidValue, fmt.Sprintf("%s.enum[%d]", path, i), "enum value %v is not of type %s", v, p.Type)
		}
	}
	if p.Default != nil {
		switch {
		case !valueHasType(p.Default, p.Type):
			c.errorf(CodeInvalidValue, path+".default", "default %v is not of type %s", p.Default, p.Type)
		case len(p.Enum) > 0 && !containsValue(p.Enum, p.Default):
			c.errorf(CodeInvalidValue, path+".default", "default %v is not one of the enum values", p.Default)
		}
	}
	if p.Required && p.Default != nil {
		c.warnf(CodeInvalidValue, path+".required", "a required parameter never uses its default")
	}
}

// valueHasType reports whether a YAML- or JSON-decoded value fits a JSON
// Schema type.
func valueHasType(v any, typ string) bool {
	switch typ {
	case ParamString:
		_, ok := v.(string)
		return ok
	case ParamBoolean:
		_, ok := v.(bool)
		return ok
	case ParamNumber:
		switch v.(type) {
		case int, int64, uint64, float64:
			return true
		}
	case ParamInteger:
		switch x := v.(type) {
		case int, int64, uint64:
			return true
		case float64:
			return x == float64(int64(x))
		}
	case ParamArray:
		_, ok := v.([]any)
		return ok
	case ParamObject:
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}

func containsValue(list []any, v any) bool {
	for _, e := range list {
		if fmt.Sprint(e) == fmt.Sprint(v) {
			return true
		}
	}
	return false
}

func validateToolApprove(c *collector, a ToolApprove) {
	switch a.Mode {
	case ToolApproveNever, ToolApproveAlways:
	case ToolApproveWhen:
		if a.When == nil || len(a.When.Conditions) == 0 {
			c.errorf(CodeRequired, "approve.when.conditions", "approve.when needs at least one condition")
			return
		}
		step := transform.Step{Type: transform.StepFilter, Match: a.When.Match, Conditions: a.When.Conditions}
		for _, is := range transform.Validate([]transform.Step{step}) {
			sev := SeverityError
			if is.Severity == transform.SeverityWarning {
				sev = SeverityWarning
			}
			path := strings.TrimPrefix(is.Path, "steps[0]")
			c.add(sev, is.Code, "approve.when"+path, "%s", is.Message)
		}
	default:
		c.errorf(CodeInvalidEnum, "approve", "approve %q is not valid (one of never | always | {when: conditions})", a.Mode)
	}
}
