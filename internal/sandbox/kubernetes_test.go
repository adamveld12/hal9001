package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hal9001/internal/spec"
)

const fakeKubectl = `#!/bin/sh
echo "KUBECONFIG=$KUBECONFIG $*" >> "$FAKE_DIR/log"
while [ $# -gt 0 ]; do case "$1" in --context|-n) shift 2;; *) break;; esac; done
cmd=$1; shift
case $cmd in
  get) if [ -f "$FAKE_DIR/pod-$2" ]; then printf Running; else echo 'Error from server (NotFound): pods "x" not found' >&2; exit 1; fi;;
  run) touch "$FAKE_DIR/pod-$1";;
  wait) ;;
  delete) rm -f "$FAKE_DIR/pod-$2";;
  version) echo '{"clientVersion":{"gitVersion":"v1.30.0"},"serverVersion":{"gitVersion":"v1.29.1"}}';;
  auth) echo yes;;
  exec) [ "$1" = -i ] && shift; shift; shift; exec "$@";;
  *) echo "unexpected $cmd" >&2; exit 9;;
esac
`

func fakeKube(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return dir
}

func kubeCfg(lifecycle string) spec.Sandbox {
	cfg := spec.DefaultSandbox(spec.SandboxKubernetes)
	cfg.Enabled, cfg.Lifecycle = true, lifecycle
	cfg.Kubeconfig, cfg.Context, cfg.Namespace = "/kc/config", "ctx1", "hal"
	cfg.Images = []string{"alpine:3", "ubuntu:24.04"}
	cfg.Resources.MemoryMB = 512
	cfg.ServiceAccount = "runner"
	return cfg
}

func TestKubernetesPerRunPodLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := fakeKube(t)
	b, err := New(spec.SandboxKubernetes, kubeCfg(spec.LifecyclePerRun), Env{Vars: map[string]string{"FOO": "bar"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if res := Check(ctx, spec.SandboxKubernetes, kubeCfg(spec.LifecyclePerRun), Env{}, ""); !res.OK || !strings.Contains(res.Message, "v1.29.1") {
		t.Fatalf("check = %+v", res)
	}
	s, err := b.Prepare(ctx, RunSpec{RunID: "Run-One", Image: "ubuntu:24.04"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pod-hal-run-one")); err != nil {
		t.Fatal("pod was not created under the sanitized run name")
	}
	res, err := s.Exec(ctx, `echo "foo=$FOO"`, nil, 0)
	if err != nil || strings.TrimSpace(res.Stdout) != "foo=bar" {
		t.Fatalf("exec = %+v, %v", res, err)
	}
	if err := s.WriteFile(ctx, "x/y.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(ctx, "x/y.txt"); err != nil || string(got) != "data" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if _, err := s.ReadFile(ctx, "../../etc/passwd"); err == nil {
		t.Fatal("escape allowed")
	}
	if err := s.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pod-hal-run-one")); err == nil {
		t.Error("pod kept after a successful run")
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	for _, want := range []string{"KUBECONFIG=/kc/config --context ctx1 -n hal run hal-run-one --image=ubuntu:24.04", "serviceAccountName", "delete pod hal-run-one"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("kubectl log lacks %q:\n%s", want, log)
		}
	}
	if _, err := b.Prepare(ctx, RunSpec{RunID: "r2", Image: "debian"}); err == nil {
		t.Error("image outside the configured list accepted")
	}

	s2, _ := b.Prepare(ctx, RunSpec{RunID: "failing"})
	_ = s2.Close(ctx, false)
	if _, err := os.Stat(filepath.Join(dir, "pod-hal-failing")); err != nil {
		t.Error("pod of a failed run was deleted")
	}
}

func TestKubernetesPersistentPodIsReused(t *testing.T) {
	ctx := context.Background()
	dir := fakeKube(t)
	b, _ := New(spec.SandboxKubernetes, kubeCfg(spec.LifecyclePersistent), Env{}, "")
	s1, err := b.Prepare(ctx, RunSpec{RunID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	s2, err := b.Prepare(ctx, RunSpec{RunID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if n := strings.Count(string(log), " run hal-persistent-"); n != 1 {
		t.Fatalf("pod created %d times, want once:\n%s", n, log)
	}
	_ = s1.Close(ctx, true)
	_ = s2.Close(ctx, true)
	if matches, _ := filepath.Glob(filepath.Join(dir, "pod-hal-persistent-*")); len(matches) != 1 {
		t.Fatal("persistent pod was deleted at the end of a run")
	}
}
