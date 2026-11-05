// Package transform turns a list of plain-language steps (the `transform` node
// type) into a jq program. It is the single source of truth for what a step
// means: the engine runs the compiled program, the preview API runs it on
// sample data, and the UI shows Describe's sentences.
//
// The package validates steps, compiles them (Compile), describes them in
// plain words (Describe), and can run them step by step through an injected
// jq runner (Preview, DropReason). Everything user-supplied is JSON-encoded
// into the program; no user text is ever spliced into jq syntax.
package transform

import "slices"

// Step kinds.
const (
	StepFilter     = "filter"
	StepRoute      = "route"
	StepPick       = "pick"
	StepSet        = "set"
	StepRemove     = "remove"
	StepSplit      = "split"
	StepSort       = "sort"
	StepLimit      = "limit"
	StepUnique     = "unique"
	StepAggregate  = "aggregate"
	StepFilterList = "filter_list"
)

// StepKinds lists every step kind in the order the UI offers them.
var StepKinds = []string{
	StepFilter, StepRoute, StepPick, StepSet, StepRemove, StepSplit,
	StepSort, StepLimit, StepUnique, StepAggregate, StepFilterList,
}

// Match modes of a condition group.
const (
	MatchAll = "all"
	MatchAny = "any"
)

// Sort orders.
const (
	OrderAsc  = "asc"
	OrderDesc = "desc"
)

// Aggregate operations.
const (
	OpCount      = "count"
	OpSum        = "sum"
	OpAvg        = "avg"
	OpMin        = "min"
	OpMax        = "max"
	OpCollect    = "collect"
	OpGroupCount = "group_count"
)

// AggregateOps lists every aggregate operation.
var AggregateOps = []string{OpCount, OpSum, OpAvg, OpMin, OpMax, OpCollect, OpGroupCount}

// Value kinds of a set step.
const (
	ValueConstant = "constant"
	ValueField    = "field"
	ValueTemplate = "template"
	ValueFunction = "function"
)

// Functions of a function value.
const (
	FuncLowercase = "lowercase"
	FuncUppercase = "uppercase"
	FuncTrim      = "trim"
	FuncLength    = "length"
	FuncJoin      = "join"
	FuncFirst     = "first"
	FuncLast      = "last"
	FuncSplit     = "split"
	FuncToNumber  = "to_number"
	FuncToString  = "to_string"
	FuncDefault   = "default"
)

// Functions lists every function a set step can apply.
var Functions = []string{
	FuncLowercase, FuncUppercase, FuncTrim, FuncLength, FuncJoin, FuncFirst,
	FuncLast, FuncSplit, FuncToNumber, FuncToString, FuncDefault,
}

// Condition operators.
const (
	OpEquals      = "equals"
	OpNotEquals   = "not_equals"
	OpContains    = "contains"
	OpNotContains = "not_contains"
	OpStartsWith  = "starts_with"
	OpEndsWith    = "ends_with"
	OpMatches     = "matches"
	OpGT          = "gt"
	OpGTE         = "gte"
	OpLT          = "lt"
	OpLTE         = "lte"
	OpExists      = "exists"
	OpNotExists   = "not_exists"
	OpIsEmpty     = "is_empty"
	OpIsNotEmpty  = "is_not_empty"
	OpIsTrue      = "is_true"
	OpIsFalse     = "is_false"
	OpIn          = "in"
)

// Operators lists every condition operator.
var Operators = []string{
	OpEquals, OpNotEquals, OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpMatches,
	OpGT, OpGTE, OpLT, OpLTE, OpExists, OpNotExists, OpIsEmpty, OpIsNotEmpty, OpIsTrue, OpIsFalse, OpIn,
}

