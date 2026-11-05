package agent_test

import (
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
