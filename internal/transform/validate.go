package transform

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// Severity of an Issue.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Issue codes. They equal the codes of internal/spec's diagnostics.
const (
	CodeRequired        = "required"
	CodeInvalidEnum     = "invalid_enum"
	CodeInvalidValue    = "invalid_value"
	CodeInvalidRegex    = "invalid_regex"
	CodeInvalidTemplate = "invalid_template"
	CodeFieldNotAllowed = "field_not_allowed"
	CodeDuplicateID     = "duplicate_id"
	CodeReservedPort    = "reserved_port"
	CodeInvalidID       = "invalid_id"
	CodeEmptyFlow       = "empty_flow"
)

// Issue is one finding about a step list. Path is relative to the list's
// owner, for example "steps[1].conditions[0].value".
type Issue struct {
	Path     string
	Severity string
	Code     string
	Message  string
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ValidationError is returned by Compile for steps with errors.
type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	for _, i := range e.Issues {
		if i.Severity == SeverityError {
			return i.Path + ": " + i.Message
		}
	}
	return "invalid steps"
}

type collector struct{ issues []Issue }

func (c *collector) add(sev, code, path, format string, args ...any) {
	c.issues = append(c.issues, Issue{Path: path, Severity: sev, Code: code, Message: fmt.Sprintf(format, args...)})
}

func (c *collector) errorf(code, path, format string, args ...any) {
	c.add(SeverityError, code, path, format, args...)
}

var portRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// stepFields lists the optional fields each step type accepts.
var stepFields = map[string][]string{
	StepFilter:     {"match", "conditions", "keep"},
	StepRoute:      {"rules", "default_port"},
	StepPick:       {"fields", "rename", "flatten"},
	StepSet:        {"field", "value"},
	StepRemove:     {"fields"},
	StepSplit:      {"field", "as", "keep_parent"},
	StepSort:       {"field", "by", "order"},
	StepLimit:      {"field", "count"},
	StepUnique:     {"field", "by"},
	StepAggregate:  {"field", "into", "op", "of", "group_by"},
	StepFilterList: {"field", "match", "conditions", "keep"},
}

func (s *Step) setFields() []string {
	var out []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"match", s.Match != ""}, {"conditions", len(s.Conditions) > 0}, {"keep", s.Keep != nil},
		{"rules", len(s.Rules) > 0}, {"default_port", s.DefaultPort != ""},
		{"fields", len(s.Fields) > 0}, {"rename", len(s.Rename) > 0}, {"flatten", s.Flatten},
		{"field", s.Field != ""}, {"value", s.Value != nil},
		{"as", s.As != ""}, {"keep_parent", s.KeepParent},
		{"by", s.By != ""}, {"order", s.Order != ""}, {"count", s.Count != nil},
		{"into", s.Into != ""}, {"op", s.Op != ""}, {"of", s.Of != ""}, {"group_by", s.GroupBy != ""},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// Validate checks steps and returns every problem found. Paths look like
// "steps[1].conditions[0].value". An empty list is a warning, not an error.
func Validate(steps []Step) []Issue {
	c := &collector{}
	if len(steps) == 0 {
		c.add(SeverityWarning, CodeEmptyFlow, "steps", "this node has no steps and passes every item through unchanged")
		return c.issues
	}
	for i := range steps {
		validateStep(c, fmt.Sprintf("steps[%d]", i), &steps[i], i == len(steps)-1)
	}
	return c.issues
}

