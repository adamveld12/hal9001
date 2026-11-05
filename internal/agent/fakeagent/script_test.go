package fakeagent

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRejectsMalformedScripts(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Script
		want string
	}{
		{"unknown op", Script{Turns: []Turn{{Steps: []Step{{Op: "teleport"}}}}}, "unknown op"},
		{"message without text", Script{Turns: []Turn{{Steps: []Step{{Op: OpMessage}}}}}, "needs text"},
		{"tool call without id", Script{Turns: []Turn{{Steps: []Step{{Op: OpToolCall}}}}}, "needs id"},
		{"nested bad step", Script{Turns: []Turn{{Steps: []Step{{Op: OpPermission, ID: "p", OnAllow: []Step{{Op: OpWriteFile}}}}}}}, "needs path"},
		{"when.mode not advertised", Script{Modes: []Mode{{ID: "a"}}, Turns: []Turn{{When: &When{Mode: "b"}}}}, "not in modes"},
		{"bad regexp", Script{Turns: []Turn{{When: &When{PromptMatches: "("}}}}, "prompt_matches"},
		{"initial mode unknown", Script{Modes: []Mode{{ID: "a"}}, InitialMode: "z"}, "initial_mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want error mentioning %q", err, tc.want)
			}
		})
	}
}

func TestScriptRoundTripsThroughFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	in := Script{LoadSession: true, Modes: []Mode{{ID: "default"}}, Turns: []Turn{{Steps: []Step{{Op: OpMessage, Text: "hi"}}}}}
	if err := in.Write(path); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.LoadSession || len(out.Turns) != 1 || out.Turns[0].Steps[0].Text != "hi" {
		t.Errorf("round trip lost data: %+v", out)
	}
}
