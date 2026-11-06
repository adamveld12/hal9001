package sandbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/spec"
)

// fakeDepot implements the parts of depot.sandbox.v1 the backend uses; commands run on the local shell.
type fakeDepot struct {
	mu       sync.Mutex
	home     string
	created  int
	stopped  []string
	gets     int
	authSeen []string
	orgSeen  []string
}

func (f *fakeDepot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
	f.orgSeen = append(f.orgSeen, r.Header.Get("x-depot-org"))
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok-depot" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"bad token"}`))
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body, _ := io.ReadAll(r.Body)
	switch method {
	case "CreateSandbox":
		f.mu.Lock()
		f.created++
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"sandbox":{"sandboxId":"sb-1","status":"SANDBOX_STATUS_STARTING"}}`))
	case "GetSandbox":
		f.mu.Lock()
		f.gets++
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"sandbox":{"sandboxId":"sb-1","status":4}}`))
	case "ListSandboxes":
		_, _ = w.Write([]byte(`{"sandboxes":[]}`))
	case "StopSandbox":
		var req struct{ Sandbox struct{ ID string } }
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.stopped = append(f.stopped, req.Sandbox.ID)
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	case "RunCommand":
		f.runCommand(w, body[5:])
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func envelope(flags byte, payload []byte) []byte {
	b := make([]byte, 5, 5+len(payload))
	b[0] = flags
	binary.BigEndian.PutUint32(b[1:], uint32(len(payload))) //nolint:gosec // test
	return append(b, payload...)
}

func (f *fakeDepot) runCommand(w http.ResponseWriter, body []byte) {
	var req struct {
		Cmd  string
		Args []string
		Env  map[string]string
	}
	_ = json.Unmarshal(body, &req)
	c := exec.Command(req.Cmd, req.Args...)
	c.Env = append(os.Environ(), "HOME="+f.home)
	for k, v := range req.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	code := 0
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	w.Header().Set("Content-Type", "application/connect+json")
	emit := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write(envelope(0, b))
	}
	emit(map[string]any{"started": map[string]any{"cmdId": "c1"}})
	emit(map[string]any{"stdout": map[string]any{"data": out.Bytes()}})
	emit(map[string]any{"stderr": map[string]any{"data": errb.Bytes()}})
	emit(map[string]any{"finished": map[string]any{"exitCode": code}})
	_, _ = w.Write(envelope(2, []byte(`{}`)))
}

func depotBackendFor(t *testing.T, srv *httptest.Server, lifecycle, token string) Backend {
	t.Helper()
	cfg := spec.DefaultSandbox(spec.SandboxDepot)
	cfg.Enabled, cfg.Lifecycle, cfg.Endpoint, cfg.OrgID = true, lifecycle, srv.URL, "org-9"
	cfg.Architecture, cfg.Resources.CPUs = "arm64", 4
	cfg.CISecrets = map[string]string{"DB_URL": "PROD_DB"}
	b, err := New(spec.SandboxDepot, cfg, Env{Vars: map[string]string{"DEPOT_TOKEN": token, "APP_MODE": "test"}, Secrets: []string{token}}, "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDepotPerRunSandbox(t *testing.T) {
	ctx := context.Background()
	fake := &fakeDepot{home: t.TempDir()}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	b := depotBackendFor(t, srv, spec.LifecyclePerRun, "tok-depot")
	s, err := b.Prepare(ctx, RunSpec{RunID: "r1", Input: []byte(`{"k":"v"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if fake.created != 1 || fake.gets < 1 {
		t.Fatalf("created=%d gets=%d: the backend must wait for the sandbox to run", fake.created, fake.gets)
	}
	if fake.orgSeen[0] != "org-9" {
		t.Errorf("x-depot-org = %q", fake.orgSeen[0])
	}
	res, err := s.Exec(ctx, `echo "mode=$APP_MODE tok=[$DEPOT_TOKEN]"; echo err >&2; exit 4`, nil, 0)
	if err != nil || res.ExitCode != 4 || strings.TrimSpace(res.Stdout) != "mode=test tok=[]" || strings.TrimSpace(res.Stderr) != "err" {
		t.Fatalf("exec = %+v, %v (the Depot token must not reach commands)", res, err)
	}
	if got, err := s.ReadFile(ctx, "input.json"); err != nil || string(got) != `{"k":"v"}` {
		t.Fatalf("input.json = %q, %v", got, err)
	}
	payload := []byte("a\x00b\nlong " + strings.Repeat("x", 500))
	if err := s.WriteFile(ctx, "d/e.bin", payload); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile(ctx, "d/e.bin"); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read back %d bytes, %v", len(got), err)
	}
	if _, err := s.ReadFile(ctx, "../outside"); !errors.Is(err, ErrEscape) {
		t.Fatalf("escape = %v", err)
	}
	if err := s.Close(ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(fake.stopped) != 1 || fake.stopped[0] != "sb-1" {
		t.Fatalf("stopped = %v", fake.stopped)
	}
}

func TestDepotCheckAndBadToken(t *testing.T) {
	fake := &fakeDepot{home: t.TempDir()}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := spec.DefaultSandbox(spec.SandboxDepot)
	cfg.Enabled, cfg.Endpoint = true, srv.URL
	if res := Check(context.Background(), spec.SandboxDepot, cfg, Env{Vars: map[string]string{"DEPOT_TOKEN": "tok-depot"}}, ""); !res.OK {
		t.Fatalf("check = %+v", res)
	}
	if res := Check(context.Background(), spec.SandboxDepot, cfg, Env{Vars: map[string]string{"DEPOT_TOKEN": "nope"}}, ""); res.OK || !strings.Contains(res.Message, "bad token") {
		t.Fatalf("check with a bad token = %+v", res)
	}
	if res := Check(context.Background(), spec.SandboxDepot, cfg, Env{}, ""); res.OK || !strings.Contains(res.Message, "DEPOT_TOKEN") {
		t.Fatalf("check without a token = %+v", res)
	}
}

func TestDepotPersistentSandboxIsKept(t *testing.T) {
	ctx := context.Background()
	fake := &fakeDepot{home: t.TempDir()}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	b := depotBackendFor(t, srv, spec.LifecyclePersistent, "tok-depot")
	for _, id := range []string{"a", "b"} {
		s, err := b.Prepare(ctx, RunSpec{RunID: id})
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close(ctx, true)
	}
	if fake.created != 1 || len(fake.stopped) != 0 {
		t.Fatalf("created=%d stopped=%v, want one kept sandbox", fake.created, fake.stopped)
	}
}
