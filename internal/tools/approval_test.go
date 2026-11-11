package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
)

func TestDeniedText(t *testing.T) {
	steered := `Denied by the user: "use the staging bucket". Do not retry the same action unchanged; do what the user asked instead.`
	held := "Denied by the user. The steering limit for this conversation was reached; stop and wait for the user instead of trying other actions."
	for _, tc := range []struct {
		comment string
		held    bool
		want    string
	}{
		{"use the staging bucket", false, steered},
		{"  use the staging bucket \n", false, steered},
		{"", false, ""},
		{"   ", false, ""},
		{"", true, held},
		{"ignored when held", true, held},
	} {
		if got := DeniedText(tc.comment, tc.held); got != tc.want {
			t.Errorf("DeniedText(%q, %v) = %q, want %q", tc.comment, tc.held, got, tc.want)
		}
	}
}

func TestDeniedErrorText(t *testing.T) {
	for _, tc := range []struct {
		e    DeniedError
		want string
	}{
		{DeniedError{Tool: "shell"}, "tool shell was denied by the user"},
		{DeniedError{Tool: "shell", Comment: "no"}, `Denied by the user: "no". Do not retry the same action unchanged; do what the user asked instead.`},
		{DeniedError{Tool: "shell", Held: true}, DeniedText("", true)},
		{DeniedError{Tool: "shell", Comment: "no", Cancelled: true}, "tool shell was denied by the user and the run was stopped"},
		{DeniedError{Tool: "shell", Expired: true}, "tool shell was not run: nobody approved it before the approval timeout"},
	} {
		if got := tc.e.Error(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestExtractCommandFindsNestedCommands(t *testing.T) {
	for args, want := range map[string]string{
		`{"command":"npm test"}`:                          "npm test",
		`{"input":{"cmd":"ls -l"}}`:                       "ls -l",
		`{"a":[{"script":"echo hi"}]}`:                    "echo hi",
		`{"raw":{"argv":["git","status","-s"]}}`:          "git status -s",
		`{"command":"top","nested":{"command":"nested"}}`: "top",
		`{"path":"x"}`:                                    "",
		`not json`:                                        "",
	} {
		if got := ExtractCommand(json.RawMessage(args)); got != want {
			t.Errorf("ExtractCommand(%s) = %q, want %q", args, got, want)
		}
	}
}

func TestGenericDescribeForToolsWithoutDescribe(t *testing.T) {
	at := DescribeCall(Tool{Name: "deploy"}, json.RawMessage(`{"env":"prod","opts":{"cmd":"make ship"}}`), "")
	if at.Title != "Run tool deploy" || at.Description != "Run tool deploy with env=prod" || at.Command != "make ship" {
		t.Errorf("generic text = %+v", at)
	}
	at = DescribeCall(Tool{Name: "deploy"}, json.RawMessage(`{}`), "needs approval because x\n\nsecond paragraph")
	if at.Description != "needs approval because x" {
		t.Errorf("description = %q", at.Description)
	}
}

func TestSandboxAndGHDescriptions(t *testing.T) {
	st := &sandboxTools{name: "docker-dev"}
	at := st.describeShell(json.RawMessage(`{"command":"npm test"}`), "")
	if at.Description != "Run `npm test` in sandbox docker-dev" || at.Command != "npm test" {
		t.Errorf("shell = %+v", at)
	}
	at = st.describeWrite(json.RawMessage(`{"path":"a/b.txt","content":"hello"}`), "")
	if at.Description != "Write a/b.txt (5 bytes) in sandbox docker-dev" {
		t.Errorf("write = %+v", at)
	}
	for _, tc := range []struct{ args, want string }{
		{`{"method":"POST","path":"/repos/o/r/pulls/123/reviews","params":{"event":"APPROVE"}}`, "Post a review on o/r#123 (approve)."},
		{`{"method":"POST","path":"/repos/o/r/issues/7/comments","params":{"body":"hi"}}`, "Comment on o/r#7."},
		{`{"method":"PUT","path":"/repos/o/r/pulls/9/merge","params":{"merge_method":"squash"}}`, "Merge o/r#9 (squash)."},
		{`{"method":"DELETE","path":"/repos/o/r/git/refs/heads/x"}`, "Call the GitHub API: DELETE /repos/o/r/git/refs/heads/x."},
	} {
		if got := ghAPIDescribe(json.RawMessage(tc.args), "").Description; got != tc.want {
			t.Errorf("%s: %q, want %q", tc.args, got, tc.want)
		}
	}
	long := strings.Repeat("echo long;", 100)
	at = DescribeCall(Tool{Name: "shell", Describe: st.describeShell}, json.RawMessage(`{"command":"`+long+`"}`), "")
	if utf8.RuneCountInString(at.Description) > MaxApprovalDescription || strings.Contains(at.Description, long) || at.Command != long {
		t.Errorf("long command: description %d runes, command %d runes", utf8.RuneCountInString(at.Description), utf8.RuneCountInString(at.Command))
	}
}

func TestDescribeEmit(t *testing.T) {
	at := DescribeEmit("reviews", "triage", "approved", "post")(json.RawMessage(`{"verdict":"ship"}`), "")
	if at.Title != "Emit approved from reviews/triage" || !strings.Contains(at.Description, `port "approved"`) || !strings.Contains(at.Description, "continues to post") || !strings.Contains(at.Description, `{"verdict":"ship"}`) {
		t.Errorf("emit = %+v", at)
	}
}

// TestEveryApprovingToolDescribesItself: a tool that can need approval says what it would do (a Description, and for a
// shell-like tool the command), so no approval card is generic or empty.
func TestEveryApprovingToolDescribesItself(t *testing.T) {
	regs := map[string]*Registry{}
	h, _ := newTestHub(t, &fakeHost{}, t.TempDir())
	regs["hub"] = h.base

	st := &sandboxTools{name: "docker-dev", sess: localSession(t)}
	var err error
	if st.policy, err = sandbox.NewPolicy(spec.Sandbox{Approval: spec.ApprovalAlways}); err != nil {
		t.Fatal(err)
	}
	sr := NewRegistry()
	if err := sr.Register(st.tools()...); err != nil {
		t.Fatal(err)
	}
	regs["sandbox"] = sr

	custom := spec.Tool{ID: "deploy-it", Lang: "bash", Script: "./deploy.sh", Approve: spec.ToolApprove{Mode: spec.ToolApproveAlways}}
	cr := NewRegistry()
	if err := cr.Register(customTool(CustomOptions{}, RunSpec{Sandbox: "docker-dev"}, "deploy-it", CustomTool{Tool: &custom})); err != nil {
		t.Fatal(err)
	}
	regs["custom"] = cr

	o := newOperator(t, &fakeOperatorHost{})
	regs["operator"] = o.reg

	// An example call that needs approval, by tool name. shellLike tools must also give a command.
	approving := map[string]string{
		"gh_api":      `{"method":"POST","path":"/repos/o/r/issues/1/comments","params":{"body":"hi"}}`,
		"fs_write":    `{"path":"a.txt","content":"x"}`,
		"shell":       `{"command":"npm test"}`,
		"deploy_it":   `{"target":"prod"}`,
		"flow_run":    `{"id":"triage"}`,
		"sensor_poll": `{"id":"inbox"}`,
	}
	shellLike := map[string]bool{"gh_api": true, "shell": true, "deploy_it": true}
	// Operator tools that ask through the host themselves (their approval text is set where they call it).
	hostGated := map[string]bool{"request_approval": true, "send_note": true, "flow_create": true, "sensor_create": true}

	seen := map[string]bool{}
	for kind, reg := range regs {
		for _, name := range reg.Names() {
			tl, _ := reg.Get(name)
			if tl.Approval == nil && tl.Policy == nil {
				continue
			}
			args, ok := approving[name]
			if !ok {
				if kind == "operator" && (!o.NeedsApproval(name) || hostGated[name]) {
					continue // a read tool, or one that asks inside its own run
				}
				t.Errorf("%s tool %s can need approval: add an approving example to this test", kind, name)
				continue
			}
			seen[name] = true
			v, why := approval(tl, json.RawMessage(args))
			if v != VerdictApprove {
				t.Errorf("%s: example call does not need approval (verdict %d)", name, v)
				continue
			}
			if kind != "operator" && tl.Describe == nil {
				t.Errorf("%s: no Describe", name)
			}
			at := DescribeCall(tl, json.RawMessage(args), why)
			if strings.TrimSpace(at.Description) == "" || utf8.RuneCountInString(at.Description) > MaxApprovalDescription {
				t.Errorf("%s: description = %q", name, at.Description)
			}
			if shellLike[name] && strings.TrimSpace(at.Command) == "" {
				t.Errorf("%s: no command", name)
			}
		}
	}
	for name := range approving {
		if !seen[name] {
			t.Errorf("example for %s was never used: the tool is not registered", name)
		}
	}
}

func TestOperatorGatedCallsCarryAHalDescription(t *testing.T) {
	host := &fakeOperatorHost{decision: Decision{Allowed: true}}
	o := newOperator(t, host)
	var sent []sentRequest
	if _, err := o.Call(context.Background(), envOf(recordingAPI(200, `{}`, &sent), "a"), "flow_run", json.RawMessage(`{"id":"triage"}`)); err != nil {
		t.Fatal(err)
	}
	if got := host.asked[0].Description; got != `Run the flow "triage" now: poll its sensors and start runs of it.` {
		t.Errorf("description = %q", got)
	}
}

func TestHubRequestApprovalNeedsADescriptionThenSucceeds(t *testing.T) {
	host := &fakeHost{askResult: AskResult{Status: "approved"}}
	h, _ := newTestHub(t, host, t.TempDir())
	s := openSession(t, h)
	ctx := context.Background()

	for _, args := range []string{`{"title":"Deploy?"}`, `{"title":"Deploy?","description":"   "}`} {
		_, err := s.Call(ctx, "request_approval", json.RawMessage(args))
		if err == nil || err.Error() != "description is required: say in one or two sentences what you want to do and why" {
			t.Fatalf("%s: err = %v", args, err)
		}
	}
	if len(host.asks) != 0 {
		t.Fatalf("a call without description reached the host: %+v", host.asks)
	}

	res, err := s.Call(ctx, "request_approval", json.RawMessage(`{"title":"Deploy?","description":"Deploy build 42 to prod because CI is green.","body":"b","priority":"high","timeout_seconds":600,"wait_seconds":5,"editable":[{"key":"note","value":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(host.asks) != 1 {
		t.Fatalf("asks = %+v", host.asks)
	}
	a := host.asks[0]
	if a.RunID != "run1" || a.NodeRunID != "nr1" || a.Title != "Deploy?" || a.Description != "Deploy build 42 to prod because CI is green." || a.BodyMD != "b" || a.Priority != "high" ||
		a.Timeout != 600*time.Second || a.Wait != 5*time.Second || len(a.Editable) != 1 || a.Editable[0].Key != "note" || string(a.Editable[0].Value) != `"hi"` {
		t.Errorf("ask = %+v", a)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(res.Text), &got)
	if got["approval_id"] != "A1" || got["decision"] != "approved" || got["status"] != "approved" {
		t.Errorf("result = %s", res.Text)
	}
}

func TestHubRequestApprovalResultMapping(t *testing.T) {
	steered := `Denied by the user: "use staging". Do not retry the same action unchanged; do what the user asked instead.`
	for _, tc := range []struct {
		name   string
		res    AskResult
		want   map[string]any
		absent []string
	}{
		{"denied with steering", AskResult{Status: "denied", Comment: "use staging"}, map[string]any{"decision": "denied", "comment": "use staging", "message": steered}, []string{"values"}},
		{"denied without comment", AskResult{Status: "denied"}, map[string]any{"decision": "denied", "comment": ""}, []string{"message"}},
		{"held", AskResult{Status: "denied", Held: true}, map[string]any{"decision": "denied", "message": DeniedText("", true)}, nil},
		{"expired", AskResult{Status: "expired"}, map[string]any{"decision": "expired"}, []string{"message", "values"}},
		{"pending", AskResult{Status: "pending"}, map[string]any{"decision": "pending"}, []string{"comment", "message"}},
		{"approved with edits", AskResult{
			Status: "approved", Comment: "fine",
			Values: map[string]json.RawMessage{"reply": json.RawMessage(`"Hello"`), "n": json.RawMessage(`2`)},
			Edits:  map[string]AskEdit{"reply": {Before: json.RawMessage(`"Hi"`), After: json.RawMessage(`"Hello"`)}},
		}, map[string]any{"decision": "approved", "comment": "fine", "edited": true}, []string{"message"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeHost{askResult: tc.res}
			h, _ := newTestHub(t, host, t.TempDir())
			s := openSession(t, h)
			res, err := s.Call(context.Background(), "request_approval", json.RawMessage(`{"title":"t","description":"d"}`))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %v, want %v (result %s)", k, got[k], v, res.Text)
				}
			}
			for _, k := range tc.absent {
				if _, ok := got[k]; ok {
					t.Errorf("result has %s: %s", k, res.Text)
				}
			}
			if tc.name == "approved with edits" {
				vals, _ := got["values"].(map[string]any)
				edits, _ := got["edits"].(map[string]any)
				reply, _ := edits["reply"].(map[string]any)
				if vals["reply"] != "Hello" || vals["n"] != float64(2) || reply["before"] != "Hi" || reply["after"] != "Hello" {
					t.Errorf("values/edits = %s", res.Text)
				}
			}
		})
	}
}

func TestHubRequestApprovalIsAllowedInReadOnlyRunsAndNeedsAnAgentRun(t *testing.T) {
	host := &fakeHost{askResult: AskResult{Status: "approved"}}
	h, _ := newTestHub(t, host, t.TempDir())
	ctx := context.Background()
	ro, err := h.Open(ctx, RunSpec{RunID: "r", ReadOnly: true, Info: func() RunInfo { return RunInfo{AgentRunID: "r", DryRun: true} }})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Call(ctx, "request_approval", json.RawMessage(`{"title":"t","description":"d"}`)); err != nil {
		t.Fatalf("read-only run: %v", err)
	}
	host.askErr = errors.New("boom")
	if _, err := ro.Call(ctx, "request_approval", json.RawMessage(`{"title":"t","description":"d"}`)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("host error = %v", err)
	}
	none, err := h.Open(ctx, RunSpec{RunID: "x", Info: func() RunInfo { return RunInfo{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer none.Close()
	if _, err := none.Call(ctx, "request_approval", json.RawMessage(`{"title":"t","description":"d"}`)); err == nil {
		t.Error("request_approval without an agent run was accepted")
	}
}

func TestOperatorRequestApprovalNeedsADescriptionThenSucceeds(t *testing.T) {
	host := &fakeOperatorHost{questionID: "A1"}
	o := newOperator(t, host)
	var sent []sentRequest
	env := envOf(recordingAPI(200, approvalView("denied", "use staging", `[]`, `null`), &sent), "alice")

	_, err := o.Call(context.Background(), env, "request_approval", json.RawMessage(`{"title":"Deploy?"}`))
	if err == nil || !strings.HasSuffix(err.Error(), ErrDescriptionRequired.Error()) {
		t.Fatalf("err = %v", err)
	}
	if len(host.questions) != 0 {
		t.Fatalf("question reached the host: %+v", host.questions)
	}
	res, err := o.Call(context.Background(), env, "request_approval", json.RawMessage(`{"title":"Deploy?","description":"Deploy build 42."}`))
	if err != nil {
		t.Fatal(err)
	}
	if host.questions[0].Description != "Deploy build 42." || !strings.Contains(res.Text, `"message":"Denied by the user: \"use staging\".`) {
		t.Errorf("question = %+v, result = %s", host.questions[0], res.Text)
	}
	var props map[string]any
	for _, ti := range o.Tools() {
		if ti.Name == "request_approval" {
			props, _ = ti.InputSchema["properties"].(map[string]any)
			req, _ := ti.InputSchema["required"].([]string)
			if len(req) != 2 || req[1] != "description" {
				t.Errorf("required = %v", req)
			}
		}
	}
	if _, ok := props["description"]; !ok {
		t.Error("schema lacks description")
	}
}
