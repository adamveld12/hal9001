package tools

import (
	"context"
	"strings"
	"testing"

	"hal9001/internal/sandbox"
	"hal9001/internal/spec"
)

func localSession(t *testing.T) sandbox.Session {
	t.Helper()
	cfg := spec.DefaultSandbox(spec.SandboxLocal)
	cfg.Enabled, cfg.Root = true, t.TempDir()
	b, err := sandbox.New(spec.SandboxLocal, cfg, sandbox.Env{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Prepare(context.Background(), sandbox.RunSpec{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunInSandbox(t *testing.T) {
	ctx := context.Background()
	s := localSession(t)
	res, err := RunInSandbox(ctx, s, "bash", `echo "got $TOKEN $(cat)"; echo err >&2; exit 3`, []byte("in"), map[string]string{"TOKEN": "s3cret-value"}, 0)
	if err != nil || res.ExitCode != 3 || strings.TrimSpace(res.Stdout) != "got "+sandbox.Redacted+" in" || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if ents, _ := s.List(ctx, ".hal"); len(ents) != 0 {
		t.Errorf("script file left behind: %v", ents)
	}
	if res, _ := RunInSandbox(ctx, s, "exec", "echo direct", nil, nil, 0); strings.TrimSpace(res.Stdout) != "direct" {
		t.Errorf("exec: %+v", res)
	}
	if _, err := RunInSandbox(ctx, s, "perl", "1", nil, nil, 0); err == nil {
		t.Error("unknown language accepted")
	}
	if _, err := RunInSandbox(ctx, s, "bash", "true", nil, map[string]string{"A B": "x"}, 0); err == nil {
		t.Error("bad env name accepted")
	}
}

func TestUnifiedDiff(t *testing.T) {
	got := UnifiedDiff("a.txt", "one\ntwo\nthree\n", "one\n2\nthree\nfour\n")
	for _, want := range []string{"--- a/a.txt", "+++ b/a.txt", "@@ -1,3 +1,4 @@", " one", "-two", "+2", " three", "+four"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff lacks %q:\n%s", want, got)
		}
	}
	if d := UnifiedDiff("n", "", "x\n"); !strings.Contains(d, "+x") || !strings.Contains(d, "@@ -1,0 +1,1 @@") {
		t.Errorf("new file diff:\n%s", d)
	}
	if UnifiedDiff("n", "same", "same") != "(no changes)" {
		t.Error("identical texts")
	}
}