func validateStep(c *collector, p string, s *Step, last bool) {
	allowed, known := stepFields[s.Type]
	switch {
	case s.Type == "":
		c.errorf(CodeRequired, p+".type", "type is required (one of %s)", strings.Join(StepKinds, " | "))
		return
	case !known:
		c.errorf(CodeInvalidEnum, p+".type", "step type %q is not valid (one of %s)", s.Type, strings.Join(StepKinds, " | "))
		return
	}
	for _, f := range s.setFields() {
		if !slices.Contains(allowed, f) {
			c.errorf(CodeFieldNotAllowed, p+"."+f, "%q is not used by %s steps", f, s.Type)
		}
	}
	switch s.Type {
	case StepFilter:
		validateGroup(c, p, s.Match, s.Conditions)
	case StepRoute:
		validateRoute(c, p, s, last)
	case StepPick:
		validatePick(c, p, s)
	case StepSet:
		validateSet(c, p, s)
	case StepRemove:
		validateFieldList(c, p, s.Fields)
	case StepSplit:
		validateSplit(c, p, s)
	case StepSort:
		arrayField(c, p+".field", s.Field, true)
		relPath(c, p+".by", s.By)
		if s.Order != "" && s.Order != OrderAsc && s.Order != OrderDesc {
			c.errorf(CodeInvalidEnum, p+".order", "order %q is not valid (asc | desc)", s.Order)
		}
	case StepLimit:
		arrayField(c, p+".field", s.Field, true)
		switch {
		case s.Count == nil:
			c.errorf(CodeRequired, p+".count", "count is required")
		case *s.Count < 0:
			c.errorf(CodeInvalidValue, p+".count", "count must not be negative")
		}
	case StepUnique:
		arrayField(c, p+".field", s.Field, true)
		relPath(c, p+".by", s.By)
	case StepAggregate:
		validateAggregate(c, p, s)
	case StepFilterList:
		arrayField(c, p+".field", s.Field, true)
		validateGroup(c, p, s.Match, s.Conditions)
	}
}

// arrayField checks the path of a field that holds an array: no "[]".
func arrayField(c *collector, path, field string, rootOK bool) {
	if field == "" {
		c.errorf(CodeRequired, path, "field is required")
		return
	}
	segs, err := parsePath(field)
	switch {
	case err != nil:
		c.errorf(CodeInvalidValue, path, "%s", err)
	case hasEach(segs):
		c.errorf(CodeInvalidValue, path, "field %q cannot contain []: name the list itself", field)
	case len(segs) == 0 && !rootOK:
		c.errorf(CodeInvalidValue, path, "field must name a field, not the whole item")
	}
}

// relPath checks an optional path relative to a list element.
func relPath(c *collector, path, field string) {
	if field == "" {
		return
	}
	segs, err := parsePath(field)
	switch {
	case err != nil:
		c.errorf(CodeInvalidValue, path, "%s", err)
	case hasEach(segs):
		c.errorf(CodeInvalidValue, path, "path %q cannot contain []", field)
	}
}

// targetPath checks a path that is written to: required, no "[]", not root.
func targetPath(c *collector, path, field string) {
	if field == "" {
		c.errorf(CodeRequired, path, "field is required")
		return
	}
	arrayField(c, path, field, false)
}

func validateFieldList(c *collector, p string, fields []string) {
	if len(fields) == 0 {
		c.errorf(CodeRequired, p+".fields", "at least one field is required")
	}
	seen := map[string]bool{}
	for i, f := range fields {
		fp := fmt.Sprintf("%s.fields[%d]", p, i)
		targetPath(c, fp, f)
		if seen[f] {
			c.errorf(CodeDuplicateID, fp, "field %q is listed twice", f)
		}
		seen[f] = true
	}
}

func validatePick(c *collector, p string, s *Step) {
	validateFieldList(c, p, s.Fields)
	for _, from := range sortedKeys(s.Rename) {
		rp := p + ".rename." + from
		to := s.Rename[from]
		switch {
		case !slices.Contains(s.Fields, from):
			c.errorf(CodeInvalidValue, rp, "rename names %q, which is not one of the picked fields", from)
		case to == "":
			c.errorf(CodeRequired, rp, "the new name must not be empty")
		case strings.ContainsAny(to, "[]") || strings.ContainsFunc(to, func(r rune) bool { return r < 0x20 }):
			c.errorf(CodeInvalidValue, rp, "the new name %q must be a plain field name", to)
		}
	}
	out := map[string]string{}
	for i, f := range s.Fields {
		segs, err := parsePath(f)
		if err != nil || len(segs) == 0 || hasEach(segs) {
			continue
		}
		key := pickTarget(s, f, segs)
		if len(key) == 0 {
			continue
		}
		k := strings.Join(key, "\x00")
		if prev, dup := out[k]; dup {
			c.errorf(CodeDuplicateID, fmt.Sprintf("%s.fields[%d]", p, i), "%q and %q would both produce the field %q", prev, f, strings.Join(key, "."))
			continue
		}
		out[k] = f
	}
}

