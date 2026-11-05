package transform

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Describe returns one plain-language sentence per step ("Keep only items
// where severity is not "noise""). It never fails: fields that are not set
// yet are described as best it can, so the UI can show it while a step is
// being filled in.
func Describe(steps []Step) []string {
	out := make([]string, len(steps))
	for i := range steps {
		out[i] = DescribeStep(&steps[i])
	}
	return out
}

const maxQuoted = 60

// quote renders text for a sentence: in double quotes, on one line, cut when long.
func quote(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == '"':
			r = '\''
		case r < 0x20 || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029:
			r = ' '
		}
		if n == maxQuoted {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return `"` + b.String() + `"`
}

// valueText renders a typed value for a sentence.
func valueText(v any) string {
	switch x := v.(type) {
	case nil:
		return "nothing"
	case string:
		return quote(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(x)
	}
	if l := toList(v); l != nil {
		parts := make([]string, len(l))
		for i, it := range l {
			parts[i] = valueText(it)
		}
		return strings.Join(parts, ", ")
	}
	b, err := json.Marshal(jsonable(v))
	if err != nil {
		return "a value"
	}
	return quote(string(b))
}

// pathText renders a field path for a sentence.
func pathText(p string) string {
	segs, err := parsePath(p)
	if err != nil {
		return quote(p)
	}
	if len(segs) == 0 {
		return "the item"
	}
	return formatPath(segs)
}

// listText renders a field path that names a list.
func listText(p string) string {
	if segs, err := parsePath(p); err == nil && len(segs) == 0 {
		return "the list"
	}
	return pathText(p)
}

type opWords struct{ pos, neg string }

var opPhrases = map[string]opWords{
	OpEquals:     {"is %s", "is not %s"},
	OpContains:   {"contains %s", "does not contain %s"},
	OpStartsWith: {"starts with %s", ""},
	OpEndsWith:   {"ends with %s", ""},
	OpMatches:    {"matches the pattern %s", ""},
	OpGT:         {"is greater than %s", ""},
	OpGTE:        {"is at least %s", ""},
	OpLT:         {"is less than %s", ""},
	OpLTE:        {"is at most %s", ""},
	OpExists:     {"is present", "is missing"},
	OpIsEmpty:    {"is empty", "is not empty"},
	OpIsTrue:     {"is true", ""},
	OpIsFalse:    {"is false", ""},
	OpIn:         {"is one of %s", ""},
}

// DescribeCondition renders one condition ("severity is not "noise"").
func DescribeCondition(cn Condition) string {
	segs, _ := parsePath(cn.Field)
	f := pathText(cn.Field)
	op, neg := cn.Op, false
	if base, ok := negated[op]; ok {
		op, neg = base, true
	}
	words, ok := opPhrases[op]
	if !ok {
		return f + " " + cn.Op
	}
	phrase := words.pos
	if neg && !hasEach(segs) {
		phrase = words.neg
	}
	if strings.Contains(phrase, "%s") {
		phrase = fmt.Sprintf(phrase, valueText(cn.Value))
	}
	text := f + " " + phrase
	if hasEach(segs) {
		if neg {
			text = "no " + text
		} else {
			text = "any " + text
		}
	}
	if cn.CaseSensitive && cn.Op != "" {
		for _, t := range textOps {
			if t == cn.Op {
				return text + " (same capitalization)"
			}
		}
	}
	return text
}

// describeGroup renders conditions joined by "and" or "or".
func describeGroup(match string, conds []Condition) string {
	if len(conds) == 0 {
		return "(no conditions yet)"
	}
	parts := make([]string, len(conds))
	for i, cn := range conds {
		parts[i] = DescribeCondition(cn)
	}
	word := " and "
	if match == MatchAny {
		word = " or "
	}
	return strings.Join(parts, word)
}

// DescribeStep renders one step.
func DescribeStep(s *Step) string {
	switch s.Type {
	case StepFilter:
		if s.keeps() {
			return "Keep only items where " + describeGroup(s.Match, s.Conditions)
		}
		return "Drop items where " + describeGroup(s.Match, s.Conditions)
	case StepRoute:
		var parts []string
		for _, r := range s.Rules {
			parts = append(parts, fmt.Sprintf("%s when %s", quote(r.Port), describeGroup(r.Match, r.Conditions)))
		}
		def := s.DefaultPort
		if def == "" {
			def = PortOut
		}
		parts = append(parts, "everything else to "+quote(def))
		return "Send items to different outputs: " + strings.Join(parts, "; ")
	case StepPick:
		text := "Keep only the fields " + joinPaths(s.Fields)
		var renames []string
		for _, f := range s.Fields {
			if to, ok := s.Rename[f]; ok {
				renames = append(renames, fmt.Sprintf("%s becomes %s", pathText(f), quote(to)))
			}
		}
		if len(renames) > 0 {
			text += " (" + strings.Join(renames, ", ") + ")"
		}
		if s.Flatten {
			text += ", with nested fields flattened"
		}
		return text
	case StepSet:
		return "Set " + pathText(s.Field) + " to " + describeValue(s.Value)
	case StepRemove:
		return "Remove the fields " + joinPaths(s.Fields)
	case StepSplit:
		text := "Turn each item of " + listText(s.Field) + " into a separate item"
		switch {
		case s.KeepParent && s.As != "":
			text += ", keeping the other fields, with each item as " + quote(s.As)
		case s.KeepParent:
			text += ", keeping the other fields"
		case s.As != "":
			text += ", each as " + quote(s.As)
		}
		return text
	case StepSort:
		dir := "ascending"
		if s.Order == OrderDesc {
			dir = "descending"
		}
		by := ""
		if segs, _ := parsePath(s.By); len(segs) > 0 {
			by = " by " + pathText(s.By)
		}
		return fmt.Sprintf("Sort %s%s (%s)", listText(s.Field), by, dir)
	case StepLimit:
		n := "N"
		if s.Count != nil {
			n = strconv.Itoa(*s.Count)
		}
		return fmt.Sprintf("Keep the first %s of %s", n, listText(s.Field))
	case StepUnique:
		by := ""
		if segs, _ := parsePath(s.By); len(segs) > 0 {
			by = " (same " + pathText(s.By) + ")"
		}
		return "Remove duplicates from " + listText(s.Field) + by
	case StepAggregate:
		return describeAggregate(s)
	case StepFilterList:
		verb := "Keep only the items of "
		if !s.keeps() {
			verb = "Remove the items of "
		}
		return verb + listText(s.Field) + " where " + describeGroup(s.Match, s.Conditions)
	}
	if s.Type == "" {
		return "Choose a step type"
	}
	return "Unknown step " + quote(s.Type)
}

func joinPaths(fields []string) string {
	if len(fields) == 0 {
		return "(none yet)"
	}
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = pathText(f)
	}
	return strings.Join(parts, ", ")
}

func describeAggregate(s *Step) string {
	into, list := pathText(s.Into), listText(s.Field)
	of := "the items"
	if segs, _ := parsePath(s.Of); len(segs) > 0 {
		of = pathText(s.Of)
	}
	switch s.Op {
	case OpCount:
		return fmt.Sprintf("Set %s to the number of items in %s", into, list)
	case OpSum:
		return fmt.Sprintf("Set %s to the sum of %s in %s", into, of, list)
	case OpAvg:
		return fmt.Sprintf("Set %s to the average of %s in %s", into, of, list)
	case OpMin:
		return fmt.Sprintf("Set %s to the smallest of %s in %s", into, of, list)
	case OpMax:
		return fmt.Sprintf("Set %s to the largest of %s in %s", into, of, list)
	case OpCollect:
		return fmt.Sprintf("Set %s to the list of %s in %s", into, of, list)
	case OpGroupCount:
		return fmt.Sprintf("Set %s to how many items in %s have each %s", into, list, pathText(s.GroupBy))
	}
	return fmt.Sprintf("Summarize %s into %s", list, into)
}

func describeValue(v *Value) string {
	if v == nil {
		return "a value"
	}
	f := pathText(v.Field)
	switch v.Kind {
	case ValueConstant:
		return valueText(v.Constant)
	case ValueField:
		return "the value of " + f
	case ValueTemplate:
		return "the text " + quote(v.Template)
	case ValueFunction:
		switch v.Function {
		case FuncLowercase:
			return f + " in lowercase"
		case FuncUppercase:
			return f + " in uppercase"
		case FuncTrim:
			return f + " without spaces at the start and end"
		case FuncLength:
			return "the length of " + f
		case FuncJoin:
			return "the items of " + f + " joined with " + valueText(v.Arg)
		case FuncFirst:
			return "the first item of " + f
		case FuncLast:
			return "the last item of " + f
		case FuncSplit:
			return f + " split at " + valueText(v.Arg)
		case FuncToNumber:
			return f + " as a number"
		case FuncToString:
			return f + " as text"
		case FuncDefault:
			return f + ", or " + valueText(v.Arg) + " when it is missing"
		}
		return "a function of " + f
	}
	return "a value"
}
