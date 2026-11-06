package content

import (
	"testing"
)

func TestFilterNodes(t *testing.T) {
	cases := []struct {
		flow, node, input string
		keep              bool
	}{
		{"triage-github", "only-review-requests", `{"reason":"review_requested","type":"PullRequest"}`, true},
		{"triage-github", "only-review-requests", `{"reason":"mention","type":"PullRequest"}`, false},
		{"triage-github", "only-review-requests", `{"reason":"review_requested","type":"Issue"}`, false},
		{"triage-slack", "filter", `{"text":"hello"}`, true},
		{"triage-slack", "filter", `{"text":"  \n"}`, false},
		{"linear-triage", "skip-closed", `{"state_type":"started"}`, true},
		{"linear-triage", "skip-closed", `{"state_type":"completed"}`, false},
		{"linear-triage", "skip-closed", `{"kind":"comment"}`, true},
		{"email-secretary", "skip-automated", `{"from":"Alice <alice@example.com>"}`, true},
		{"email-secretary", "skip-automated", `{"from":"GitHub <notifications@github.com>"}`, false},
		{"email-secretary", "skip-automated", `{"from":"App <NoReply@example.com>"}`, false},
	}
	for _, tc := range cases {
		out := jqNode(t, node(t, tc.flow, tc.node).Script, tc.input)
		if (out != "") != tc.keep {
			t.Errorf("%s/%s on %s: keep=%v, output %q", tc.flow, tc.node, tc.input, tc.keep, out)
		}
	}
}
