package transform

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Helper definitions emitted at the top of a program, only when used. Order
// is fixed so output is deterministic; a helper may use the ones above it.
var helperDefs = []struct{ name, def string }{
	{"hal_get", `def hal_get($p): try getpath($p) catch null;`},
	{"hal_each", `def hal_each($p): hal_get($p) | if type == "array" then .[] else empty end;`},
	{"hal_text", `def hal_text: if type == "string" then . elif . == null then "" elif type == "array" then map(hal_text) | join(", ") else tojson end;`},
	{"hal_pick", `def hal_pick($specs): . as $in | reduce $specs[] as $s ({}; ($in | hal_get($s.from)) as $v | if $v == null then . else setpath($s.to; $v) end);`},
	{"hal_del", `def hal_del($ps): reduce $ps[] as $p (.; . as $d | try delpaths([$p]) catch $d);`},
	{"hal_unique", `def hal_unique(f): reduce .[] as $x ({s: {}, o: []}; ($x | f | tojson) as $k | if .s[$k] then . else (.s[$k] = true | .o += [$x]) end) | .o;`},
	{"hal_group_count", `def hal_group_count(f): reduce .[] as $x ([]; ($x | f) as $k | if any(.[]; .value == $k) then map(if .value == $k then .count += 1 else . end) else . + [{value: $k, count: 1}] end);`},
}

// helperNeeds lists the helpers each helper calls.
var helperNeeds = map[string][]string{"hal_each": {"hal_get"}, "hal_pick": {"hal_get"}}

type compiler struct{ used map[string]bool }

func (c *compiler) use(names ...string) {
	for _, n := range names {
		c.used[n] = true
		for _, dep := range helperNeeds[n] {
			c.used[dep] = true
		}
	}
}

// Compile returns the jq program for steps: helper definitions (when used),
// then one "# step N: description" comment and one line of jq per step,
// chained with pipes. The program reads one JSON value on stdin and prints
// zero or more values: no output means the item was dropped, several mean it
// was split, and "_port" / "_reason" keys carry routing (REQUIREMENTS §4).
// Steps with errors return a *ValidationError.
func Compile(steps []Step) (string, error) {
	if issues := Validate(steps); HasErrors(issues) {
		return "", &ValidationError{Issues: issues}
	}
	if len(steps) == 0 {
		return ".", nil
	}
	c := &compiler{used: map[string]bool{}}
	desc := Describe(steps)
	var body strings.Builder
	for i := range steps {
		expr, err := c.step(&steps[i])
		if err != nil {
			return "", fmt.Errorf("steps[%d]: %w", i, err)
		}
		fmt.Fprintf(&body, "# step %d: %s\n", i+1, commentText(desc[i]))
		if i > 0 {
			body.WriteString("| ")
		}
		body.WriteString(expr)
		body.WriteByte('\n')
	}
	var out strings.Builder
	for _, h := range helperDefs {
		if c.used[h.name] {
			out.WriteString(h.def)
			out.WriteByte('\n')
		}
	}
	out.WriteString(body.String())
	return out.String(), nil
}

// commentText makes s safe to put after "#": one line, and no backslash (jq
// 1.7.1 continues a comment onto the next line after a trailing backslash).
func commentText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteByte('/')
		case r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lit encodes v as a jq literal. JSON is valid jq, and encoding escapes every
