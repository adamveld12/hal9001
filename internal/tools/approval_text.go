package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits of the text hal generates for an approval.
const (
	// MaxApprovalDescription is the longest description, in runes (store.MaxApprovalDescription).
	MaxApprovalDescription = 600
	// MaxApprovalCommand is the longest command shown with an approval, in runes.
	MaxApprovalCommand = 4000
	// maxInlineCommand is how much of a command goes into a one-line description.
	maxInlineCommand = 80
	maxFactValue     = 200
	maxFactFields    = 8
)

// ApprovalField is one row of the facts table of an approval.
type ApprovalField struct{ Key, Value string }

// ApprovalText is what hal says about a call that needs approval, generated from the call's facts (never from the
// agent's own words).
type ApprovalText struct {
	// Title is one line; Description at most MaxApprovalDescription runes; Command the shell command or other
	// explanation of what runs (at most MaxApprovalCommand runes), empty when the tool runs no command.
	Title, Description, Command string
	Fields                      []ApprovalField
}

// DescribeCall returns the approval text of a call of t: t.Describe's, with every part bounded, or the generic text
// when the tool has no Describe (or leaves parts empty).
func DescribeCall(t Tool, args json.RawMessage, why string) ApprovalText {
	var at ApprovalText
	if t.Describe != nil {
		at = t.Describe(args, why)
	}
	g := genericDescribe(t.Name, args, why)
	if strings.TrimSpace(at.Title) == "" {
		at.Title = g.Title
	}
	if strings.TrimSpace(at.Description) == "" {
		at.Description = g.Description
	}
	if strings.TrimSpace(at.Command) == "" {
		at.Command = g.Command
	}
	if len(at.Fields) == 0 {
		at.Fields = g.Fields
	}
	at.Title = ClipRunes(oneLine(at.Title), 200)
	at.Description = ClipRunes(strings.TrimSpace(at.Description), MaxApprovalDescription)
	at.Command = ClipRunes(strings.TrimSpace(at.Command), MaxApprovalCommand)
	for i := range at.Fields {
		at.Fields[i].Value = ClipRunes(at.Fields[i].Value, maxFactValue)
	}
	return at
}

// genericDescribe is the text of a tool without a Describe: "Run tool <name>", the reason's first paragraph and the
// call's top-level scalar arguments as facts.
func genericDescribe(name string, args json.RawMessage, why string) ApprovalText {
	at := ApprovalText{Title: "Run tool " + name, Command: ExtractCommand(args), Fields: factFields(args)}
	desc := strings.TrimSpace(strings.SplitN(strings.TrimSpace(why), "\n\n", 2)[0])
	if desc == "" {
		desc = "Run tool " + name
		if len(at.Fields) > 0 {
			desc += " with " + factSummary(at.Fields, 3)
		}
	}
	at.Description = desc
	return at
}

// factFields lists the top-level string, number and boolean arguments as key/value rows, sorted by key.
func factFields(args json.RawMessage) []ApprovalField {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []ApprovalField
	for _, k := range keys {
		var v string
		switch x := m[k].(type) {
		case string:
			v = x
		case json.Number:
			v = x.String()
		case bool:
			v = fmt.Sprint(x)
		default:
			continue
		}
		if v == "" || k == "command" || k == "cmd" || k == "script" || k == "content" || k == "stdin" {
			continue
		}
		out = append(out, ApprovalField{Key: k, Value: ClipRunes(oneLine(v), maxFactValue)})
		if len(out) == maxFactFields {
			break
		}
	}
	return out
}

func factSummary(fs []ApprovalField, n int) string {
	var parts []string
	for i, f := range fs {
		if i == n {
			break
		}
		parts = append(parts, f.Key+"="+ClipRunes(f.Value, 40))
	}
	return strings.Join(parts, ", ")
}

// commandKeys are the argument names that hold what a tool runs, in order of preference.
var commandKeys = []string{"command", "cmd", "script", "argv"}

// ExtractCommand finds the command in a tool call's arguments: the first value of command, cmd, script or argv,
// looking at the top level first and then in nested objects and arrays (keys in sorted order). A string is returned
// as is, an array of strings (argv) joined by spaces. It returns "" when there is none.
func ExtractCommand(args json.RawMessage) string {
	var v any
	if json.Unmarshal(args, &v) != nil {
		return ""
	}
	return findCommand(v, 0)
}

func findCommand(v any, depth int) string {
	if depth > 6 {
		return ""
	}
	switch x := v.(type) {
	case map[string]any:
		for _, k := range commandKeys {
			if c := commandText(x[k]); c != "" {
				return c
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if c := findCommand(x[k], depth+1); c != "" {
				return c
			}
		}
	case []any:
		for _, e := range x {
			if c := findCommand(e, depth+1); c != "" {
				return c
			}
		}
	}
	return ""
}

func commandText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return ""
			}
			parts = append(parts, s)
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

// ClipRunes cuts s to at most n runes, ending with an ellipsis when it was cut.
func ClipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:n-1]), " \t\r\n") + "…"
}

// inlineCode quotes a short command for a one-line description: in backticks, or in plain quotes when the command
// itself holds a backtick. Only the first line is shown.
func inlineCode(cmd string) string {
	line, more, _ := strings.Cut(strings.TrimSpace(cmd), "\n")
	line = ClipRunes(strings.TrimSpace(line), maxInlineCommand)
	if more != "" && !strings.HasSuffix(line, "…") {
		line += " …"
	}
	if strings.Contains(line, "`") {
		return `"` + line + `"`
	}
	return "`" + line + "`"
}

// DeniedText is what the agent reads when the user denied a call that carried steering (comment): the steering text
// and an instruction to follow it. A held approval (the conversation reached max_steers_per_run) offers no steering:
// the agent is told to stop and wait. An empty comment on a normal denial returns "".
func DeniedText(comment string, held bool) string {
	if held {
		return "Denied by the user. The steering limit for this conversation was reached; stop and wait for the user instead of trying other actions."
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return ""
	}
	return `Denied by the user: "` + comment + `". Do not retry the same action unchanged; do what the user asked instead.`
}
