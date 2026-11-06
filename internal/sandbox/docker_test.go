package sandbox

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hal9001/internal/spec"
)

// TestDockerSmoke runs against a real Docker Engine and skips when there is none (or in -short mode).
func TestDockerSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a Docker Engine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg := spec.DefaultSandbox(spec.SandboxDocker)
	cfg.Enabled, cfg.Images, cfg.Network = true, []string{"alpine:3"}, false
	env := Env{Vars: map[string]string{"HELLO": "from-env", "SEKRET": "very-secret-1"}, Secrets: []string{"very-secret-1"}}
	if res := Check(ctx, spec.SandboxDocker, cfg, env, ""); !res.OK {
		t.Skipf("no Docker Engine: %s", res.Message)
	}
	b, err := New(spec.SandboxDocker, cfg, env, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Prepare(ctx, RunSpec{RunID: "smoke-" + shortHash(t.Name()+time.Now().String()), Input: []byte("{}")})
	if err != nil {
		t.Skipf("cannot start a container (image pull offline?): %v", err)
	}
	defer func() { _ = s.Close(ctx, true) }()
	res, err := s.Exec(ctx, `echo "$HELLO $SEKRET"; pwd; cat; exit 5`, []byte("piped"), 0)
	if err != nil || res.ExitCode != 5 || !strings.Contains(res.Stdout, "from-env "+Redacted) || !strings.HasSuffix(res.Stdout, "piped") {
		t.Fatalf("exec = %+v, %v", res, err)
	}
	if res, _ := s.Exec(ctx, "wget -q -T 3 -O- http://example.com >/dev/null 2>&1; echo $?", nil, 0); strings.TrimSpace(res.Stdout) == "0" {
		t.Error("network is disabled but the container reached the internet")
	}
	data := []byte("bin\x00ary\n\"quoted\"")
	if err := s.WriteFile(ctx, "a/b.bin", data); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(ctx, "a/b.bin"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read = %q, %v", got, err)
	}
	if _, err := s.ReadFile(ctx, "../../etc/passwd"); !errors.Is(err, ErrEscape) {
		t.Fatalf("escape = %v", err)
	}
	if res, _ := s.Exec(ctx, "sleep 30", nil, time.Second); !res.TimedOut {
		t.Errorf("timeout not reported: %+v", res)
	}
}
