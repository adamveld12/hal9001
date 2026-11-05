package auth_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"hal9001/internal/auth"
)

// parseSpec turns "required:F sufficient:S" into stack entries and scripted
// modules m0, m1, ... that return S(uccess), F(ail) or I(gnore). Module mN's
// principal is "pN".
func parseSpec(t *testing.T, spec string) (*auth.Stack, *[]string) {
	t.Helper()
	reg := auth.NewRegistry()
	var entries []auth.StackEntry
	called := &[]string{}
	for i, f := range strings.Fields(spec) {
		ctl, res, _ := strings.Cut(f, ":")
		name := fmt.Sprintf("m%d", i)
		var r auth.Result
		switch res {
		case "S":
			r = auth.Success(auth.Principal{Name: fmt.Sprintf("p%d", i)})
		case "F":
			r = auth.Fail("nope")
		case "I":
			r = auth.Ignore("n/a")
		default:
			t.Fatalf("bad spec %q", f)
		}
		if err := reg.Register(scriptedModule{name: name, fn: func(*auth.AuthRequest) auth.Result {
			*called = append(*called, name)
			return r
		}}); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, auth.StackEntry{Module: name, Control: auth.Control(ctl)})
	}
	return &auth.Stack{Name: "t", Entries: entries, Registry: reg}, called
}

func TestStackControlSemantics(t *testing.T) {
	tests := []struct {
		name          string
		spec          string
		want          auth.Outcome
		wantRan       int    // modules actually called
		wantPrincipal string // when success
	}{
		{"sufficient success", "sufficient:S", auth.OutcomeSuccess, 1, "p0"},
		{"sufficient failure", "sufficient:F", auth.OutcomeFail, 1, ""},
		{"only ignores: no module succeeded", "sufficient:I", auth.OutcomeFail, 1, ""},
		{"empty stack", "", auth.OutcomeFail, 0, ""},
		{"required success", "required:S", auth.OutcomeSuccess, 1, "p0"},
		{"required failure", "required:F", auth.OutcomeFail, 1, ""},
		{"required ignore", "required:I", auth.OutcomeFail, 1, ""},
		{"requisite success", "requisite:S", auth.OutcomeSuccess, 1, "p0"},

		{"requisite failure stops evaluation", "requisite:F sufficient:S", auth.OutcomeFail, 1, ""},
		{"requisite success continues", "requisite:S sufficient:S", auth.OutcomeSuccess, 2, "p0"},
		{"requisite ignore continues", "requisite:I sufficient:S", auth.OutcomeSuccess, 2, "p1"},
		{"required failure then sufficient success still fails", "required:F sufficient:S", auth.OutcomeFail, 2, ""},
		{"required success then requisite failure fails", "required:S requisite:F", auth.OutcomeFail, 2, ""},
		{"required failure then requisite success fails", "required:F requisite:S", auth.OutcomeFail, 2, ""},
		{"sufficient success short-circuits later required failure", "sufficient:S required:F", auth.OutcomeSuccess, 1, "p0"},
		{"failed sufficient is ignored", "sufficient:F sufficient:S", auth.OutcomeSuccess, 2, "p1"},
		{"required success then sufficient success", "required:S sufficient:S", auth.OutcomeSuccess, 2, "p0"},
		{"all required succeed", "required:S required:S", auth.OutcomeSuccess, 2, "p0"},
		{"one of two required fails", "required:S required:F", auth.OutcomeFail, 2, ""},
		{"ignored required does not fail the stack", "required:I sufficient:S", auth.OutcomeSuccess, 2, "p1"},

		{"optional alone succeeds", "optional:S", auth.OutcomeSuccess, 1, "p0"},
		{"optional alone fails", "optional:F", auth.OutcomeFail, 1, ""},
		{"optional alone ignored", "optional:I", auth.OutcomeFail, 1, ""},
		{"optional success does not rescue a failed sufficient", "optional:S sufficient:F", auth.OutcomeFail, 2, ""},
		{"optional failure does not break a required success", "required:S optional:F", auth.OutcomeSuccess, 2, "p0"},
		{"optional success does not rescue a required failure", "optional:S required:F", auth.OutcomeFail, 2, ""},
		{"optional success after required success is not the principal", "optional:S required:S", auth.OutcomeSuccess, 2, "p1"},
		{"two optionals, one succeeds, nothing else ran", "optional:F optional:S", auth.OutcomeSuccess, 2, "p1"},
		{"optional counts when other modules only ignored", "sufficient:I optional:S", auth.OutcomeSuccess, 2, "p1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, called := parseSpec(t, tt.spec)
			ev := st.Evaluate(context.Background(), &auth.AuthRequest{})
			if ev.Outcome != tt.want {
				t.Errorf("outcome = %s (%s), want %s", ev.Outcome, ev.Reason, tt.want)
			}
			if len(*called) != tt.wantRan {
				t.Errorf("ran %v, want %d modules", *called, tt.wantRan)
			}
			if len(ev.Trace) != tt.wantRan {
				t.Errorf("trace has %d entries, want %d", len(ev.Trace), tt.wantRan)
			}
			if ev.Principal.Name != tt.wantPrincipal {
				t.Errorf("principal = %q, want %q", ev.Principal.Name, tt.wantPrincipal)
			}
			if tt.want == auth.OutcomeSuccess && len(ev.Modules) == 0 {
				t.Error("successful evaluation lists no succeeded modules")
			}
		})
	}
}

