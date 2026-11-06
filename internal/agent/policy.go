package agent

import (
	"fmt"
	"regexp"
)

// Action is the policy outcome for one permission request.
type Action string

const (
	ActionAllow  Action = "allow"
	ActionReject Action = "reject"
)

// ToolRequest is the part of a session/request_permission the policy sees.
type ToolRequest struct {
	Kind  string // ACP tool kind: read, edit, execute, other, ...
	Title string
	// Name is the tool name when the transport provides one separately.
	Name string
	// RawInput is the decoded rawInput of the tool call (usually a map).
	RawInput any
	// ToolCallID identifies the tool call within the session.
	ToolCallID string
}

// Decision is a policy outcome with the evidence recorded on the
// permission request.
type Decision struct {
	Action Action
	// Rule names what matched, e.g. "hal:mcp", "allow.tools[0]: ^mcp__linear__".
	Rule   string
	Reason string
}

// halToolPatterns match the names runtimes give hal's own MCP tools (see HalToolPatterns).
var halToolPatterns = compileAll(HalToolPatterns)

// HalToolPatterns match hal's own MCP tools by title or name across runtimes (claude: mcp__hal__gh_api). hal decides
// at execution time inside its MCP server (approval, read-only, dry-run), so the agent layer lets them through.
var HalToolPatterns = []string{`^mcp__hal__`, `^mcp__hal_`, `^hal[._:/ -]`, `^hal \(MCP\)`}

func compileAll(in []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(in))
	for i, s := range in {
		out[i] = regexp.MustCompile(s)
	}
	return out
}

// Engine applies the lockdown policy. Agents run with no built-in tools (docs/acp-lockdown.md), so the only
// permission requests that are legitimate are for hal's own MCP tools and for tools of MCP servers the node declared
// (allow.tools); everything else is rejected, whatever the node's mode.
type Engine struct {
	policy Policy
	tools  []*regexp.Regexp
}

// NewEngine compiles the allow rules. The policy only selects what is recorded; it does not widen what is allowed.
func NewEngine(p Policy, allow AllowRules) (*Engine, error) {
	e := &Engine{policy: p}
	for _, s := range allow.Tools {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("allow.tools %q: %w", s, err)
		}
		e.tools = append(e.tools, re)
	}
	switch p {
	case PolicyReadonly, PolicyGated, PolicyAuto:
	default:
		return nil, fmt.Errorf("unknown policy %q", p)
	}
	return e, nil
}

// Decide applies the policy to one request.
func (e *Engine) Decide(r ToolRequest) Decision {
	names := toolNames(r)
	for _, re := range halToolPatterns {
		for _, n := range names {
			if re.MatchString(n) {
				return Decision{Action: ActionAllow, Rule: "hal:mcp", Reason: fmt.Sprintf("tool %q is one of hal's MCP tools; hal approves inside its server", n)}
			}
		}
	}
	for i, re := range e.tools {
		for _, n := range names {
			if re.MatchString(n) {
				return Decision{Action: ActionAllow, Rule: fmt.Sprintf("allow.tools[%d]: %s", i, re), Reason: fmt.Sprintf("tool %q matches allow.tools", n)}
			}
		}
	}
	return Decision{Action: ActionReject, Rule: "lockdown", Reason: "agents have no built-in tools; only hal's MCP tools and allow.tools are permitted (kind " + kindOrOther(r.Kind) + ")"}
}

func kindOrOther(k string) string {
	if k == "" {
		return "other"
	}
	return k
}

// toolNames are the strings the rules are matched against: the title and any name-like field of the raw input.
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
