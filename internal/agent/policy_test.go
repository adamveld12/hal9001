package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hal9001/internal/agent"
)

func TestPolicyDecisionTable(t *testing.T) {
	cmd := func(c string) any { return map[string]any{"command": c} }
	allow := agent.AllowRules{
		Tools:    []string{`^mcp__linear__(get|list)_`},
		Commands: []string{`^gh (pr|issue) (view|diff|list)\b`, `^git (log|diff|show)\b`},
	}
	for _, tc := range []struct {
		name   string
		policy agent.Policy
		allow  agent.AllowRules
		req    agent.ToolRequest
		want   agent.Action
		rule   string // substring of the matched rule
	}{
		{"auto allows everything", agent.PolicyAuto, agent.AllowRules{}, agent.ToolRequest{Kind: "execute", RawInput: cmd("rm -rf /")}, agent.ActionAllow, "policy:auto"},
		{"readonly read", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "read"}, agent.ActionAllow, "kind:read"},
		{"readonly search", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "search"}, agent.ActionAllow, "kind:search"},
		{"readonly fetch", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "fetch"}, agent.ActionAllow, "kind:fetch"},
		{"readonly think", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "think"}, agent.ActionAllow, "kind:think"},
		{"readonly edit", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "edit"}, agent.ActionReject, "policy:readonly"},
		{"readonly delete", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "delete"}, agent.ActionReject, "policy:readonly"},
		{"readonly move", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "move"}, agent.ActionReject, "policy:readonly"},
		{"readonly execute without rules", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "execute", RawInput: cmd("gh pr view 1")}, agent.ActionReject, "policy:readonly"},
		{"readonly switch_mode", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{Kind: "switch_mode"}, agent.ActionReject, "policy:readonly"},
		{"readonly MCP tool not listed", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "other", Title: "mcp__slack__post_message"}, agent.ActionReject, "policy:readonly"},
		{"readonly MCP tool listed", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "other", Title: "mcp__linear__get_issue"}, agent.ActionAllow, "allow.tools[0]"},
		{"tool listed via raw input name", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "other", Title: "Linear", RawInput: map[string]any{"name": "mcp__linear__list_issues"}}, agent.ActionAllow, "allow.tools[0]"},
		{"tool name field", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "other", Name: "mcp__linear__get_issue"}, agent.ActionAllow, "allow.tools[0]"},
		{"gh pr view", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("gh pr view 12 --json title,body")}, agent.ActionAllow, "allow.commands[0]"},
		{"gh pr comment", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("gh pr comment 12 -b hi")}, agent.ActionReject, "policy:readonly"},
		{"git log second rule", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git log --oneline -5")}, agent.ActionAllow, "allow.commands[1]"},
		{"prefix is not enough: gh pr viewer", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("gh pr viewer")}, agent.ActionReject, "policy:readonly"},
		{"chained with &&", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("gh pr view 1 && gh pr comment 1 -b x")}, agent.ActionReject, "policy:readonly"},
		{"chained with ;", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git log; rm -rf .")}, agent.ActionReject, "policy:readonly"},
		{"piped", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git log | sh")}, agent.ActionReject, "policy:readonly"},
		{"redirect", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git diff > /etc/x")}, agent.ActionReject, "policy:readonly"},
		{"command substitution", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git log $(rm -rf .)")}, agent.ActionReject, "policy:readonly"},
		{"backticks in double quotes", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git log --grep \"`id`\"")}, agent.ActionReject, "policy:readonly"},
		{"operator inside single quotes is data", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("gh pr list --search 'a && b | c'")}, agent.ActionAllow, "allow.commands[0]"},
		{"no extractable command", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", Title: "gh pr view 1", RawInput: map[string]any{"description": "view"}}, agent.ActionReject, "policy:readonly"},
		{"title is not trusted as the command", agent.PolicyReadonly, allow, agent.ToolRequest{Kind: "execute", Title: "gh pr view 1", RawInput: cmd("gh pr comment 1 -b x")}, agent.ActionReject, "policy:readonly"},
		{"gated read", agent.PolicyGated, agent.AllowRules{}, agent.ToolRequest{Kind: "read"}, agent.ActionAllow, "kind:read"},
		{"gated edit asks", agent.PolicyGated, agent.AllowRules{}, agent.ToolRequest{Kind: "edit"}, agent.ActionAsk, "policy:gated"},
		{"gated MCP asks", agent.PolicyGated, agent.AllowRules{}, agent.ToolRequest{Kind: "other", Title: "mcp__x__y"}, agent.ActionAsk, "policy:gated"},
		{"gated allow rule still applies", agent.PolicyGated, allow, agent.ToolRequest{Kind: "execute", RawInput: cmd("git show HEAD")}, agent.ActionAllow, "allow.commands[1]"},
		{"empty kind is other", agent.PolicyReadonly, agent.AllowRules{}, agent.ToolRequest{}, agent.ActionReject, "policy:readonly"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := agent.NewEngine(tc.policy, tc.allow)
			if err != nil {
				t.Fatal(err)
			}
			d := eng.Decide(tc.req)
			if d.Action != tc.want || !strings.Contains(d.Rule, tc.rule) || d.Reason == "" {
				t.Errorf("Decide = %+v, want %s via %q", d, tc.want, tc.rule)
			}
		})
	}
}

func TestExtractCommandShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
		ok   bool
	}{
		{"string", map[string]any{"command": "echo hi"}, "echo hi", true},
		{"cmd key", map[string]any{"cmd": "echo hi"}, "echo hi", true},
		{"argv", map[string]any{"command": []any{"git", "log", "-5"}}, "git log -5", true},
		{"bash -lc wrapper", map[string]any{"command": []any{"bash", "-lc", "gh pr view 3"}}, "gh pr view 3", true},
		{"/bin/sh -c wrapper", map[string]any{"command": []any{"/bin/sh", "-c", "gh pr view 3"}}, "gh pr view 3", true},
		{"extra keys ignored", map[string]any{"command": "ls", "description": "list"}, "ls", true},
		{"missing", map[string]any{"path": "x"}, "", false},
		{"empty string", map[string]any{"command": "  "}, "", false},
		{"non-string argv", map[string]any{"command": []any{"ls", 1}}, "", false},
		{"not an object", "ls", "", false},
		{"nil", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := agent.ExtractCommand(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ExtractCommand = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestNewEngineRejectsBadConfig(t *testing.T) {
	if _, err := agent.NewEngine(agent.PolicyReadonly, agent.AllowRules{Commands: []string{"("}}); err == nil {
		t.Error("invalid commands regex accepted")
	}
	if _, err := agent.NewEngine(agent.PolicyReadonly, agent.AllowRules{Tools: []string{"[a-"}}); err == nil {
		t.Error("invalid tools regex accepted")
	}
	if _, err := agent.NewEngine("yolo", agent.AllowRules{}); err == nil {
		t.Error("unknown policy accepted")
	}
}

func TestPolicyForPhases(t *testing.T) {
	for _, tc := range []struct {
		mode  agent.Mode
		style agent.ApproveStyle
		phase agent.Phase
		want  agent.Policy
	}{
		{agent.ModeAuto, "", agent.PhaseExecute, agent.PolicyAuto},
		{agent.ModeReadonly, "", agent.PhaseExecute, agent.PolicyReadonly},
		{agent.ModeApprove, agent.ApproveProposal, agent.PhasePropose, agent.PolicyReadonly},
		{agent.ModeApprove, agent.ApproveProposal, agent.PhaseExecute, agent.PolicyAuto},
		{agent.ModeApprove, agent.ApprovePerTool, agent.PhaseExecute, agent.PolicyGated},
	} {
		if got := agent.PolicyFor(tc.mode, tc.style, tc.phase); got != tc.want {
			t.Errorf("PolicyFor(%s,%s,%s) = %s, want %s", tc.mode, tc.style, tc.phase, got, tc.want)
		}
	}
}

func TestParseNote(t *testing.T) {
	valid := `{"title":"T","priority":"urgent","summary_md":"s","suggested_reply":"r","links":[{"label":"l","url":"https://x.test"}]}`
	for _, tc := range []struct {
		name     string
		in       string
		wantErr  string // substring; empty = success
		title    string
		priority string
	}{
		{"bare json", valid, "", "T", "urgent"},
		{"fenced json", "Result:\n```json\n" + valid + "\n```", "", "T", "urgent"},
		{"fenced without language", "```\n" + valid + "\n```\nHope that helps!", "", "T", "urgent"},
		{"prose before and after", "Sure. " + valid + " Anything else?", "", "T", "urgent"},
		{"last object wins", `{"title":"old"} then ` + `{"title":"new","priority":"low","summary_md":""}`, "", "new", "low"},
		{"missing priority defaults to normal", `{"title":"T","summary_md":"s"}`, "", "T", "normal"},
		{"braces inside strings", `{"title":"a } b","summary_md":"{x}"}`, "", "a } b", "normal"},
		{"no json", "I looked and found nothing.", "no JSON", "", ""},
		{"truncated json", `{"title":"T","summary_md":"s`, "no JSON", "", ""},
		{"empty title", `{"title":"  ","summary_md":"s"}`, "no title", "", ""},
		{"bad priority", `{"title":"T","priority":"critical"}`, "priority", "", ""},
		{"wrong type", `{"title":5}`, "schema", "", ""},
		{"link without url", `{"title":"T","links":[{"label":"x","url":""}]}`, "url", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := agent.ParseNote(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n.Title != tc.title || n.Priority != tc.priority {
				t.Errorf("note = %+v, want title %q priority %q", n, tc.title, tc.priority)
			}
		})
	}
}

// pathFixture is a directory tree for the path rules: draft/ (editable),
// draft2/ (a sibling sharing the prefix), outside/, secrets/, and symlinks.
type pathFixture struct {
	root, draft string
}

func newPathFixture(t *testing.T) pathFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"draft/sub", "draft2", "outside", "secrets/nested"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"draft/escape":  filepath.Join(root, "outside"),   // inside draft, points out
		"draft/dangler": filepath.Join(root, "no-target"), // dangling
		"alias":         root,                             // another spelling of root, like /var -> /private/var
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	return pathFixture{root: root, draft: filepath.Join(root, "draft")}
}

func TestEditUnderRule(t *testing.T) {
	fx := newPathFixture(t)
	abs := func(p string) string { return filepath.Join(fx.root, p) }
	for _, tc := range []struct {
		name      string
		policy    agent.Policy
		editUnder []string
		req       agent.ToolRequest
		want      agent.Action
	}{
		{"file in dir", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"file in nested dir that does not exist yet", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/new/deeper/a.txt")}}, agent.ActionAllow},
		{"relative path resolves against base", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{"draft/a.txt"}}, agent.ActionAllow},
		{"relative dir resolves against base", agent.PolicyGated, []string{"draft"}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"dir itself", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft")}}, agent.ActionAllow},
		{"readonly policy too", agent.PolicyReadonly, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"delete", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "delete", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"move inside", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "move", Paths: []string{abs("draft/a.txt"), abs("draft/sub/a.txt")}}, agent.ActionAllow},
		{"second dir", agent.PolicyGated, []string{abs("outside"), abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"path spelled through a symlink alias", agent.PolicyGated, []string{abs("alias/draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"dir spelled through a symlink alias", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("alias/draft/a.txt")}}, agent.ActionAllow},

		{"dot-dot escape", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/../outside/a.txt")}}, agent.ActionAsk},
		{"relative dot-dot escape", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{"draft/../../etc/passwd"}}, agent.ActionAsk},
		{"sibling with the same prefix", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft2/a.txt")}}, agent.ActionAsk},
		{"sibling with the same prefix, readonly", agent.PolicyReadonly, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft2/a.txt")}}, agent.ActionReject},
		{"symlink inside the dir pointing out", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/escape/a.txt")}}, agent.ActionAsk},
		{"dangling symlink inside the dir", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/dangler")}}, agent.ActionAsk},
		{"no paths named", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "edit"}, agent.ActionAsk},
		{"one of two paths outside", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "move", Paths: []string{abs("draft/a.txt"), abs("outside/a.txt")}}, agent.ActionAsk},
		{"no edit_under configured", agent.PolicyGated, nil, agent.ToolRequest{Kind: "edit", Paths: []string{abs("draft/a.txt")}}, agent.ActionAsk},
		{"execute is not an edit", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "execute", Paths: []string{abs("draft/a.txt")}, RawInput: map[string]any{"command": "touch a"}}, agent.ActionAsk},
		{"other kind is not an edit", agent.PolicyGated, []string{abs("draft")}, agent.ToolRequest{Kind: "other", Paths: []string{abs("draft/a.txt")}}, agent.ActionAsk},
		{"auto policy needs no rule", agent.PolicyAuto, nil, agent.ToolRequest{Kind: "edit"}, agent.ActionAllow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, err := agent.NewEngine(tc.policy, agent.AllowRules{EditUnder: tc.editUnder, Base: fx.root})
			if err != nil {
				t.Fatal(err)
			}
			d := eng.Decide(tc.req)
			if d.Action != tc.want {
				t.Fatalf("Decide = %+v, want %s", d, tc.want)
			}
			if tc.want == agent.ActionAllow && tc.policy != agent.PolicyAuto && !strings.HasPrefix(d.Rule, "allow.edit_under[") {
				t.Errorf("rule = %q", d.Rule)
			}
		})
	}
}

func TestEditUnderRuleNamesTheDirectory(t *testing.T) {
	fx := newPathFixture(t)
	eng, err := agent.NewEngine(agent.PolicyGated, agent.AllowRules{EditUnder: []string{filepath.Join(fx.root, "outside"), fx.draft}})
	if err != nil {
		t.Fatal(err)
	}
	d := eng.Decide(agent.ToolRequest{Kind: "edit", Paths: []string{filepath.Join(fx.draft, "a.txt")}})
	if want := "allow.edit_under[1]: " + fx.draft; d.Rule != want {
		t.Errorf("rule = %q, want %q", d.Rule, want)
	}
}

func TestProtectedPathsAreNotReadableByKind(t *testing.T) {
	fx := newPathFixture(t)
	abs := func(p string) string { return filepath.Join(fx.root, p) }
	tools := agent.AllowRules{Tools: []string{`^Read secrets$`}}
	for _, tc := range []struct {
		name      string
		policy    agent.Policy
		protected []string
		allow     agent.AllowRules
		req       agent.ToolRequest
		want      agent.Action
	}{
		{"read in protected dir asks", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("secrets/key")}}, agent.ActionAsk},
		{"read in protected dir is rejected when readonly", agent.PolicyReadonly, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("secrets/key")}}, agent.ActionReject},
		{"protected single file", agent.PolicyGated, []string{abs("draft/token")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("draft/token")}}, agent.ActionAsk},
		{"other file next to protected file", agent.PolicyGated, []string{abs("draft/token")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("draft/token2")}}, agent.ActionAllow},
		{"search and fetch and think are covered", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "search", Paths: []string{abs("secrets/nested")}}, agent.ActionAsk},
		{"relative path resolves against base", agent.PolicyGated, []string{"secrets"}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{"secrets/nested/k"}}, agent.ActionAsk},
		{"dot-dot into protected", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("draft/../secrets/key")}}, agent.ActionAsk},
		{"symlink alias of protected dir", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("alias/secrets/key")}}, agent.ActionAsk},
		{"any of several paths", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("draft/a"), abs("secrets/key")}}, agent.ActionAsk},
		{"unprotected read", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("draft/a.txt")}}, agent.ActionAllow},
		{"sibling with the same prefix", agent.PolicyGated, []string{abs("draft")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("draft2/a")}}, agent.ActionAllow},
		{"no paths named", agent.PolicyGated, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read"}, agent.ActionAllow},
		{"allow.tools still applies", agent.PolicyGated, []string{abs("secrets")}, tools, agent.ToolRequest{Kind: "read", Title: "Read secrets", Paths: []string{abs("secrets/key")}}, agent.ActionAllow},
		{"auto policy ignores protected", agent.PolicyAuto, []string{abs("secrets")}, agent.AllowRules{}, agent.ToolRequest{Kind: "read", Paths: []string{abs("secrets/key")}}, agent.ActionAllow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rules := tc.allow
			rules.Protected, rules.Base = tc.protected, fx.root
			eng, err := agent.NewEngine(tc.policy, rules)
			if err != nil {
				t.Fatal(err)
			}
			d := eng.Decide(tc.req)
			if d.Action != tc.want {
				t.Fatalf("Decide = %+v, want %s", d, tc.want)
			}
			if tc.want != agent.ActionAllow && (!strings.Contains(d.Reason, "protected:") || !strings.Contains(d.Rule, "protected:")) {
				t.Errorf("the protected path is not named: %+v", d)
			}
		})
	}
}