// Step is one plain-language step of a transform node. Type selects which of
// the other fields apply (see stepFields); Validate rejects the rest.
//
//	filter:      match, conditions, keep
//	route:       rules, default_port
//	pick:        fields, rename, flatten
//	set:         field, value
//	remove:      fields
//	split:       field, as, keep_parent
//	sort:        field, by, order
//	limit:       field, count
//	unique:      field, by
//	aggregate:   field, into, op, of, group_by
//	filter_list: field, match, conditions, keep
type Step struct {
	Type string `yaml:"type" json:"type"`

	// filter, filter_list, and each route rule
	Match      string      `yaml:"match,omitempty" json:"match,omitempty"`
	Conditions []Condition `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	// Keep is true (the default) to keep what matches, false to drop it.
	Keep *bool `yaml:"keep,omitempty" json:"keep,omitempty"`

	// route
	Rules       []Rule `yaml:"rules,omitempty" json:"rules,omitempty"`
	DefaultPort string `yaml:"default_port,omitempty" json:"default_port,omitempty"`

	// pick, remove
	Fields  []string          `yaml:"fields,omitempty" json:"fields,omitempty"`
	Rename  map[string]string `yaml:"rename,omitempty" json:"rename,omitempty"`
	Flatten bool              `yaml:"flatten,omitempty" json:"flatten,omitempty"`

	// set, split, sort, limit, unique, aggregate, filter_list
	Field string `yaml:"field,omitempty" json:"field,omitempty"`
	Value *Value `yaml:"value,omitempty" json:"value,omitempty"`

	// split
	As         string `yaml:"as,omitempty" json:"as,omitempty"`
	KeepParent bool   `yaml:"keep_parent,omitempty" json:"keep_parent,omitempty"`

	// sort, unique
	By    string `yaml:"by,omitempty" json:"by,omitempty"`
	Order string `yaml:"order,omitempty" json:"order,omitempty"`

	// limit
	Count *int `yaml:"count,omitempty" json:"count,omitempty"`

	// aggregate
	Into    string `yaml:"into,omitempty" json:"into,omitempty"`
	Op      string `yaml:"op,omitempty" json:"op,omitempty"`
	Of      string `yaml:"of,omitempty" json:"of,omitempty"`
	GroupBy string `yaml:"group_by,omitempty" json:"group_by,omitempty"`
}

// Rule is one branch of a route step: items matching the conditions go to Port.
type Rule struct {
	Port       string      `yaml:"port" json:"port"`
	Match      string      `yaml:"match,omitempty" json:"match,omitempty"`
	Conditions []Condition `yaml:"conditions,omitempty" json:"conditions,omitempty"`
}

// Condition tests one field. Field is a dot path ("author.login"); "[]" in a
// path means "any element" ("labels[].name"). "." is the item itself. Text
// operators ignore case unless CaseSensitive is set.
type Condition struct {
	Field         string `yaml:"field" json:"field"`
	Op            string `yaml:"op" json:"op"`
	Value         any    `yaml:"value,omitempty" json:"value,omitempty"`
	CaseSensitive bool   `yaml:"case_sensitive,omitempty" json:"case_sensitive,omitempty"`
}

// Value is what a set step writes. Kind selects the other fields:
//
//	constant: value (a typed JSON value)
//	field:    field (copy another field)
//	template: template (text with {field.path} placeholders; {{ and }} are literal braces)
//	function: function, field, and arg for join/split (separator) and default (the fallback)
type Value struct {
	Kind     string `yaml:"kind" json:"kind"`
	Constant any    `yaml:"value,omitempty" json:"value,omitempty"`
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
	Function string `yaml:"function,omitempty" json:"function,omitempty"`
	Field    string `yaml:"field,omitempty" json:"field,omitempty"`
	Arg      any    `yaml:"arg,omitempty" json:"arg,omitempty"`
}

// keeps reports whether a filter keeps what matches (the default).
func (s *Step) keeps() bool { return s.Keep == nil || *s.Keep }

func (s *Step) match() string {
	if s.Match == "" {
		return MatchAll
	}
	return s.Match
}

// Port names of a transform node.
const (
	PortOut   = "out"
	PortError = "error"
)

// Ports returns the output ports the steps can emit on, excluding the
// implicit error port: "out", then (when a route step exists) every route
// rule's port and the default port, in order, without duplicates. Items no
// rule matches go to the default port, which is "out" when unset.
func Ports(steps []Step) []string {
	ports := []string{PortOut}
	for i := range steps {
		if steps[i].Type != StepRoute {
			continue
		}
		for _, r := range steps[i].Rules {
			if r.Port != "" && !slices.Contains(ports, r.Port) {
				ports = append(ports, r.Port)
			}
		}
		if d := steps[i].DefaultPort; d != "" && !slices.Contains(ports, d) {
			ports = append(ports, d)
		}
	}
	return ports
}

// Clone returns a deep copy of steps.
func Clone(steps []Step) []Step {
	if steps == nil {
		return nil
	}
	out := make([]Step, len(steps))
	for i, s := range steps {
		s.Conditions = cloneConds(s.Conditions)
		if s.Rules != nil {
			rules := make([]Rule, len(s.Rules))
			for j, r := range s.Rules {
				r.Conditions = cloneConds(r.Conditions)
				rules[j] = r
			}
			s.Rules = rules
		}
		s.Fields = slices.Clone(s.Fields)
		if s.Rename != nil {
			m := make(map[string]string, len(s.Rename))
			for k, v := range s.Rename {
				m[k] = v
			}
			s.Rename = m
		}
		if s.Keep != nil {
			k := *s.Keep
			s.Keep = &k
		}
		if s.Count != nil {
			c := *s.Count
			s.Count = &c
		}
		if s.Value != nil {
			v := *s.Value
			s.Value = &v
		}
		out[i] = s
	}
	return out
}

func cloneConds(cs []Condition) []Condition { return slices.Clone(cs) }