// quote, backslash and control character, so user text cannot end the literal
// or open an interpolation.
func lit(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(jsonable(v)); err != nil {
		return "null" // unreachable after Validate: valueKind rejects what cannot be encoded
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// jsonable converts YAML-decoded maps (map[any]any) to ones encoding/json accepts.
func jsonable(v any) any {
	switch x := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[fmt.Sprint(k)] = jsonable(val)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[k] = jsonable(val)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, val := range x {
			l[i] = jsonable(val)
		}
		return l
	}
	return v
}

func (c *compiler) path(p string) []seg {
	segs, _ := parsePath(p) // validated
	return segs
}

// get is jq for the value at a path without "[]": null when missing.
func (c *compiler) get(segs []seg) string {
	if len(segs) == 0 {
		return "."
	}
	c.use("hal_get")
	return "hal_get(" + lit(keysOf(segs)) + ")"
}

// getAll is jq for the value at any path: the single value, or for a path
// with "[]" an array of every non-null match.
func (c *compiler) getAll(segs []seg) string {
	if !hasEach(segs) {
		return c.get(segs)
	}
	var parts []string
	var cur []seg
	for _, s := range segs {
		if s.each {
			parts = append(parts, c.each(cur))
			cur = nil
			continue
		}
		cur = append(cur, s)
	}
	if len(cur) > 0 {
		parts = append(parts, c.get(cur))
	}
	return "[" + strings.Join(parts, " | ") + " | select(. != null)]"
}

// each is jq for the elements of the array at a path without "[]".
func (c *compiler) each(segs []seg) string {
	c.use("hal_each")
	return "hal_each(" + lit(keysOf(segs)) + ")"
}

// onPath applies a predicate to the value at a path. With "[]" in the path it
// holds when any element satisfies it.
func (c *compiler) onPath(segs []seg, leaf string) string {
	for i, s := range segs {
		if s.each {
			return "any(" + c.each(segs[:i]) + "; " + c.onPath(segs[i+1:], leaf) + ")"
		}
	}
	if len(segs) == 0 {
		return leaf
	}
	return "(" + c.get(segs) + " | " + leaf + ")"
}

var negated = map[string]string{
	OpNotEquals: OpEquals, OpNotContains: OpContains, OpNotExists: OpExists, OpIsNotEmpty: OpIsEmpty,
}

func (c *compiler) condition(cn *Condition) string {
	segs := c.path(cn.Field)
	op, neg := cn.Op, false
	if base, ok := negated[op]; ok {
		op, neg = base, true
	}
	e := c.onPath(segs, c.leaf(op, cn))
	if neg {
		return "(" + e + " | not)"
	}
	return e
}

// group joins conditions with and/or.
func (c *compiler) group(match string, conds []Condition) string {
	parts := make([]string, len(conds))
	for i := range conds {
		parts[i] = c.condition(&conds[i])
	}
	if len(parts) == 1 {
		return parts[0]
	}
	word := " and "
	if match == MatchAny {
		word = " or "
	}
	return "(" + strings.Join(parts, word) + ")"
}

func quoteMeta(s string) string { return regexp.QuoteMeta(s) }

// eq is a predicate (on ".") for equality with v. Text compares by regex with
// the i flag unless case matters, so non-ASCII letters fold too.
func eq(v any, ci bool) string {
	if s, ok := v.(string); ok && ci {
		return `(type == "string" and test(` + lit(`\A`+quoteMeta(s)+`\z`) + `; "i"))`
	}
	return "(. == " + lit(v) + ")"
}

func (c *compiler) leaf(op string, cn *Condition) string {
	ci := !cn.CaseSensitive
	v := cn.Value
	switch op {
	case OpEquals:
		return eq(v, ci)
	case OpContains:
		s, isStr := v.(string)
		if !isStr {
			s = textOf(v)
		}
		if isStr && ci {
			return `(if type == "string" then test(` + lit(quoteMeta(s)) + `; "i") elif type == "array" then any(.[]; ` + eq(v, true) + `) else false end)`
		}
		return `(if type == "string" then contains(` + lit(s) + `) elif type == "array" then any(.[]; . == ` + lit(v) + `) else false end)`
	case OpStartsWith:
		s := v.(string)
		if ci {
			return `(type == "string" and test(` + lit(`\A`+quoteMeta(s)) + `; "i"))`
		}
		return `(type == "string" and startswith(` + lit(s) + `))`
	case OpEndsWith:
		s := v.(string)
		if ci {
			return `(type == "string" and test(` + lit(quoteMeta(s)+`\z`) + `; "i"))`
		}
		return `(type == "string" and endswith(` + lit(s) + `))`
	case OpMatches:
		if ci {
			return `(type == "string" and test(` + lit(v) + `; "i"))`
		}
		return `(type == "string" and test(` + lit(v) + `))`
	case OpGT, OpGTE, OpLT, OpLTE:
		typ := "number"
		if _, ok := v.(string); ok {
			typ = "string"
		}
		sym := map[string]string{OpGT: ">", OpGTE: ">=", OpLT: "<", OpLTE: "<="}[op]
		return `(type == "` + typ + `" and . ` + sym + ` ` + lit(v) + `)`
	case OpExists:
		return `(. != null)`
	case OpIsEmpty:
		return `(. == null or . == [] or . == {} or (type == "string" and test("\\A\\s*\\z")))`
	case OpIsTrue:
		return `(. == true)`
	case OpIsFalse:
		return `(. == false)`
	case OpIn:
		items := toList(v)
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = eq(it, ci)
		}
		return "(" + strings.Join(parts, " or ") + ")"
	}
	return "false"
}

