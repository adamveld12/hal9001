package runner

import (
	"context"
	"strings"
	"testing"
)

func TestRedactorReplacesSecretsEverywhere(t *testing.T) {
	r := NewRedactor(map[string]string{
		"SVC_ONE": "value-one-1234",
		"SVC_TWO": "value-two-abcd",
		"DEBUG":   "true", // too short to be a secret
	})
	in := "auth value-one-1234 and value-two-abcd, debug=true"
	want := "auth «SVC_ONE» and «SVC_TWO», debug=true"
	if got := r.String(in); got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	if got := r.Argv([]string{"curl", "-H", "Authorization: Bearer value-one-1234"}); got[2] != "Authorization: Bearer «SVC_ONE»" {
		t.Errorf("Argv = %q", got)
	}
}

func TestRedactorLongestValueWins(t *testing.T) {
	r := NewRedactor(map[string]string{
		"SHORT": "shared-prefix",
		"LONG":  "shared-prefix-extended",
	})
	if got := r.String("a shared-prefix-extended b shared-prefix c"); got != "a «LONG» b «SHORT» c" {
		t.Errorf("got %q", got)
	}
}

func TestRedactorSharedValueIsDeterministic(t *testing.T) {
	r := NewRedactor(map[string]string{"ZED": "same-value-1", "ALPHA": "same-value-1"})
	for range 20 {
		if got := r.String("same-value-1"); got != "«ALPHA»" {
			t.Fatalf("got %q", got)
		}
	}
}

func TestRedactorCoversJSONEscapedForm(t *testing.T) {
	r := NewRedactor(map[string]string{"SVC_FOUR": `pa"ss\word-123`})
	out := r.String(`{"v":"pa\"ss\\word-123"} raw: pa"ss\word-123`)
	if strings.Contains(out, "word-123") {
		t.Errorf("secret leaked: %q", out)
	}
}

func TestRedactorNilAndEmpty(t *testing.T) {
	var nilR *Redactor
	if nilR.String("abc") != "abc" || string(nilR.Bytes([]byte("abc"))) != "abc" {
		t.Error("nil redactor must pass through")
	}
	if got := NewRedactor(map[string]string{"A": "short"}).String("short"); got != "short" {
		t.Errorf("short values must not be redacted, got %q", got)
	}
}

func TestRedactResultOfRealRun(t *testing.T) {
	skipIfMissing(t, "bash")
	const secret = "value-three-42"
	res, err := Run(context.Background(), Spec{
		Lang:   LangBash,
		Script: `echo "out $SVC_THREE"; echo "err $SVC_THREE" >&2`,
		Env:    map[string]string{"SVC_THREE": secret},
		Cwd:    t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	red := NewRedactor(map[string]string{"SVC_THREE": secret}).Result(res)
	if string(red.Stdout) != "out «SVC_THREE»\n" || string(red.Stderr) != "err «SVC_THREE»\n" {
		t.Errorf("stdout=%q stderr=%q", red.Stdout, red.Stderr)
	}
	// The original is untouched: callers parse raw output, then redact before storing.
	if !strings.Contains(string(res.Stdout), secret) {
		t.Error("Result() must not mutate the original")
	}
}