func TestToolPaths(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       any
		content   string
		locations []string
		want      []string
	}{
		{"nothing", nil, "", nil, nil},
		{"locations", nil, "", []string{"/a/b", "", "/a/c"}, []string{"/a/b", "/a/c"}},
		{"diff content", nil, `[{"type":"diff","path":"/a/d.go","newText":"x"},{"type":"content","content":{"type":"text","text":"hi"}}]`, nil, []string{"/a/d.go"}},
		{"content that is not a list", nil, `{"type":"diff","path":"/a/d.go"}`, nil, nil},
		{"file_path", map[string]any{"file_path": "/a/f.go", "old_string": "x"}, "", nil, []string{"/a/f.go"}},
		{"single path keys", map[string]any{"filePath": "/1", "path": "/2", "notebook_path": "/3", "target_file": "/4", "file": "/5"}, "", nil, []string{"/1", "/2", "/3", "/4", "/5"}},
		{"move keys", map[string]any{"old_path": "/o", "new_path": "/n", "source": "/s", "destination": "/d"}, "", nil, []string{"/o", "/n", "/s", "/d"}},
		{"paths array", map[string]any{"paths": []any{"/x", "/y", 3}}, "", nil, []string{"/x", "/y"}},
		{"changes object keys sorted", map[string]any{"changes": map[string]any{"/z": map[string]any{}, "/a": map[string]any{}}}, "", nil, []string{"/a", "/z"}},
		{"files array of objects", map[string]any{"files": []any{map[string]any{"path": "/p"}, map[string]any{"file_path": "/q"}, map[string]any{"other": 1}}}, "", nil, []string{"/p", "/q"}},
		{"files array of strings", map[string]any{"files": []any{"/p", "/q"}}, "", nil, []string{"/p", "/q"}},
		{"deduplicated across sources, locations first", map[string]any{"path": "/a/b"}, `[{"type":"diff","path":"/a/b"},{"type":"diff","path":"/a/c"}]`, []string{"/a/b"}, []string{"/a/b", "/a/c"}},
		{"command input names no path", map[string]any{"command": "rm -rf /"}, "", nil, nil},
		{"raw input that is not an object", "x", "", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.ToolPaths(tc.raw, json.RawMessage(tc.content), tc.locations)
			if !slices.Equal(got, tc.want) {
				t.Errorf("ToolPaths = %q, want %q", got, tc.want)
			}
		})
	}
}