// textOf is the text form of a number or boolean.
func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// ---- steps ----

func (c *compiler) step(s *Step) (string, error) {
	switch s.Type {
	case StepFilter:
		g := c.group(s.match(), s.Conditions)
		if !s.keeps() {
			g += " | not"
		}
		return "select(" + g + ")", nil
	case StepRoute:
		return c.route(s), nil
	case StepPick:
		return c.pick(s), nil
	case StepSet:
		return c.set(s)
	case StepRemove:
		c.use("hal_del")
		paths := make([][]string, len(s.Fields))
		for i, f := range s.Fields {
			paths[i] = keysOf(c.path(f))
		}
		return "hal_del(" + lit(paths) + ")", nil
	case StepSplit:
		return c.split(s), nil
	case StepSort, StepLimit, StepUnique, StepFilterList:
		return c.listStep(s), nil
	case StepAggregate:
		return c.aggregate(s), nil
	}
	return "", fmt.Errorf("unknown step type %q", s.Type)
}

func (c *compiler) route(s *Step) string {
	var b strings.Builder
	b.WriteString(`(if type != "object" then error("a route step needs the item to be an object")`)
	for i := range s.Rules {
		r := &s.Rules[i]
		reason := "sent to " + r.Port + ": " + describeGroup(r.Match, r.Conditions)
		fmt.Fprintf(&b, " elif %s then . + {_port: %s, _reason: %s}", c.group(matchOf(r.Match), r.Conditions), lit(r.Port), lit(reason))
	}
	def := s.DefaultPort
	if def == "" {
		def = PortOut
	}
	fmt.Fprintf(&b, " else . + {_port: %s, _reason: %s} end)", lit(def), lit("no rule matched, so sent to "+def))
	return b.String()
}

func matchOf(m string) string {
	if m == "" {
		return MatchAll
	}
	return m
}

type pickSpec struct {
	From []string `json:"from"`
	To   []string `json:"to"`
}

func (c *compiler) pick(s *Step) string {
	c.use("hal_pick")
	specs := make([]pickSpec, len(s.Fields))
	for i, f := range s.Fields {
		segs := c.path(f)
		specs[i] = pickSpec{From: keysOf(segs), To: pickTarget(s, f, segs)}
	}
	return "hal_pick(" + lit(specs) + ")"
}

func (c *compiler) split(s *Step) string {
	segs := c.path(s.Field)
	f := lit(keysOf(segs))
	each := c.each(segs)
	switch {
	case s.KeepParent && s.As != "":
		return "(. as $p | " + each + " as $e | $p | delpaths([" + f + "]) | setpath([" + lit(s.As) + "]; $e))"
	case s.KeepParent:
		return "(. as $p | " + each + " as $e | $p | setpath(" + f + "; $e))"
	case s.As != "":
		return "(" + each + " | {" + lit(s.As) + ": .})"
	}
	return each
}

func (c *compiler) listStep(s *Step) string {
	segs := c.path(s.Field)
	f := lit(keysOf(segs))
	var op string
	switch s.Type {
	case StepSort:
		op = "sort"
		if by := c.path(s.By); len(by) > 0 {
			op = "sort_by(" + c.get(by) + ")"
		}
		if s.Order == OrderDesc {
			op += " | reverse"
		}
	case StepLimit:
		op = ".[:" + strconv.Itoa(*s.Count) + "]"
	case StepUnique:
		c.use("hal_unique")
		op = "hal_unique(" + c.get(c.path(s.By)) + ")"
	case StepFilterList:
		g := c.group(s.match(), s.Conditions)
		if !s.keeps() {
			g += " | not"
		}
		op = "map(select(" + g + "))"
	}
	return "(" + c.get(segs) + ` as $l | if ($l | type) == "array" then setpath(` + f + "; $l | " + op + ") else . end)"
}

func (c *compiler) aggregate(s *Step) string {
	of := c.get(c.path(s.Of))
	var op string
	switch s.Op {
	case OpCount:
		op = "length"
	case OpSum:
		op = `[.[] | ` + of + ` | select(type == "number")] | add // 0`
	case OpAvg:
		op = `[.[] | ` + of + ` | select(type == "number")] | if length == 0 then null else add / length end`
	case OpMin:
		op = `[.[] | ` + of + ` | select(. != null)] | min`
	case OpMax:
		op = `[.[] | ` + of + ` | select(. != null)] | max`
	case OpCollect:
		op = `[.[] | ` + of + ` | select(. != null)]`
	case OpGroupCount:
		c.use("hal_group_count")
		op = "hal_group_count(" + c.get(c.path(s.GroupBy)) + ")"
	}
	list := c.get(c.path(s.Field))
	return `((` + list + ` | if type == "array" then . else [] end) as $l | setpath(` + lit(keysOf(c.path(s.Into))) + `; $l | ` + op + `))`
}