// pickTarget returns the output path of one picked field.
func pickTarget(s *Step, field string, segs []seg) []string {
	if to, ok := s.Rename[field]; ok && to != "" {
		return []string{to}
	}
	if s.Flatten {
		return []string{strings.Join(keysOf(segs), "_")}
	}
	return keysOf(segs)
}

func validateRoute(c *collector, p string, s *Step, last bool) {
	if !last {
		c.errorf(CodeInvalidValue, p, "a route step must be the last step")
	}
	if len(s.Rules) == 0 {
		c.errorf(CodeRequired, p+".rules", "at least one rule is required")
	}
	seen := map[string]int{}
	for i, r := range s.Rules {
		rp := fmt.Sprintf("%s.rules[%d]", p, i)
		validatePort(c, rp+".port", r.Port)
		if r.Port != "" {
			if first, dup := seen[r.Port]; dup {
				c.errorf(CodeDuplicateID, rp+".port", "port %q is already used by rules[%d]", r.Port, first)
			} else {
				seen[r.Port] = i
			}
		}
		validateGroup(c, rp, r.Match, r.Conditions)
	}
	if s.DefaultPort != "" {
		validatePort(c, p+".default_port", s.DefaultPort)
	}
}

func validatePort(c *collector, path, port string) {
	switch {
	case port == "":
		c.errorf(CodeRequired, path, "port is required")
	case port == PortError:
		c.errorf(CodeReservedPort, path, "port %q is implicit and cannot be used for routing", port)
	case !portRE.MatchString(port):
		c.errorf(CodeInvalidID, path, "port name %q is invalid: use a letter followed by letters, digits, - or _", port)
	}
}

func validateGroup(c *collector, p, match string, conds []Condition) {
	if match != "" && match != MatchAll && match != MatchAny {
		c.errorf(CodeInvalidEnum, p+".match", "match %q is not valid (all | any)", match)
	}
	if len(conds) == 0 {
		c.errorf(CodeRequired, p+".conditions", "at least one condition is required")
	}
	for i := range conds {
		validateCondition(c, fmt.Sprintf("%s.conditions[%d]", p, i), &conds[i])
	}
}

var (
	needsValue = []string{OpEquals, OpNotEquals, OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpMatches, OpGT, OpGTE, OpLT, OpLTE, OpIn}
	textOps    = []string{OpEquals, OpNotEquals, OpContains, OpNotContains, OpStartsWith, OpEndsWith, OpMatches, OpIn}
)

