package sandbox

import (
	"testing"

	"hal9001/internal/spec"
)

func TestDecide(t *testing.T) {
	cfg := spec.DefaultSandbox(spec.SandboxLocal)
	cfg.Approval = spec.ApprovalRules
	cfg.Rules = spec.SandboxRules{
		Shell: []spec.SandboxShellRule{
			{Match: `^rm\s`, Action: spec.RuleDeny},
			{Match: `^(ls|cat|git status)\b`, Action: spec.RuleAuto},
		},
		FSWrite: []spec.SandboxPathRule{
			{Path: ".github/**", Action: spec.RuleDeny},
			{Path: "notes/*.md", Action: spec.RuleAuto},
			{Path: "**/*.txt", Action: spec.RuleAuto},
		},
	}
	p, err := NewPolicy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		op   string
		args map[string]any
		want string
	}{
		{OpShell, map[string]any{"command": "rm -rf /"}, spec.RuleDeny},
		{OpShell, map[string]any{"command": "ls -la"}, spec.RuleAuto},
		{OpShell, map[string]any{"command": "curl example.com"}, spec.RuleApprove},
		{OpFSWrite, map[string]any{"path": ".github/workflows/ci.yml"}, spec.RuleDeny},
		{OpFSWrite, map[string]any{"path": "notes/a.md"}, spec.RuleAuto},
		{OpFSWrite, map[string]any{"path": "notes/deep/a.md"}, spec.RuleApprove},
		{OpFSWrite, map[string]any{"path": "x/y/z.txt"}, spec.RuleAuto},
		{OpFSWrite, map[string]any{"path": "z.txt"}, spec.RuleAuto},
		{OpFSWrite, map[string]any{"path": "/notes/a.md"}, spec.RuleAuto},
		{OpFSWrite, map[string]any{"path": "../escape.txt"}, spec.RuleDeny},
		{"fs_read", map[string]any{"path": "x"}, spec.RuleAuto},
	} {
		if got := p.Decide(tc.op, tc.args); got.Action != tc.want {
			t.Errorf("Decide(%s, %v) = %+v, want %s", tc.op, tc.args, got, tc.want)
		}
	}
	for mode, want := range map[string]string{spec.ApprovalAuto: spec.RuleAuto, spec.ApprovalAlways: spec.RuleApprove} {
		cfg.Approval = mode
		p, _ := NewPolicy(cfg)
		if got := p.Decide(OpShell, map[string]any{"command": "rm -rf /"}); got.Action != want {
			t.Errorf("mode %s: %+v, want %s", mode, got, want)
		}
		if got := p.Decide("fs_read", nil); got.Action != spec.RuleAuto {
			t.Errorf("mode %s: reads must not need approval, got %+v", mode, got)
		}
	}
}
