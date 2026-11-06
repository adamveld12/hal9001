package sandbox

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"hal9001/internal/spec"
)

// Operations Decide knows. Every other operation (reads, listing, search) needs no approval.
const (
	OpShell   = "shell"
	OpFSWrite = "fs_write"
)

// Decision is the approval outcome for one tool call.
type Decision struct {
	// Action is spec.RuleAuto (run it), spec.RuleApprove (ask the user in the Inbox) or spec.RuleDeny (refuse).
	Action string
	// Reason says which policy produced the decision, for the approval card and for tool errors.
	Reason string
}

// Policy is a sandbox's compiled approval policy.
type Policy struct {
	mode  string
	shell []shellRule
	paths []pathRule
}

type shellRule struct {
	re     *regexp.Regexp
	action string
}

type pathRule struct {
	re     *regexp.Regexp
	glob   string
	action string
}

// NewPolicy compiles the approval settings of cfg.
func NewPolicy(cfg spec.Sandbox) (*Policy, error) {
	p := &Policy{mode: cfg.Approval}
	for i, r := range cfg.Rules.Shell {
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return nil, fmt.Errorf("approval_rules.shell[%d]: %w", i, err)
		}
		p.shell = append(p.shell, shellRule{re, r.Action})
	}
	for i, r := range cfg.Rules.FSWrite {
		re, err := regexp.Compile(globRegexp(r.Path))
		if err != nil {
			return nil, fmt.Errorf("approval_rules.fs_write[%d]: %w", i, err)
		}
		p.paths = append(p.paths, pathRule{re: re, glob: r.Path, action: r.Action})
	}
	return p, nil
}

// Decide returns the decision for op with the tool arguments: "command" for shell, "path" for fs_write.
//
//   - approval auto: shell and fs_write run.
//   - approval always: shell and fs_write wait for the user.
//   - approval rules: the first rule whose regex (shell, searched anywhere in the command) or glob (fs_write,
//     relative to the workdir) matches decides; with no match the user is asked. An fs_write path that
//     leaves the workdir is denied.
func (p *Policy) Decide(op string, args map[string]any) Decision {
	if op != OpShell && op != OpFSWrite {
		return Decision{spec.RuleAuto, "operation needs no approval"}
	}
	switch p.mode {
	case spec.ApprovalAuto:
		return Decision{spec.RuleAuto, "sandbox approval is auto"}
	case spec.ApprovalAlways:
		return Decision{spec.RuleApprove, "sandbox approval is always"}
	}
	if op == OpShell {
		cmd, _ := args["command"].(string)
		for i, r := range p.shell {
			if r.re.MatchString(cmd) {
				return Decision{r.action, fmt.Sprintf("shell rule %d (%s)", i+1, r.re)}
			}
		}
		return Decision{spec.RuleApprove, "no shell rule matches"}
	}
	raw, _ := args["path"].(string)
	rel := path.Clean(strings.TrimLeft(raw, "/"))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return Decision{spec.RuleDeny, "path leaves the workdir"}
	}
	for i, r := range p.paths {
		if r.re.MatchString(rel) {
			return Decision{r.action, fmt.Sprintf("fs_write rule %d (%s)", i+1, r.glob)}
		}
	}
	return Decision{spec.RuleApprove, "no fs_write rule matches"}
}

// globRegexp converts a glob (`*` within a segment, `**` across segments, `?` one non-slash character) to an
// anchored regular expression.
func globRegexp(glob string) string {
	glob = strings.TrimPrefix(path.Clean("/"+strings.TrimLeft(glob, "/")), "/")
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}