func validateCondition(c *collector, p string, cn *Condition) {
	if cn.Field == "" {
		c.errorf(CodeRequired, p+".field", "field is required (use . for the whole item)")
	} else if _, err := parsePath(cn.Field); err != nil {
		c.errorf(CodeInvalidValue, p+".field", "%s", err)
	}
	if cn.Op == "" {
		c.errorf(CodeRequired, p+".op", "op is required (one of %s)", strings.Join(Operators, " | "))
		return
	}
	if !slices.Contains(Operators, cn.Op) {
		c.errorf(CodeInvalidEnum, p+".op", "operator %q is not valid (one of %s)", cn.Op, strings.Join(Operators, " | "))
		return
	}
	if cn.CaseSensitive && !slices.Contains(textOps, cn.Op) {
		c.errorf(CodeFieldNotAllowed, p+".case_sensitive", "case_sensitive does not apply to %s", cn.Op)
	}
	if !slices.Contains(needsValue, cn.Op) {
		if cn.Value != nil {
			c.errorf(CodeFieldNotAllowed, p+".value", "%s takes no value", cn.Op)
		}
		return
	}
	if cn.Value == nil {
		c.errorf(CodeRequired, p+".value", "%s needs a value", cn.Op)
		return
	}
	kind, err := valueKind(cn.Value)
	if err != nil {
		c.errorf(CodeInvalidValue, p+".value", "%s", err)
		return
	}
	vp := p + ".value"
	switch cn.Op {
	case OpEquals, OpNotEquals, OpContains, OpNotContains:
		if kind != kString && kind != kNumber && kind != kBool {
			c.errorf(CodeInvalidValue, vp, "%s needs text, a number or true/false, not %s", cn.Op, kind)
		}
	case OpStartsWith, OpEndsWith:
		if kind != kString {
			c.errorf(CodeInvalidValue, vp, "%s needs text, not %s", cn.Op, kind)
		}
	case OpMatches:
		if kind != kString {
			c.errorf(CodeInvalidValue, vp, "matches needs a pattern (text), not %s", kind)
		} else if _, err := regexp.Compile(cn.Value.(string)); err != nil {
			c.errorf(CodeInvalidRegex, vp, "invalid pattern %q: %s", cn.Value, strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		}
	case OpGT, OpGTE, OpLT, OpLTE:
		if kind != kNumber && kind != kString {
			c.errorf(CodeInvalidValue, vp, "%s needs a number or text (such as a date), not %s", cn.Op, kind)
		}
	case OpIn:
		if kind != kList {
			c.errorf(CodeInvalidValue, vp, "in needs a list of values, not %s", kind)
			return
		}
		items := toList(cn.Value)
		if len(items) == 0 {
			c.errorf(CodeInvalidValue, vp, "the list must have at least one value")
		}
		for i, it := range items {
			if k, err := valueKind(it); err != nil || (k != kString && k != kNumber && k != kBool) {
				c.errorf(CodeInvalidValue, fmt.Sprintf("%s[%d]", vp, i), "list values must be text, numbers or true/false")
			}
		}
	}
}

func validateSet(c *collector, p string, s *Step) {
	targetPath(c, p+".field", s.Field)
	v := s.Value
	if v == nil {
		c.errorf(CodeRequired, p+".value", "value is required")
		return
	}
	vp := p + ".value"
	kinds := []string{ValueConstant, ValueField, ValueTemplate, ValueFunction}
	if !slices.Contains(kinds, v.Kind) {
		if v.Kind == "" {
			c.errorf(CodeRequired, vp+".kind", "kind is required (one of %s)", strings.Join(kinds, " | "))
		} else {
			c.errorf(CodeInvalidEnum, vp+".kind", "kind %q is not valid (one of %s)", v.Kind, strings.Join(kinds, " | "))
		}
		return
	}
	notAllowed := func(name string, set bool) {
		if set {
			c.errorf(CodeFieldNotAllowed, vp+"."+name, "%q is not used by a %s value", name, v.Kind)
		}
	}
	switch v.Kind {
	case ValueConstant:
		notAllowed("field", v.Field != "")
		notAllowed("template", v.Template != "")
		notAllowed("function", v.Function != "")
		notAllowed("arg", v.Arg != nil)
		if _, err := valueKind(v.Constant); err != nil {
			c.errorf(CodeInvalidValue, vp+".value", "%s", err)
		}
	case ValueField:
		notAllowed("value", v.Constant != nil)
		notAllowed("template", v.Template != "")
		notAllowed("function", v.Function != "")
		notAllowed("arg", v.Arg != nil)
		sourcePath(c, vp+".field", v.Field)
	case ValueTemplate:
		notAllowed("value", v.Constant != nil)
		notAllowed("field", v.Field != "")
		notAllowed("function", v.Function != "")
		notAllowed("arg", v.Arg != nil)
		if v.Template == "" {
			c.errorf(CodeRequired, vp+".template", "template is required")
		} else if _, err := parseTemplate(v.Template); err != nil {
			c.errorf(CodeInvalidTemplate, vp+".template", "%s", err)
		}
	case ValueFunction:
		notAllowed("value", v.Constant != nil)
		notAllowed("template", v.Template != "")
		sourcePath(c, vp+".field", v.Field)
		validateFunction(c, vp, v)
	}
}

func sourcePath(c *collector, path, field string) {
	if field == "" {
		c.errorf(CodeRequired, path, "field is required (use . for the whole item)")
	} else if _, err := parsePath(field); err != nil {
		c.errorf(CodeInvalidValue, path, "%s", err)
	}
}

func validateFunction(c *collector, vp string, v *Value) {
	if !slices.Contains(Functions, v.Function) {
		if v.Function == "" {
			c.errorf(CodeRequired, vp+".function", "function is required (one of %s)", strings.Join(Functions, " | "))
		} else {
			c.errorf(CodeInvalidEnum, vp+".function", "function %q is not valid (one of %s)", v.Function, strings.Join(Functions, " | "))
		}
		return
	}
	switch v.Function {
	case FuncJoin, FuncSplit:
		s, ok := v.Arg.(string)
		switch {
		case v.Arg == nil:
			c.errorf(CodeRequired, vp+".arg", "%s needs a separator", v.Function)
		case !ok:
			c.errorf(CodeInvalidValue, vp+".arg", "the separator must be text")
		case v.Function == FuncSplit && s == "":
			c.errorf(CodeInvalidValue, vp+".arg", "the separator must not be empty")
		}
	case FuncDefault:
		if v.Arg == nil {
			c.errorf(CodeRequired, vp+".arg", "default needs a value to use when the field is missing")
		} else if k, err := valueKind(v.Arg); err != nil {
			c.errorf(CodeInvalidValue, vp+".arg", "%s", err)
		} else if k == kOther {
			c.errorf(CodeInvalidValue, vp+".arg", "the default must be text, a number, true/false or a list")
		}
	default:
		if v.Arg != nil {
			c.errorf(CodeFieldNotAllowed, vp+".arg", "%s takes no argument", v.Function)
		}
	}
}

func validateSplit(c *collector, p string, s *Step) {
	arrayField(c, p+".field", s.Field, true)
	if s.As != "" && (strings.ContainsAny(s.As, ".[]") || strings.ContainsFunc(s.As, func(r rune) bool { return r < 0x20 })) {
		c.errorf(CodeInvalidValue, p+".as", "name %q must be a plain field name", s.As)
	}
	if segs, err := parsePath(s.Field); err == nil && len(segs) == 0 && s.KeepParent {
		c.errorf(CodeInvalidValue, p+".keep_parent", "there is no parent to keep when the item itself is the list")
	}
}

var aggregateOps = map[string]struct{ of, groupBy bool }{
	OpCount: {}, OpSum: {of: true}, OpAvg: {of: true}, OpMin: {of: true}, OpMax: {of: true},
	OpCollect: {of: true}, OpGroupCount: {groupBy: true},
}

func validateAggregate(c *collector, p string, s *Step) {
	arrayField(c, p+".field", s.Field, false)
	targetPath(c, p+".into", s.Into)
	rules, ok := aggregateOps[s.Op]
	switch {
	case s.Op == "":
		c.errorf(CodeRequired, p+".op", "op is required (one of %s)", strings.Join(AggregateOps, " | "))
	case !ok:
		c.errorf(CodeInvalidEnum, p+".op", "op %q is not valid (one of %s)", s.Op, strings.Join(AggregateOps, " | "))
	default:
		relPath(c, p+".of", s.Of)
		relPath(c, p+".group_by", s.GroupBy)
		if s.Of != "" && !rules.of {
			c.errorf(CodeFieldNotAllowed, p+".of", "%s does not use of", s.Op)
		}
		if s.GroupBy != "" && !rules.groupBy {
			c.errorf(CodeFieldNotAllowed, p+".group_by", "%s does not use group_by", s.Op)
		}
		if rules.groupBy && s.GroupBy == "" {
			c.errorf(CodeRequired, p+".group_by", "group_count needs group_by")
		}
	}
}

// ---- value helpers ----

const (
	kNull   = "null"
	kString = "text"
	kNumber = "a number"
	kBool   = "true/false"
	kList   = "a list"
	kOther  = "an object"
)

// valueKind classifies a decoded YAML/JSON value. Numbers must be finite.
func valueKind(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return kNull, nil
	case string:
		return kString, nil
	case bool:
		return kBool, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "", fmt.Errorf("numbers must be finite")
		}
		return kNumber, nil
	case float32:
		return valueKind(float64(x))
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return kNumber, nil
	case []any:
		return kList, nil
	case map[string]any, map[any]any:
		return kOther, nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return kList, nil
	case reflect.Map, reflect.Struct:
		return kOther, nil
	}
	return "", fmt.Errorf("unsupported value type %T", v)
}

// toList converts any decoded list into []any.
func toList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
