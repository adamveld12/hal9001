package agent_test

import (
	"strings"
	"testing"

	"hal9001/internal/agent"
)

func TestNewEngineRejectsBadConfig(t *testing.T) {
	if _, err := agent.NewEngine(agent.PolicyReadonly, agent.AllowRules{Tools: []string{"[a-"}}); err == nil {
		t.Error("invalid tools regex accepted")
	}
	if _, err := agent.NewEngine("yolo", agent.AllowRules{}); err == nil {
		t.Error("unknown policy accepted")
	}
}

// Agents have no built-in tools: hal's own MCP tools (any runtime's spelling) and allow.tools are allowed, everything
// else is rejected whatever the node's mode.
func TestLockdownPolicy(t *testing.T) {
	e, err := agent.NewEngine(agent.PolicyGated, agent.AllowRules{Tools: []string{`^mcp__linear__`}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		req  agent.ToolRequest
		want agent.Action
	}{
		{agent.ToolRequest{Kind: "other", Title: "mcp__hal__gh_api"}, agent.ActionAllow},
		{agent.ToolRequest{Kind: "other", Title: "mcp__hal_now"}, agent.ActionAllow},
		{agent.ToolRequest{Kind: "other", Name: "hal.gh_api"}, agent.ActionAllow},
		{agent.ToolRequest{Kind: "other", Title: "mcp__linear__list_issues"}, agent.ActionAllow},
		{agent.ToolRequest{Kind: "other", Title: "mcp__halloween__x"}, agent.ActionReject},
		{agent.ToolRequest{Kind: "read", Title: "Read /etc/hosts"}, agent.ActionReject},
		{agent.ToolRequest{Kind: "execute", Title: "echo hi", RawInput: map[string]any{"command": "echo hi"}}, agent.ActionReject},
		{agent.ToolRequest{Kind: "edit", Title: "Write x"}, agent.ActionReject},
		{agent.ToolRequest{Kind: "fetch", Title: "WebFetch"}, agent.ActionReject},
	} {
		if got := e.Decide(tc.req); got.Action != tc.want {
			t.Errorf("%+v: %s (%s), want %s", tc.req, got.Action, got.Reason, tc.want)
		}
	}
}

func TestGuardPrompt(t *testing.T) {
	for in, want := range map[string]string{
		"/mcp add x":         "[hal9001]\n/mcp add x",
		"  \n/export /tmp/x": "[hal9001]\n  \n/export /tmp/x",
		"review this":        "review this",
		"<hal_context>\n/x":  "<hal_context>\n/x",
	} {
		if got := agent.GuardPrompt(in); got != want {
			t.Errorf("GuardPrompt(%q) = %q, want %q", in, got, want)
		}
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
