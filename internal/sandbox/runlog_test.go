package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"hal9001/internal/spec"
)

type collectLog struct {
	mu      sync.Mutex
	entries []LogEntry
	updates []SessionUpdate
}

func (c *collectLog) Add(e LogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
}

func (c *collectLog) Update(u SessionUpdate) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, u)
}

func (c *collectLog) find(kind, titlePart string) *LogEntry {
	for i := range c.entries {
		if c.entries[i].Kind == kind && strings.Contains(c.entries[i].Title, titlePart) {
			return &c.entries[i]
		}
	}
	return nil
}

// dump is everything the log holds, for secret scans.
func (c *collectLog) dump() string { return fmt.Sprintf("%+v %+v", c.entries, c.updates) }

// A session opened through the Manager with a RunLog records provisioning (with env names, never values), every
// command with its exit code and output, file operations and the teardown; secrets never reach the log.
func TestRunLogRecordsLifecycleAndCommands(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "root")
	writeLocalCfg(t, home, "enabled: true\nroot: "+root+"\nlifecycle: per_run\n")
	if _, err := spec.WriteFile(home, spec.KindSandboxEnv, "local", []byte("# hal:plain REGION\nREGION=eu-west\nAPI_TOKEN=s3cr3t-value\n"), ""); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerOptions{Home: home})
	defer m.Close()

	lg := &collectLog{}
	s, err := m.Open(ctx, "local", RunSpec{RunID: "r1", Input: []byte(`{"a":1}`), Log: lg})
	if err != nil {
		t.Fatal(err)
	}
	if lg.find(KindLifecycle, "Provisioning local sandbox") == nil || lg.find(KindLifecycle, "Sandbox ready") == nil {
		t.Fatalf("lifecycle entries missing: %s", lg.dump())
	}
	env := lg.find(KindLifecycle, "Environment variables")
	if env == nil || env.Fields["names"] != "API_TOKEN, REGION" {
		t.Fatalf("env entry = %+v", env)
	}
	if n := len(lg.updates); n < 2 || lg.updates[n-1].Status != StatusReady || lg.updates[n-1].Info.Workdir == "" {
		t.Fatalf("header updates = %+v", lg.updates)
	}

	tctx := WithCall(ctx, CallInfo{Tool: "shell", Approval: "auto: test"})
	if _, err := s.Exec(tctx, `echo out; echo err >&2; echo $API_TOKEN`, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `echo big; exit 3`, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile(ctx, "a/b.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(ctx, "missing.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// A hidden operation is not logged.
	if _, err := s.ReadFile(WithCall(ctx, CallInfo{Hidden: true}), "a/b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx, true); err != nil {
		t.Fatal(err)
	}

	ok := lg.find(KindExec, "echo out")
	if ok == nil || ok.ExitCode == nil || *ok.ExitCode != 0 || ok.Tool != "shell" || ok.Approval != "auto: test" || ok.Cwd == "" {
		t.Fatalf("exec entry = %+v", ok)
	}
	if !strings.Contains(ok.Stdout, "out") || !strings.Contains(ok.Stderr, "err") || ok.Duration <= 0 {
		t.Fatalf("exec output/duration = %+v", ok)
	}
	if strings.Contains(ok.Stdout, "s3cr3t-value") || !strings.Contains(ok.Stdout, Redacted) {
		t.Fatalf("secret in logged stdout: %q", ok.Stdout)
	}
	bad := lg.find(KindExec, "exit 3")
	if bad == nil || bad.Level != LevelWarn || bad.ExitCode == nil || *bad.ExitCode != 3 {
		t.Fatalf("failing command entry = %+v", bad)
	}
	if w := lg.find(KindFS, "write a/b.txt"); w == nil || w.Tool != "fs_write" || w.Bytes == nil || *w.Bytes != 2 {
		t.Fatalf("fs write entry = %+v", w)
	}
	if r := lg.find(KindFS, "read missing.txt"); r == nil || r.Level != LevelWarn || r.Error == "" {
		t.Fatalf("fs read entry = %+v", r)
	}
	if n := len(lg.entries); lg.find(KindFS, "read a/b.txt") != nil {
		t.Fatalf("hidden read was logged (%d entries)", n)
	}
	if td := lg.find(KindLifecycle, "Teardown"); td == nil || !strings.Contains(td.Title, "removed") {
		t.Fatalf("teardown entry = %+v", td)
	}
	if lg.updates[len(lg.updates)-1].Status != StatusClosed {
		t.Fatalf("last update = %+v", lg.updates[len(lg.updates)-1])
	}
	if strings.Contains(lg.dump(), "s3cr3t-value") {
		t.Fatalf("the secret value reached the log: %s", lg.dump())
	}
}

// Large output is capped per entry (head and tail kept, marker in between), and a command typed with a secret value
// is redacted in the logged command line.
func TestRunLogCapsOutputAndRedactsCommands(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	writeLocalCfg(t, home, "enabled: true\nroot: "+filepath.Join(t.TempDir(), "root")+"\n")
	if _, err := spec.WriteFile(home, spec.KindSandboxEnv, "local", []byte("API_TOKEN=s3cr3t-value\n"), ""); err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerOptions{Home: home})
	defer m.Close()
	lg := &collectLog{}
	s, err := m.Open(ctx, "local", RunSpec{RunID: "r2", Log: lg})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `echo START; head -c 300000 /dev/zero | tr '\0' x; echo; echo END; echo s3cr3t-value`, nil, 0); err != nil {
		t.Fatal(err)
	}
	e := lg.find(KindExec, "START")
	if e == nil || !e.Truncated {
		t.Fatalf("entry = %+v", e)
	}
	if len(e.Stdout) > LogOutputLimit+100 || !strings.HasPrefix(e.Stdout, "START") || !strings.Contains(e.Stdout, "bytes omitted") {
		t.Fatalf("stdout len %d, not head+marker: %.80q", len(e.Stdout), e.Stdout)
	}
	if !strings.Contains(e.Stdout, "END") {
		t.Errorf("the tail of the output was lost")
	}
	if strings.Contains(e.Command, "s3cr3t-value") || !strings.Contains(e.Command, Redacted) {
		t.Fatalf("command not redacted: %q", e.Command)
	}
}

// A backend that cannot provision logs the failure with its message and marks the session failed.
func TestRunLogProvisionFailure(t *testing.T) {
	m := NewManager(ManagerOptions{Home: t.TempDir()})
	defer m.Close()
	lg := &collectLog{}
	if _, err := m.Open(context.Background(), "docker", RunSpec{RunID: "r3", Image: "alpine:3", Log: lg}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Open = %v", err)
	}
	e := lg.find(KindLifecycle, "Provisioning failed")
	if e == nil || e.Level != LevelError || !strings.Contains(e.Error, "not enabled") {
		t.Fatalf("failure entry = %+v", e)
	}
	if last := lg.updates[len(lg.updates)-1]; last.Status != StatusFailed || !strings.Contains(last.Error, "not enabled") {
		t.Fatalf("last update = %+v", last)
	}
}

func TestCleanRepoStripsCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:tok@github.com/o/r.git": "https://github.com/o/r.git",
		"https://github.com/o/r":              "https://github.com/o/r",
		"git@github.com:o/r.git":              "git@github.com:o/r.git",
	} {
		if got := CleanRepo(in); got != want {
			t.Errorf("CleanRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
