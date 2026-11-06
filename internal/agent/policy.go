package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Action is the policy outcome for one permission request.
type Action string

const (
	ActionAllow  Action = "allow"
	ActionReject Action = "reject"
	// ActionAsk sends the request to the approval queue (gated policy).
	ActionAsk Action = "ask"
)

// ToolRequest is the part of a session/request_permission the policy sees.
type ToolRequest struct {
	Kind  string // ACP tool kind: read, edit, execute, other, ...
	Title string
	// Name is the tool name when the transport provides one separately.
	Name string
	// RawInput is the decoded rawInput of the tool call (usually a map).
	RawInput any
	// Paths are the target paths of the call (see ToolPaths).
	Paths []string
	// ToolCallID identifies the tool call within the session.
	ToolCallID string
}

// Decision is a policy outcome with the evidence recorded on the
// permission request.
type Decision struct {
	Action Action
	// Rule names what matched, e.g. "kind:read", "allow.commands[0]: ^gh pr view".
	Rule   string
	Reason string
}

// Engine applies a Policy and a node's AllowRules (§7.2).
type Engine struct {
	policy   Policy
	tools    []*regexp.Regexp
	commands []*regexp.Regexp
	veto     func(ToolRequest, int) string
	paths    pathRules
}

// autoKinds are allowed without asking under readonly and gated.
var autoKinds = map[string]bool{"read": true, "search": true, "fetch": true, "think": true}

// editKinds are the tool kinds allow.edit_under applies to.
var editKinds = map[string]bool{"edit": true, "delete": true, "move": true}

// NewEngine compiles the allow rules.
func NewEngine(p Policy, allow AllowRules) (*Engine, error) {
	e := &Engine{policy: p, veto: allow.CommandVeto}
	for _, s := range allow.Tools {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("allow.tools %q: %w", s, err)
		}
		e.tools = append(e.tools, re)
	}
	for _, s := range allow.Commands {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("allow.commands %q: %w", s, err)
		}
		e.commands = append(e.commands, re)
	}
	switch p {
	case PolicyReadonly, PolicyGated, PolicyAuto:
	default:
		return nil, fmt.Errorf("unknown policy %q", p)
	}
	e.paths = newPathRules(allow)
	return e, nil
}

// Decide applies the policy to one request.
func (e *Engine) Decide(r ToolRequest) Decision {
	if e.policy == PolicyAuto {
		return Decision{Action: ActionAllow, Rule: "policy:auto", Reason: "auto policy allows every request"}
	}
	// A read-class call on a protected path is not allowed by its kind; it
	// falls through to the allow rules and then to ask/reject.
	var protected string
	if autoKinds[r.Kind] {
		p, hit := e.paths.protected(r.Paths)
		if !hit {
			return Decision{Action: ActionAllow, Rule: "kind:" + r.Kind, Reason: "tool kind " + r.Kind + " is read-class"}
		}
		protected = "protected:" + p
	}

	if editKinds[r.Kind] {
		if d, hit := e.paths.denied(r.Paths); hit {
			return Decision{Action: ActionReject, Rule: "edit_deny:" + d, Reason: "the " + r.Kind + " call names " + d + ", which is read-only"}
		}
	}

	var editNote string
	if editKinds[r.Kind] && len(e.paths.editUnder) > 0 {
		if i, dir, ok := e.paths.editAllowed(r.Paths); ok {
			return Decision{Action: ActionAllow, Rule: fmt.Sprintf("allow.edit_under[%d]: %s", i, dir), Reason: fmt.Sprintf("every target path of the %s call is inside %s", r.Kind, dir)}
		}
		if len(r.Paths) == 0 {
			editNote = "the " + r.Kind + " call names no target path, so allow.edit_under cannot apply"
		} else {
			editNote = "a target path of the " + r.Kind + " call is outside allow.edit_under"
		}
	}

	// allow.commands applies to execute-kind tools only.
	var cmdNote string
	if r.Kind == "execute" && len(e.commands) > 0 {
		cmd, ok := ExtractCommand(r.RawInput)
		switch {
		case !ok:
			cmdNote = "could not extract a command from the tool input, so allow.commands cannot apply"
		case hasShellControl(cmd):
			cmdNote = fmt.Sprintf("command %q chains or redirects with shell operators, so allow.commands does not apply", cmd)
		default:
			for i, re := range e.commands {
				if re.MatchString(cmd) {
					if e.veto != nil {
						if why := e.veto(r, i); why != "" {
							cmdNote = fmt.Sprintf("command %q matches allow.commands[%d], but %s", cmd, i, why)
							break
						}
					}
					return Decision{Action: ActionAllow, Rule: fmt.Sprintf("allow.commands[%d]: %s", i, re), Reason: fmt.Sprintf("command %q matches allow.commands", cmd)}
				}
			}
			if cmdNote == "" {
				cmdNote = fmt.Sprintf("command %q matches no allow.commands rule", cmd)
			}
		}
	}

	// allow.tools applies to any other request, matched on title or name.
	names := toolNames(r)
	for i, re := range e.tools {
		for _, n := range names {
			if re.MatchString(n) {
				return Decision{Action: ActionAllow, Rule: fmt.Sprintf("allow.tools[%d]: %s", i, re), Reason: fmt.Sprintf("tool %q matches allow.tools", n)}
			}
		}
	}

	reason := "tool kind " + kindOrOther(r.Kind) + " is not permitted without approval"
	if r.Kind == "other" || r.Kind == "" {
		reason = "tool kind other (e.g. an MCP tool) is not in allow.tools"
	}
	if cmdNote != "" {
		reason = cmdNote
	}
	if editNote != "" {
		reason = editNote
	}
	suffix := ""
	if protected != "" {
		reason = "tool kind " + r.Kind + " names " + protected + ", which is not readable without approval"
		suffix = "; " + protected
	}
	if e.policy == PolicyGated {
		return Decision{Action: ActionAsk, Rule: "policy:gated" + suffix, Reason: reason + "; sent to the approval queue"}
	}
	return Decision{Action: ActionReject, Rule: "policy:readonly" + suffix, Reason: reason + "; rejected by the read-only policy"}
}

