package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ---- gh_api ----

var (
	ghReviewRE  = regexp.MustCompile(`^/?repos/([^/]+)/([^/]+)/pulls/(\d+)/reviews/?$`)
	ghCommentRE = regexp.MustCompile(`^/?repos/([^/]+)/([^/]+)/(?:issues|pulls)/(\d+)/comments/?$`)
	ghMergeRE   = regexp.MustCompile(`^/?repos/([^/]+)/([^/]+)/pulls/(\d+)/merge/?$`)
	ghEditRE    = regexp.MustCompile(`^/?repos/([^/]+)/([^/]+)/(issues|pulls)/(\d+)/?$`)
	ghNewRE     = regexp.MustCompile(`^/?repos/([^/]+)/([^/]+)/(issues|pulls)/?$`)
)

// ghAPIDescribe says what a gh_api write does: the well-known review, comment, merge and edit endpoints by name, any
// other by method and path.
func ghAPIDescribe(args json.RawMessage, _ string) ApprovalText {
	a, err := parseGHAPI(args)
	if err != nil {
		m := writeMethod(args)
		if m == "" {
			m = "write"
		}
		return ApprovalText{Title: "GitHub " + m + " (invalid arguments)", Description: "Call the GitHub API with " + m + " (the arguments are invalid, so it will fail)."}
	}
	path := strings.SplitN(a.Path, "?", 2)[0]
	at := ApprovalText{Command: "gh api -X " + a.Method + " " + a.Path}
	at.Fields = []ApprovalField{{"method", a.Method}, {"path", a.Path}}
	for _, k := range []string{"event", "state", "title", "merge_method"} {
		if v, ok := a.Params[k].(string); ok && v != "" {
			at.Fields = append(at.Fields, ApprovalField{k, v})
		}
	}
	ref := func(m []string) string { return fmt.Sprintf("%s/%s#%s", m[1], m[2], m[len(m)-1]) }
	switch {
	case a.Method == "POST" && ghReviewRE.MatchString(path):
		at.Description = "Post a review on " + ref(ghReviewRE.FindStringSubmatch(path))
		if ev, _ := a.Params["event"].(string); ev != "" {
			at.Description += " (" + strings.ToLower(strings.ReplaceAll(ev, "_", " ")) + ")"
		}
	case a.Method == "POST" && ghCommentRE.MatchString(path):
		at.Description = "Comment on " + ref(ghCommentRE.FindStringSubmatch(path))
	case a.Method == "PUT" && ghMergeRE.MatchString(path):
		at.Description = "Merge " + ref(ghMergeRE.FindStringSubmatch(path))
		if mm, _ := a.Params["merge_method"].(string); mm != "" {
			at.Description += " (" + mm + ")"
		}
	case (a.Method == "PATCH" || a.Method == "PUT") && ghEditRE.MatchString(path):
		m := ghEditRE.FindStringSubmatch(path)
		verb := "Edit"
		if st, _ := a.Params["state"].(string); st == "closed" {
			verb = "Close"
		}
		at.Description = fmt.Sprintf("%s %s/%s#%s", verb, m[1], m[2], m[4])
	case a.Method == "POST" && ghNewRE.MatchString(path):
		m := ghNewRE.FindStringSubmatch(path)
		what := "an issue"
		if m[3] == "pulls" {
			what = "a pull request"
		}
		at.Description = fmt.Sprintf("Open %s in %s/%s", what, m[1], m[2])
		if t, _ := a.Params["title"].(string); t != "" {
			at.Description += ": " + ClipRunes(oneLine(t), 100)
		}
	default:
		at.Description = fmt.Sprintf("Call the GitHub API: %s %s", a.Method, a.Path)
	}
	if body, _ := a.Params["body"].(string); body != "" {
		at.Fields = append(at.Fields, ApprovalField{"body", ClipRunes(oneLine(body), maxFactValue)})
	}
	at.Description += "."
	at.Title = ClipRunes(strings.TrimSuffix(at.Description, "."), 120)
	return at
}

// ---- sandbox tools ----

func (s *sandboxTools) describeWrite(args json.RawMessage, _ string) ApprovalText {
	var a struct{ Path, Content string }
	_ = json.Unmarshal(args, &a)
	return ApprovalText{
		Title:       "Write " + a.Path,
		Description: fmt.Sprintf("Write %s (%d bytes) in sandbox %s", a.Path, len(a.Content), s.name),
		Fields:      []ApprovalField{{"path", a.Path}, {"bytes", fmt.Sprint(len(a.Content))}, {"sandbox", s.name}},
	}
}

func (s *sandboxTools) describeShell(args json.RawMessage, _ string) ApprovalText {
	var a struct {
		Command        string `json:"command"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(args, &a)
	at := ApprovalText{
		Title:       "Run shell command in sandbox " + s.name,
		Description: fmt.Sprintf("Run %s in sandbox %s", inlineCode(a.Command), s.name),
		Command:     a.Command,
		Fields:      []ApprovalField{{"sandbox", s.name}},
	}
	if a.TimeoutSeconds > 0 {
		at.Fields = append(at.Fields, ApprovalField{"timeout", fmt.Sprintf("%ds", a.TimeoutSeconds)})
	}
	return at
}

// ---- custom tools ----

// describeCustom says what a call of the custom tool id runs: its key arguments, the sandbox when it runs in one, and
// the script or command line.
func describeCustom(id, lang, script string, command []string, inSandbox bool, sandboxName string) func(json.RawMessage, string) ApprovalText {
	return func(args json.RawMessage, _ string) ApprovalText {
		fields := factFields(args)
		desc := "Run custom tool " + id
		if len(fields) > 0 {
			desc += " with " + factSummary(fields, 3)
		}
		at := ApprovalText{Title: "Run custom tool " + id, Fields: fields}
		if inSandbox {
			name := "the node's sandbox"
			if sandboxName != "" {
				name = "sandbox " + sandboxName
			}
			desc += " in " + name
			at.Fields = append(at.Fields, ApprovalField{"sandbox", sandboxName})
		}
		at.Description = desc
		if lang == "exec" && len(command) > 0 {
			quoted := make([]string, len(command))
			for i, c := range command {
				quoted[i] = shellQuote(c)
			}
			at.Command = strings.Join(quoted, " ")
		} else {
			at.Command = script
		}
		at.Fields = append([]ApprovalField{{"tool", id}}, at.Fields...)
		return at
	}
}

// ---- emit_<port> ----

// DescribeEmit returns the Describe of an emit_<port> tool that needs approval before it hands the result on: it
// names the flow and node, the port and the node the port leads to (next may be empty), and summarizes the payload.
func DescribeEmit(flowID, nodeID, port, next string) func(json.RawMessage, string) ApprovalText {
	return func(args json.RawMessage, _ string) ApprovalText {
		where := nodeID
		if flowID != "" {
			where = flowID + "/" + nodeID
		}
		desc := fmt.Sprintf("Finish %s on port %q", where, port)
		if next != "" {
			desc += ", which continues to " + next
		}
		if p := compactJSON(args); p != "" && p != "{}" {
			desc += ". Payload: " + ClipRunes(p, 300)
		}
		at := ApprovalText{
			Title:       fmt.Sprintf("Emit %s from %s", port, where),
			Description: desc,
			Fields:      []ApprovalField{{"flow", flowID}, {"node", nodeID}, {"port", port}},
		}
		if next != "" {
			at.Fields = append(at.Fields, ApprovalField{"next", next})
		}
		return at
	}
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	return buf.String()
}