func (c *compiler) set(s *Step) (string, error) {
	val, err := c.value(s.Value)
	if err != nil {
		return "", err
	}
	return "setpath(" + lit(keysOf(c.path(s.Field))) + "; " + val + ")", nil
}

func (c *compiler) value(v *Value) (string, error) {
	switch v.Kind {
	case ValueConstant:
		return lit(v.Constant), nil
	case ValueField:
		return c.getAll(c.path(v.Field)), nil
	case ValueTemplate:
		parts, err := parseTemplate(v.Template)
		if err != nil {
			return "", err
		}
		c.use("hal_text")
		exprs := make([]string, 0, len(parts))
		for _, p := range parts {
			if p.field == nil {
				exprs = append(exprs, lit(p.text))
			} else {
				exprs = append(exprs, "("+c.getAll(p.field)+" | hal_text)")
			}
		}
		return "(" + strings.Join(exprs, " + ") + ")", nil
	case ValueFunction:
		return c.function(v), nil
	}
	return "", fmt.Errorf("unknown value kind %q", v.Kind)
}

func (c *compiler) function(v *Value) string {
	g := c.getAll(c.path(v.Field))
	switch v.Function {
	case FuncLowercase:
		return "(" + g + ` | if type == "string" then ascii_downcase else . end)`
	case FuncUppercase:
		return "(" + g + ` | if type == "string" then ascii_upcase else . end)`
	case FuncTrim:
		return "(" + g + ` | if type == "string" then gsub("\\A\\s+|\\s+\\z"; "") else . end)`
	case FuncLength:
		return "(" + g + ` | if . == null then 0 elif type == "array" or type == "string" or type == "object" then length else null end)`
	case FuncJoin:
		c.use("hal_text")
		return "(" + g + ` | if type == "array" then map(hal_text) | join(` + lit(v.Arg) + `) else hal_text end)`
	case FuncFirst:
		return "(" + g + ` | if type == "array" then .[0] else null end)`
	case FuncLast:
		return "(" + g + ` | if type == "array" then .[-1] else null end)`
	case FuncSplit:
		return "(" + g + ` | if type == "string" then split(` + lit(v.Arg) + `) else null end)`
	case FuncToNumber:
		return "(" + g + ` | if type == "number" then . elif type == "string" then (try tonumber catch null) else null end)`
	case FuncToString:
		return "(" + g + ` | if type == "string" then . elif . == null then "" else tojson end)`
	case FuncDefault:
		return "(" + g + ` | if . == null then ` + lit(v.Arg) + ` else . end)`
	}
	return "null"
}

// ---- templates ----

type tplPart struct {
	text  string // literal text, when field is nil
	field []seg  // placeholder path; non-nil (possibly empty slice) marks a placeholder
}

// parseTemplate splits "Hello {user.name}!" into literal and placeholder
// parts. "{{" and "}}" are literal braces.
func parseTemplate(t string) ([]tplPart, error) {
	var parts []tplPart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, tplPart{text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(t); i++ {
		switch ch := t[i]; {
		case ch == '{' && strings.HasPrefix(t[i:], "{{"):
			lit.WriteByte('{')
			i++
		case ch == '}' && strings.HasPrefix(t[i:], "}}"):
			lit.WriteByte('}')
			i++
		case ch == '{':
			end := strings.IndexByte(t[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("a { is never closed (write {{ for a literal brace)")
			}
			name := strings.TrimSpace(t[i+1 : i+end])
			if name == "" {
				return nil, fmt.Errorf("an empty placeholder {} (write {{ for a literal brace)")
			}
			segs, err := parsePath(name)
			if err != nil {
				return nil, fmt.Errorf("placeholder {%s}: %w", name, err)
			}
			flush()
			if segs == nil {
				segs = []seg{}
			}
			parts = append(parts, tplPart{field: segs})
			i += end
		case ch == '}':
			return nil, fmt.Errorf("a } has no matching { (write }} for a literal brace)")
		default:
			lit.WriteByte(ch)
		}
	}
	flush()
	return parts, nil
}