func kindOrOther(k string) string {
	if k == "" {
		return "other"
	}
	return k
}

// toolNames are the strings allow.tools is matched against: the title and any
// name-like field of the raw input.
func toolNames(r ToolRequest) []string {
	var names []string
	if r.Title != "" {
		names = append(names, r.Title)
	}
	if r.Name != "" {
		names = append(names, r.Name)
	}
	if m, ok := r.RawInput.(map[string]any); ok {
		for _, k := range []string{"name", "tool", "tool_name", "toolName"} {
			if s, ok := m[k].(string); ok && s != "" {
				names = append(names, s)
			}
		}
	}
	return names
}

// ExtractCommand returns the shell command of an execute-kind tool's raw
// input. It understands {"command": "<string>"}, {"command": ["argv", ...]}
// (a `sh -c <script>` wrapper is unwrapped), and the "cmd" spelling.
func ExtractCommand(raw any) (string, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	for _, key := range []string{"command", "cmd"} {
		switch v := m[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s, true
			}
		case []any:
			argv := make([]string, 0, len(v))
			for _, a := range v {
				s, ok := a.(string)
				if !ok {
					return "", false
				}
				argv = append(argv, s)
			}
			if len(argv) == 0 {
				return "", false
			}
			if isShell(argv[0]) && len(argv) >= 3 && strings.HasPrefix(argv[1], "-") && strings.Contains(argv[1], "c") {
				return strings.TrimSpace(argv[2]), true
			}
			return strings.TrimSpace(strings.Join(argv, " ")), true
		}
	}
	return "", false
}

func isShell(arg0 string) bool {
	i := strings.LastIndex(arg0, "/")
	switch arg0[i+1:] {
	case "sh", "bash", "zsh", "dash":
		return true
	}
	return false
}

// hasShellControl reports whether cmd contains an unquoted command separator,
// pipe, redirection, background operator, or any command/process
// substitution. Such a command could run something the allow rule never
// matched, so allow.commands refuses to match it.
func hasShellControl(cmd string) bool {
	inSingle, inDouble := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case c == '\\':
			i++ // escaped character
		case c == '`':
			return true
		case c == '$' && i+1 < len(cmd) && (cmd[i+1] == '(' || cmd[i+1] == '{'):
			return true
		case inDouble:
			if c == '"' {
				inDouble = false
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		case strings.ContainsRune(";&|<>\n\r()", rune(c)):
			return true
		}
	}
	return false
}