func TestStackTraceRecordsEveryModuleResult(t *testing.T) {
	st, _ := parseSpec(t, "requisite:I required:F sufficient:S")
	ev := st.Evaluate(context.Background(), &auth.AuthRequest{})
	want := []auth.ModuleTrace{
		{Module: "m0", Control: auth.Requisite, Result: auth.KindIgnore, Reason: "n/a"},
		{Module: "m1", Control: auth.Required, Result: auth.KindFail, Reason: "nope"},
		{Module: "m2", Control: auth.Sufficient, Result: auth.KindSuccess},
	}
	if !slices.Equal(ev.Trace, want) {
		t.Errorf("trace = %+v\nwant   %+v", ev.Trace, want)
	}
	if ev.Outcome != auth.OutcomeFail || !strings.Contains(ev.Reason, "m1") {
		t.Errorf("outcome %s reason %q: want a failure naming the failed required module m1", ev.Outcome, ev.Reason)
	}
}

func TestStackChallengePausesAndResumes(t *testing.T) {
	reg := auth.NewRegistry()
	var sawResponse string
	_ = reg.Register(fixed("pw", auth.Success(auth.Principal{Name: "owner"})))
	_ = reg.Register(scriptedModule{name: "otp", fn: func(r *auth.AuthRequest) auth.Result {
		if r.Step == nil {
			return auth.Challenge(auth.Step{Kind: "otp", State: []byte(`"secret-state"`)})
		}
		sawResponse = string(r.Step.Response) + "|" + string(r.Step.State)
		if string(r.Step.Response) == `"123456"` {
			return auth.Success(auth.Principal{Name: "owner"})
		}
		return auth.Fail("bad code")
	}})
	_ = reg.Register(fixed("after", auth.Fail("later required fails")))
	st := &auth.Stack{Entries: []auth.StackEntry{
		{Module: "pw", Control: auth.Required},
		{Module: "otp", Control: auth.Required},
	}, Registry: reg}

	ev := st.Evaluate(context.Background(), &auth.AuthRequest{Interactive: true})
	if ev.Outcome != auth.OutcomeChallenge || ev.Pending == nil || ev.Pending.Module != "otp" {
		t.Fatalf("got %+v, want a challenge from otp", ev)
	}
	got := st.Resume(context.Background(), &auth.AuthRequest{Interactive: true}, ev.Pending, auth.StepResponse{Response: []byte(`"123456"`)})
	if got.Outcome != auth.OutcomeSuccess {
		t.Fatalf("resume outcome = %s (%s)", got.Outcome, got.Reason)
	}
	if sawResponse != `"123456"|"secret-state"` {
		t.Errorf("module saw %q: want the response and the state it issued", sawResponse)
	}
	// The earlier successful module's result survives the round trip, and the
	// full trace shows the challenge followed by the final answer.
	var trace []string
	for _, m := range got.Trace {
		trace = append(trace, m.Module+":"+string(m.Result))
	}
	if strings.Join(trace, ",") != "pw:success,otp:challenge,otp:success" {
		t.Errorf("trace = %v", trace)
	}

	bad := st.Resume(context.Background(), &auth.AuthRequest{Interactive: true}, ev.Pending, auth.StepResponse{Response: []byte(`"000000"`)})
	if bad.Outcome != auth.OutcomeFail {
		t.Errorf("wrong code outcome = %s, want fail", bad.Outcome)
	}
}

func TestStackChallengeIsIgnoredOutsideInteractiveLogin(t *testing.T) {
	reg := auth.NewRegistry()
	_ = reg.Register(fixed("otp", auth.Challenge(auth.Step{Kind: "otp"})))
	_ = reg.Register(fixed("local", auth.Success(auth.Principal{Name: "localhost"})))
	st := &auth.Stack{Entries: []auth.StackEntry{
		{Module: "otp", Control: auth.Sufficient},
		{Module: "local", Control: auth.Sufficient},
	}, Registry: reg}
	ev := st.Evaluate(context.Background(), &auth.AuthRequest{Interactive: false})
	if ev.Outcome != auth.OutcomeSuccess || ev.Trace[0].Result != auth.KindIgnore {
		t.Errorf("non-interactive: outcome %s, first result %s; want success with the challenge treated as ignore", ev.Outcome, ev.Trace[0].Result)
	}
}
